package cmd

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/go-logr/logr"
	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart/loader"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/yaml"
)

// End-to-end test of the cobra commands against a private envtest API server.
// KUBECONFIG points at it, so even the default-kubeconfig path cannot reach a
// real cluster.
func TestPlanShowValuesCommands(t *testing.T) {
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
	if err := os.WriteFile(kcPath, kc, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KUBECONFIG", kcPath)
	t.Setenv("HELM_CACHE_HOME", filepath.Join(dir, "helm-cache"))
	rc, _ := clientcmd.RESTConfigFromKubeConfig(kc)
	if !strings.Contains(rc.Host, "127.0.0.1") {
		t.Fatalf("refusing: envtest server %s is not loopback", rc.Host)
	}
	cs := kubernetes.NewForConfigOrDie(rc)
	if _, err := cs.CoreV1().Namespaces().Create(context.Background(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "kubesense"}}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}

	chart, _ := filepath.Abs("../pkg/plan/testdata/app")
	overrides := filepath.Join(dir, "overrides.yaml")
	if err := os.WriteFile(overrides, []byte("message: first\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) (string, error) {
		t.Helper()
		r, w, _ := os.Pipe()
		stdout := os.Stdout
		os.Stdout = w
		rootCmd.SetArgs(args)
		err := rootCmd.Execute()
		w.Close()
		os.Stdout = stdout
		out, _ := io.ReadAll(r)
		return string(out), err
	}

	// Fresh install: a change, so --detailed-exitcode exits 2. Uses the
	// default kubeconfig ($KUBECONFIG -> envtest), as a customer would.
	planFile := filepath.Join(dir, "first.plan")
	out, err := run("plan", "--chart", chart, "--release", "ks", "-f", overrides, "--out", planFile, "--detailed-exitcode")
	var ee *exitError
	if !errors.As(err, &ee) || ee.code != 2 {
		t.Fatalf("plan --detailed-exitcode on a fresh install: want exit 2, got %v\n%s", err, out)
	}
	if !strings.Contains(out, "no release -> revision 1") || !strings.Contains(out, "+ ConfigMap/ks-config") {
		t.Fatalf("plan output:\n%s", out)
	}
	if fi, err := os.Stat(planFile); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("plan file must be written 0600: %v %v", fi, err)
	}
	if out, err := run("show", planFile); err != nil || !strings.Contains(out, "+ ConfigMap/ks-config") {
		t.Fatalf("show: %v\n%s", err, out)
	}
	if _, err := cs.CoreV1().ConfigMaps("kubesense").Get(context.Background(), "ks-config", metav1.GetOptions{}); err == nil {
		t.Fatal("plan wrote to the cluster")
	}
	if _, err := run("apply", planFile); err == nil {
		t.Fatal("apply must not be a command in this release")
	}

	// Install for real, then values pull returns exactly the overrides and a
	// plan with them has nothing to do.
	flags := genericclioptions.NewConfigFlags(false)
	flags.KubeConfig = &kcPath
	ns := "kubesense"
	flags.Namespace = &ns
	cfg := new(action.Configuration)
	if err := cfg.Init(flags, ns, "secret", func(string, ...interface{}) {}); err != nil {
		t.Fatal(err)
	}
	c, err := loader.Load(chart)
	if err != nil {
		t.Fatal(err)
	}
	in := action.NewInstall(cfg)
	in.ReleaseName, in.Namespace = "ks", ns
	if _, err := in.Run(c, map[string]interface{}{"message": "first"}); err != nil {
		t.Fatal(err)
	}
	pulled := filepath.Join(dir, "pulled.yaml")
	if _, err := run("values", "pull", "--release", "ks", "-o", pulled); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(pulled)
	var got map[string]interface{}
	if err := yaml.Unmarshal(b, &got); err != nil || len(got) != 1 || got["message"] != "first" {
		t.Fatalf("values pull wrote %q (%v)", b, err)
	}
	if fi, _ := os.Stat(pulled); fi.Mode().Perm() != 0o600 {
		t.Fatalf("values file mode %v, want 0600", fi.Mode().Perm())
	}
	if out, err := run("plan", "--chart", chart, "--release", "ks", "-f", pulled, "--detailed-exitcode"); err != nil {
		t.Fatalf("plan from pulled values: want no changes (exit 0), got %v\n%s", err, out)
	}
}
