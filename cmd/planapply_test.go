package cmd

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"
)

// End-to-end smoke test of the cobra commands against a private envtest API
// server (never the default kubeconfig).
func TestPlanShowApplyCommands(t *testing.T) {
	assets := os.Getenv("KUBEBUILDER_ASSETS")
	if assets == "" {
		home, _ := os.UserHomeDir()
		m, _ := filepath.Glob(filepath.Join(home, "Library", "Application Support", "io.kubebuilder.envtest", "k8s", "*"))
		m2, _ := filepath.Glob(filepath.Join(home, ".local", "share", "kubebuilder-envtest", "k8s", "*"))
		m = append(m, m2...)
		sort.Strings(m)
		if len(m) == 0 {
			t.Fatal("envtest binaries not found; see pkg/plan/env_test.go")
		}
		assets = m[len(m)-1]
	}
	ctrllog.SetLogger(logr.Discard())
	env := &envtest.Environment{BinaryAssetsDirectory: assets}
	if _, err := env.Start(); err != nil {
		t.Fatal(err)
	}
	defer env.Stop()
	user, err := env.AddUser(envtest.User{Name: "smoke", Groups: []string{"system:masters"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	kc, _ := user.KubeConfig()
	dir := t.TempDir()
	kcPath := filepath.Join(dir, "kubeconfig")
	os.WriteFile(kcPath, kc, 0o600)
	t.Setenv("KUBECONFIG", kcPath)
	rc, _ := clientcmd.RESTConfigFromKubeConfig(kc)
	if !strings.Contains(rc.Host, "127.0.0.1") {
		t.Fatalf("refusing: envtest server %s is not loopback", rc.Host)
	}
	cs := kubernetes.NewForConfigOrDie(rc)
	if _, err := cs.CoreV1().Namespaces().Create(context.Background(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "kubesense"}}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}

	chart, _ := filepath.Abs("../pkg/plan/testdata/app")
	planFile := filepath.Join(dir, "prod.plan")
	run := func(args ...string) {
		t.Helper()
		rootCmd.SetArgs(args)
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
	}
	run("plan", "--kubeconfig", kcPath, "--chart", chart, "--release", "ks", "--out", planFile)
	run("show", planFile)
	run("apply", planFile, "--kubeconfig", kcPath)
	if _, err := cs.AppsV1().Deployments("kubesense").Get(context.Background(), "ks-api", metav1.GetOptions{}); err != nil {
		t.Fatalf("deployment not created: %v", err)
	}
	// Applying the same plan again must refuse (release baseline moved on).
	rootCmd.SetArgs([]string{"apply", planFile, "--kubeconfig", kcPath})
	if err := rootCmd.Execute(); err == nil || !strings.Contains(err.Error(), "release-baseline") {
		t.Fatalf("second apply: want release-baseline refusal, got %v", err)
	}
}
