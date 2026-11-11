package plan

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	authv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// Preflight checks a cluster for what KubeSense needs before the first
// install: Kubernetes version, permissions, nodes, DNS, metrics-server,
// storage and CSI drivers, ingress, the namespace's pod security, and the
// cloud's own add-ons (EBS CSI and the AWS Load Balancer Controller on EKS,
// the PD and Azure Disk CSI drivers on GKE and AKS). It only reads.

// Level is how a check came out.
type Level string

const (
	Pass Level = "pass"
	Warn Level = "warn" // works, but something will be missing or need care
	Fail Level = "fail" // the install will not work until this is fixed
	Info Level = "info"
)

// Check is one finding.
type Check struct {
	Group  string `json:"group"`
	Name   string `json:"name"`
	Level  Level  `json:"level"`
	Detail string `json:"detail"`
	Fix    string `json:"fix,omitempty"`
}

// PreflightReport is every finding for one cluster.
type PreflightReport struct {
	Cluster   string  `json:"cluster"`
	Provider  string  `json:"provider"` // aws | gcp | azure | other
	Namespace string  `json:"namespace"`
	Checks    []Check `json:"checks"`
}

// Failed reports whether any check failed.
func (r *PreflightReport) Failed() bool {
	for _, c := range r.Checks {
		if c.Level == Fail {
			return true
		}
	}
	return false
}

// preflightEnv is what the checks read; tests swap in fakes.
type preflightEnv struct {
	cs kubernetes.Interface
	// metrics reports whether the metrics API answers (metrics-server).
	metrics func(context.Context) error
	// groups lists the API groups the server serves.
	groups func() ([]string, error)
}

// RunPreflight connects and runs every check.
func RunPreflight(ctx context.Context, t Target, namespace string) (*PreflightReport, error) {
	c, err := t.connect(namespace, nil, nil)
	if err != nil {
		return nil, err
	}
	env := preflightEnv{
		cs: c.clientset,
		metrics: func(ctx context.Context) error {
			return c.clientset.Discovery().RESTClient().Get().AbsPath("/apis/metrics.k8s.io/v1beta1/nodes").Do(ctx).Error()
		},
		groups: func() ([]string, error) {
			gl, err := c.clientset.Discovery().ServerGroups()
			if err != nil {
				return nil, err
			}
			var out []string
			for _, g := range gl.Groups {
				out = append(out, g.Name)
			}
			return out, nil
		},
	}
	r, err := preflight(ctx, env, namespace)
	if r != nil {
		r.Cluster = c.context + " (" + c.serverURL + ")"
	}
	return r, err
}

func preflight(ctx context.Context, e preflightEnv, ns string) (*PreflightReport, error) {
	r := &PreflightReport{Namespace: ns}
	add := func(group, name string, l Level, detail, fix string) {
		r.Checks = append(r.Checks, Check{Group: group, Name: name, Level: l, Detail: detail, Fix: fix})
	}

	nodes, err := e.cs.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list nodes: %w", err)
	}
	r.Provider = detectProvider(nodes.Items)

	checkVersion(e, add)
	checkPermissions(ctx, e, ns, add)
	checkNodes(nodes.Items, add)
	checkDNS(ctx, e, add)
	checkMetrics(ctx, e, add)
	drivers, err := checkStorage(ctx, e, add)
	if err != nil {
		return nil, err
	}
	checkCloud(ctx, e, r.Provider, nodes.Items, drivers, add)
	checkIngress(ctx, e, r.Provider, add)
	checkNamespace(ctx, e, ns, add)
	checkPlatform(e, add)
	return r, nil
}

func detectProvider(nodes []corev1.Node) string {
	for _, n := range nodes {
		switch id := n.Spec.ProviderID; {
		case strings.HasPrefix(id, "aws://"):
			return "aws"
		case strings.HasPrefix(id, "gce://"):
			return "gcp"
		case strings.HasPrefix(id, "azure://"):
			return "azure"
		}
	}
	return "other"
}

type addFn func(group, name string, l Level, detail, fix string)

func checkVersion(e preflightEnv, add addFn) {
	v, err := e.cs.Discovery().ServerVersion()
	if err != nil {
		add("Cluster", "Kubernetes version", Warn, "could not read: "+err.Error(), "")
		return
	}
	minor, _ := strconv.Atoi(strings.TrimRight(v.Minor, "+"))
	if v.Major == "1" && minor < 21 {
		add("Cluster", "Kubernetes version", Fail, v.GitVersion+" is older than 1.21", "upgrade the cluster to 1.21 or later")
		return
	}
	add("Cluster", "Kubernetes version", Pass, v.GitVersion, "")
}

