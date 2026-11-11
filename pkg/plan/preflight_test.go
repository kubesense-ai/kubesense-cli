package plan

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	authv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	netv1 "k8s.io/api/networking/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/version"
	fakediscovery "k8s.io/client-go/discovery/fake"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func pfNode(name, providerID, arch string) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{"kubernetes.io/arch": arch, "kubernetes.io/os": "linux"}},
		Spec:       corev1.NodeSpec{ProviderID: providerID},
		Status: corev1.NodeStatus{
			Conditions:  []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}},
			Allocatable: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("8"), corev1.ResourceMemory: resource.MustParse("32Gi")},
		},
	}
}

func pfDeploy(ns, name string, ready int32) *appsv1.Deployment {
	one := int32(1)
	return &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, Labels: map[string]string{"app.kubernetes.io/name": name}},
		Spec: appsv1.DeploymentSpec{Replicas: &one}, Status: appsv1.DeploymentStatus{ReadyReplicas: ready}}
}

func pfClass(name, prov string, def, expand bool) *storagev1.StorageClass {
	sc := &storagev1.StorageClass{ObjectMeta: metav1.ObjectMeta{Name: name, Annotations: map[string]string{}}, Provisioner: prov, AllowVolumeExpansion: &expand}
	if def {
		sc.Annotations["storageclass.kubernetes.io/is-default-class"] = "true"
	}
	return sc
}

func pfRun(t *testing.T, admin, metrics bool, objs ...runtime.Object) *PreflightReport {
	t.Helper()
	cs := fake.NewSimpleClientset(objs...)
	cs.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{Major: "1", Minor: "30", GitVersion: "v1.30.4"}
	cs.PrependReactor("create", "selfsubjectaccessreviews", func(a k8stesting.Action) (bool, runtime.Object, error) {
		return true, &authv1.SelfSubjectAccessReview{Status: authv1.SubjectAccessReviewStatus{Allowed: admin}}, nil
	})
	env := preflightEnv{cs: cs,
		metrics: func(context.Context) error {
			if metrics {
				return nil
			}
			return errors.New("not found")
		},
		groups: func() ([]string, error) { return nil, nil },
	}
	r, err := preflight(context.Background(), env, "kubesense")
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func level(r *PreflightReport, name string) Level {
	for _, c := range r.Checks {
		if c.Name == name {
			return c.Level
		}
	}
	return ""
}

func TestPreflightEKSMissingAddons(t *testing.T) {
	r := pfRun(t, true, false,
		pfNode("ip-10-0-1-1", "aws:///us-east-1a/i-1", "amd64"),
		pfDeploy("kube-system", "coredns", 2),
		pfClass("gp2", "kubernetes.io/aws-ebs", true, false),
	)
	want := map[string]Level{
		"EBS CSI driver": Fail, "AWS Load Balancer Controller": Warn, "metrics-server": Warn,
		"Default StorageClass": Warn, "Ingress controller": Warn, "Cluster DNS": Pass, "Permissions": Pass,
	}
	for n, l := range want {
		if got := level(r, n); got != l {
			t.Errorf("%s = %q, want %q", n, got, l)
		}
	}
	if r.Provider != "aws" || !r.Failed() {
		t.Fatalf("provider %s failed %v", r.Provider, r.Failed())
	}
}

func TestPreflightEKSReady(t *testing.T) {
	r := pfRun(t, true, true,
		pfNode("ip-10-0-1-1", "aws:///us-east-1a/i-1", "amd64"),
		pfDeploy("kube-system", "coredns", 2),
		pfDeploy("kube-system", "ebs-csi-controller", 2),
		pfDeploy("aws-lbc", "aws-load-balancer-controller", 1),
		&storagev1.CSIDriver{ObjectMeta: metav1.ObjectMeta{Name: "ebs.csi.aws.com"}},
		pfClass("gp3", "ebs.csi.aws.com", true, true),
		&netv1.IngressClass{ObjectMeta: metav1.ObjectMeta{Name: "alb"}, Spec: netv1.IngressClassSpec{Controller: "ingress.k8s.aws/alb"}},
	)
	if r.Failed() {
		var b bytes.Buffer
		r.Show(&b)
		t.Fatalf("expected no failures:\n%s", b.String())
	}
	if level(r, "AWS Load Balancer Controller") != Pass {
		t.Error("LBC in another namespace should be found by label")
	}
}

func TestPreflightAKSAndPodSecurity(t *testing.T) {
	r := pfRun(t, false, true,
		pfNode("aks-pool-1", "azure:///subscriptions/x/vm/0", "arm64"),
		pfDeploy("kube-system", "coredns", 0),
		pfClass("managed-csi", "disk.csi.azure.com", true, true),
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "kubesense", Labels: map[string]string{"pod-security.kubernetes.io/enforce": "baseline"}}},
	)
	for n, l := range map[string]Level{
		"Azure Disk CSI driver": Fail, // no CSIDriver object
		"Provisioner":           Fail,
		"x86-64 nodes":          Fail,
		"Cluster DNS":           Fail,
		"Permissions":           Fail,
		"Pod security":          Fail,
	} {
		if got := level(r, n); got != l {
			t.Errorf("%s = %q, want %q", n, got, l)
		}
	}
	var b bytes.Buffer
	r.Show(&b)
	if !strings.Contains(b.String(), "pod-security.kubernetes.io/enforce=privileged") {
		t.Error("fix for pod security not shown")
	}
}

func TestPreflightOldKubernetes(t *testing.T) {
	cs := fake.NewSimpleClientset(pfNode("n", "gce://p/z/n", "amd64"))
	cs.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{Major: "1", Minor: "20+", GitVersion: "v1.20.15"}
	r, err := preflight(context.Background(), preflightEnv{cs: cs, metrics: func(context.Context) error { return nil }, groups: func() ([]string, error) { return []string{"security.openshift.io"}, nil }}, "kubesense")
	if err != nil {
		t.Fatal(err)
	}
	if level(r, "Kubernetes version") != Fail || r.Provider != "gcp" || level(r, "OpenShift") != Warn {
		t.Fatalf("got %+v", r.Checks)
	}
}

func TestPreflightNonCSIProvisioner(t *testing.T) {
	r := pfRun(t, true, true, pfNode("kind", "kind://docker/x", "amd64"), pfDeploy("kube-system", "coredns", 1),
		pfClass("standard", "rancher.io/local-path", true, true))
	if level(r, "Provisioner") != "" {
		t.Fatal("local-path is not a CSI driver; must not need a CSIDriver object")
	}
}