// checkPermissions asks the API server what the current user may do.
func checkPermissions(ctx context.Context, e preflightEnv, ns string, add addFn) {
	can := func(verb, group, res, namespace string) bool {
		rev, err := e.cs.AuthorizationV1().SelfSubjectAccessReviews().Create(ctx, &authv1.SelfSubjectAccessReview{
			Spec: authv1.SelfSubjectAccessReviewSpec{ResourceAttributes: &authv1.ResourceAttributes{
				Verb: verb, Group: group, Resource: res, Namespace: namespace,
			}},
		}, metav1.CreateOptions{})
		return err == nil && rev.Status.Allowed
	}
	if can("*", "*", "*", "") {
		add("Cluster", "Permissions", Pass, "cluster-admin", "")
		return
	}
	need := []struct{ verb, group, res, ns string }{
		{"create", "", "namespaces", ""},
		{"create", "rbac.authorization.k8s.io", "clusterroles", ""},
		{"create", "rbac.authorization.k8s.io", "clusterrolebindings", ""},
		{"create", "apps", "deployments", ns},
		{"create", "apps", "statefulsets", ns},
		{"create", "apps", "daemonsets", ns},
		{"create", "", "secrets", ns},
		{"create", "", "persistentvolumeclaims", ns},
	}
	var missing []string
	for _, n := range need {
		if !can(n.verb, n.group, n.res, n.ns) {
			missing = append(missing, n.verb+" "+n.res)
		}
	}
	if len(missing) > 0 {
		add("Cluster", "Permissions", Fail, "your user cannot: "+strings.Join(missing, ", "),
			"use a cluster-admin context, or grant the RBAC in the self-hosted deployment SOP §2.3")
		return
	}
	add("Cluster", "Permissions", Pass, "can create everything KubeSense installs", "")
}

func checkNodes(nodes []corev1.Node, add addFn) {
	var ready, amd64, tainted, windows, fargate int
	cpu, mem := resource.Quantity{}, resource.Quantity{}
	for _, n := range nodes {
		isReady := false
		for _, c := range n.Status.Conditions {
			if c.Type == corev1.NodeReady && c.Status == corev1.ConditionTrue {
				isReady = true
			}
		}
		if n.Labels["kubernetes.io/os"] == "windows" {
			windows++
		}
		if n.Labels["eks.amazonaws.com/compute-type"] == "fargate" {
			fargate++
		}
		for _, t := range n.Spec.Taints {
			if t.Effect == corev1.TaintEffectNoSchedule || t.Effect == corev1.TaintEffectNoExecute {
				tainted++
				break
			}
		}
		if !isReady || n.Spec.Unschedulable {
			continue
		}
		ready++
		if n.Labels["kubernetes.io/arch"] == "amd64" || n.Status.NodeInfo.Architecture == "amd64" {
			amd64++
			cpu.Add(*n.Status.Allocatable.Cpu())
			mem.Add(*n.Status.Allocatable.Memory())
		}
	}
	switch {
	case ready == 0:
		add("Nodes", "Ready nodes", Fail, fmt.Sprintf("0 of %d nodes are Ready and schedulable", len(nodes)), "")
	default:
		add("Nodes", "Ready nodes", Pass, fmt.Sprintf("%d of %d Ready and schedulable", ready, len(nodes)), "")
	}
	if ready > 0 && amd64 == 0 {
		add("Nodes", "x86-64 nodes", Fail, "no Ready amd64 node: the server components only run on x86-64",
			"add an amd64 node pool (agents run on arm64 too)")
	} else if amd64 > 0 {
		gib := float64(mem.Value()) / (1 << 30)
		detail := fmt.Sprintf("%d amd64 nodes, %.0f vCPU and %.0f GiB allocatable", amd64, float64(cpu.MilliValue())/1000, gib)
		if cpu.Cmp(resource.MustParse("2")) < 0 || gib < 8 {
			add("Nodes", "Capacity", Fail, detail+": below the 2 vCPU / 8 GiB minimum", "add nodes")
		} else {
			add("Nodes", "Capacity", Info, detail, "size from your ingest volume and retention (SOP §1.2); 2 vCPU / 8 GiB is a demo floor")
		}
	}
	if tainted > 0 {
		add("Nodes", "Taints", Info, fmt.Sprintf("%d nodes have NoSchedule/NoExecute taints", tainted),
			"to run KubeSense or the logsensor there, set global.tolerations and kubesensor.logsensor.tolerations")
	}
	if windows > 0 {
		add("Nodes", "Windows nodes", Info, fmt.Sprintf("%d Windows nodes: the logsensor does not run on them", windows), "")
	}
	if fargate > 0 {
		add("Nodes", "Fargate", Warn, fmt.Sprintf("%d EKS Fargate nodes: DaemonSets (the logsensor) do not run on Fargate", fargate),
			"collect Fargate pod logs another way (SOP: EKS Fargate logs)")
	}
}

func readyDeployment(ctx context.Context, cs kubernetes.Interface, ns, name string) (found bool, ready, want int32) {
	d, err := cs.AppsV1().Deployments(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return false, 0, 0
	}
	want = 1
	if d.Spec.Replicas != nil {
		want = *d.Spec.Replicas
	}
	return true, d.Status.ReadyReplicas, want
}

// findDeployment looks for a deployment by name in kube-system, then by the
// app.kubernetes.io/name label anywhere (Helm installs put it elsewhere).
func findDeployment(ctx context.Context, cs kubernetes.Interface, name string) (ns string, ready, want int32, ok bool) {
	if found, r, w := readyDeployment(ctx, cs, "kube-system", name); found {
		return "kube-system", r, w, true
	}
	l, err := cs.AppsV1().Deployments("").List(ctx, metav1.ListOptions{LabelSelector: "app.kubernetes.io/name=" + name})
	if err != nil || len(l.Items) == 0 {
		return "", 0, 0, false
	}
	d := l.Items[0]
	want = 1
	if d.Spec.Replicas != nil {
		want = *d.Spec.Replicas
	}
	return d.Namespace, d.Status.ReadyReplicas, want, true
}

func deploymentCheck(ctx context.Context, cs kubernetes.Interface, add addFn, group, title string, names []string, missing Level, missingDetail, fix string) bool {
	for _, n := range names {
		ns, ready, want, ok := findDeployment(ctx, cs, n)
		if !ok {
			continue
		}
		where := ns + "/" + n
		if ready == 0 {
			add(group, title, Fail, fmt.Sprintf("%s has no ready pods (0/%d)", where, want), "kubectl -n "+ns+" describe deployment "+n)
		} else {
			add(group, title, Pass, fmt.Sprintf("%s %d/%d ready", where, ready, want), "")
		}
		return true
	}
	add(group, title, missing, missingDetail, fix)
	return false
}

func checkDNS(ctx context.Context, e preflightEnv, add addFn) {
	deploymentCheck(ctx, e.cs, add, "Cluster", "Cluster DNS", []string{"coredns", "kube-dns"}, Fail,
		"no CoreDNS or kube-dns deployment found: pods cannot resolve service names",
		"install CoreDNS (EKS: the coredns add-on)")
}

func checkMetrics(ctx context.Context, e preflightEnv, add addFn) {
	if err := e.metrics(ctx); err == nil {
		add("Cluster", "metrics-server", Pass, "metrics API answers", "")
		return
	}
	add("Cluster", "metrics-server", Warn, "metrics API (metrics.k8s.io) is not available: autoscaling (HPA) and kubectl top will not work",
		"install metrics-server (EKS: the metrics-server add-on; GKE and AKS ship it)")
}

// rwxProvisioners can give ReadWriteMany volumes.
var rwxProvisioners = []string{"efs.csi.aws.com", "file.csi.azure.com", "filestore.csi.storage.gke.io", "nfs", "cephfs", "kubernetes.io/azure-file"}

func checkStorage(ctx context.Context, e preflightEnv, add addFn) (map[string]bool, error) {
	drivers := map[string]bool{}
	dl, err := e.cs.StorageV1().CSIDrivers().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list CSI drivers: %w", err)
	}
	for _, d := range dl.Items {
		drivers[d.Name] = true
	}
	scs, err := e.cs.StorageV1().StorageClasses().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list storage classes: %w", err)
	}
	var def []string
	var rwx []string
	for _, sc := range scs.Items {
		if sc.Annotations["storageclass.kubernetes.io/is-default-class"] == "true" {
			def = append(def, sc.Name)
			l, d := Pass, fmt.Sprintf("%s (%s)", sc.Name, sc.Provisioner)
			fix := ""
			if sc.AllowVolumeExpansion == nil || !*sc.AllowVolumeExpansion {
				l, d = Warn, d+": allowVolumeExpansion is off, so ClickHouse disks cannot be grown later"
				fix = "use a class with allowVolumeExpansion: true (set its name in the values' storageClass)"
			}
			add("Storage", "Default StorageClass", l, d, fix)
			if strings.Contains(sc.Provisioner, ".csi.") && !drivers[sc.Provisioner] {
				add("Storage", "Provisioner", Fail, fmt.Sprintf("default class %s uses %s, which has no CSIDriver in the cluster: volumes will stay Pending", sc.Name, sc.Provisioner),
					"install the "+sc.Provisioner+" CSI driver")
			}
		}
		for _, p := range rwxProvisioners {
			if strings.Contains(sc.Provisioner, p) {
				rwx = append(rwx, sc.Name)
				break
			}
		}
	}
	switch len(def) {
	case 0:
		l := Fail
		if len(scs.Items) > 0 {
			l = Warn
		}
		add("Storage", "Default StorageClass", l, fmt.Sprintf("no default StorageClass (%d classes exist)", len(scs.Items)),
			"mark one default, or set storageClass explicitly in your values")
	case 1:
	default:
		add("Storage", "Default StorageClass", Warn, "more than one default: "+strings.Join(def, ", "), "keep exactly one default")
	}
	if len(rwx) > 0 {
		add("Storage", "ReadWriteMany", Info, "classes that can do ReadWriteMany: "+strings.Join(rwx, ", "), "")
	} else {
		add("Storage", "ReadWriteMany", Info, "no ReadWriteMany class: needed only for dynamic log parsing",
			"use the NFS chart (SOP §2.4), or EFS / Azure Files / Filestore")
	}
	return drivers, nil
}

func checkCloud(ctx context.Context, e preflightEnv, provider string, nodes []corev1.Node, drivers map[string]bool, add addFn) {
	need := func(driver, title, fix string) {
		if drivers[driver] {
			add("Cloud", title, Pass, driver+" installed", "")
		} else {
			add("Cloud", title, Fail, driver+" not installed: disk volumes (ClickHouse, VictoriaMetrics) stay Pending", fix)
		}
	}
	switch provider {
	case "aws":
		need("ebs.csi.aws.com", "EBS CSI driver",
			"install the aws-ebs-csi-driver add-on with an IAM role (EKS 1.23+ needs it even for the gp2 class)")
		if drivers["ebs.csi.aws.com"] {
			deploymentCheck(ctx, e.cs, add, "Cloud", "EBS CSI controller", []string{"ebs-csi-controller"}, Warn,
				"ebs-csi-controller deployment not found", "check the aws-ebs-csi-driver add-on")
		}
		deploymentCheck(ctx, e.cs, add, "Cloud", "AWS Load Balancer Controller", []string{"aws-load-balancer-controller"}, Warn,
			"not installed: LoadBalancer Services get a legacy Classic ELB and ALB/NLB Ingress is unavailable",
			"install aws-load-balancer-controller (Helm chart eks/aws-load-balancer-controller) with its IAM role")
		if drivers["efs.csi.aws.com"] {
			add("Cloud", "EFS CSI driver", Info, "installed (ReadWriteMany available)", "")
		}
	case "gcp":
		need("pd.csi.storage.gke.io", "Persistent Disk CSI driver", "enable GcePersistentDiskCsiDriver on the cluster")
		for _, n := range nodes {
			if strings.HasPrefix(n.Name, "gk3-") {
				add("Cloud", "GKE Autopilot", Warn, "looks like Autopilot: hostPath mounts are restricted, so the logsensor may be refused",
					"use a Standard cluster, or ship logs from the application side")
				break
			}
		}
	case "azure":
		need("disk.csi.azure.com", "Azure Disk CSI driver", "enable the disk CSI driver: az aks update --enable-disk-driver")
		if drivers["file.csi.azure.com"] {
			add("Cloud", "Azure Files CSI driver", Info, "installed (ReadWriteMany available)", "")
		}
	default:
		add("Cloud", "Provider", Info, "not AWS, GCP or Azure (from node providerID): no cloud add-on checks", "")
	}
}

func checkIngress(ctx context.Context, e preflightEnv, provider string, add addFn) {
	l, err := e.cs.NetworkingV1().IngressClasses().List(ctx, metav1.ListOptions{})
	if err != nil {
		add("Network", "Ingress controller", Warn, "could not list IngressClasses: "+err.Error(), "")
		return
	}
	if len(l.Items) == 0 {
		fix := "install one (SOP §2.5: ingress-nginx), or keep global.ingress.enabled false and expose the dashboard yourself"
		if provider == "gcp" {
			add("Network", "Ingress controller", Info, "no IngressClass: GKE's built-in gce ingress still works with the kubernetes.io/ingress.class annotation", "")
			return
		}
		add("Network", "Ingress controller", Warn, "no IngressClass: the dashboard Ingress will not get an address", fix)
		return
	}
	var names []string
	for _, ic := range l.Items {
		n := ic.Name + " (" + ic.Spec.Controller + ")"
		if ic.Annotations["ingressclass.kubernetes.io/is-default-class"] == "true" {
			n += " default"
		}
		names = append(names, n)
	}
	sort.Strings(names)
	add("Network", "Ingress controller", Pass, strings.Join(names, ", "), "")
}

func checkNamespace(ctx context.Context, e preflightEnv, ns string, add addFn) {
	n, err := e.cs.CoreV1().Namespaces().Get(ctx, ns, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		add("Namespace", ns, Info, "does not exist yet; apply creates it", "")
		return
	}
	if err != nil {
		add("Namespace", ns, Warn, "could not read: "+err.Error(), "")
		return
	}
	switch lvl := n.Labels["pod-security.kubernetes.io/enforce"]; lvl {
	case "baseline", "restricted":
		add("Namespace", "Pod security", Fail, "enforces "+lvl+": the logsensor's hostPath /var/log mount is refused",
			"kubectl label namespace "+ns+" pod-security.kubernetes.io/enforce=privileged --overwrite")
	default:
		add("Namespace", "Pod security", Pass, "allows the logsensor's hostPath mount", "")
	}
	secs, err := e.cs.CoreV1().Secrets(ns).List(ctx, metav1.ListOptions{LabelSelector: "owner=helm"})
	if err == nil {
		rels := map[string]bool{}
		for _, s := range secs.Items {
			rels[s.Labels["name"]] = true
		}
		if len(rels) > 0 {
			var names []string
			for k := range rels {
				names = append(names, k)
			}
			sort.Strings(names)
			add("Namespace", "Existing releases", Info, strings.Join(names, ", "), "upgrade with kubesense plan and apply")
		}
	}
}

func checkPlatform(e preflightEnv, add addFn) {
	gs, err := e.groups()
	if err != nil {
		return
	}
	for _, g := range gs {
		if g == "security.openshift.io" {
			add("Cluster", "OpenShift", Warn, "OpenShift: pods need the SCC from the SOP",
				"apply the SCC and re-bind it after every install (SOP §2.3)")
			return
		}
	}
}

// Show prints the report grouped, worst first within each group.
func (r *PreflightReport) Show(w io.Writer) {
	fmt.Fprintf(w, "%s %s\n%s %s\n%s %s\n", Bold("Cluster:  "), r.Cluster, Bold("Provider: "), r.Provider, Bold("Namespace:"), r.Namespace)
	order := []string{"Cluster", "Nodes", "Storage", "Cloud", "Network", "Namespace"}
	counts := map[Level]int{}
	for _, g := range order {
		var cs []Check
		for _, c := range r.Checks {
			if c.Group == g {
				cs = append(cs, c)
			}
		}
		if len(cs) == 0 {
			continue
		}
		fmt.Fprintf(w, "\n%s\n", Bold(g))
		for _, c := range cs {
			counts[c.Level]++
			var mark string
			switch c.Level {
			case Pass:
				mark = Green("✓")
			case Warn:
				mark = Yellow("!")
			case Fail:
				mark = Red("✗")
			default:
				mark = Dim("·")
			}
			fmt.Fprintf(w, "  %s %-28s %s\n", mark, c.Name, c.Detail)
			if c.Fix != "" && c.Level != Pass {
				fmt.Fprintf(w, "    %s %s\n", Dim("→"), c.Fix)
			}
		}
	}
	summary := fmt.Sprintf("%d passed, %d warnings, %d failed", counts[Pass], counts[Warn], counts[Fail])
	switch {
	case counts[Fail] > 0:
		summary = Red(summary + ": fix the failures before installing")
	case counts[Warn] > 0:
		summary = Yellow(summary + ": ready to install; read the warnings")
	default:
		summary = Green(summary + ": ready to install")
	}
	fmt.Fprintf(w, "\n%s\n%s\n", summary, Dim("Not checked: outbound HTTPS from the cluster to the KubeSense registry and licence server."))
}

// JSON writes the report as JSON.
func (r *PreflightReport) JSON(w io.Writer) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}
