package plan

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/go-logr/logr"
	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart/loader"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"
)

// The acceptance tests run against a private kube-apiserver + etcd started by
// controller-runtime's envtest. They NEVER read ~/.kube/config: TestMain writes
// a kubeconfig into a temp dir, points KUBECONFIG at it, and asserts that its
// server is loopback before any test runs.
var (
	testTarget Target
	testCS     kubernetes.Interface
	nsCounter  int64
)

func findAssets() (string, error) {
	if d := os.Getenv("KUBEBUILDER_ASSETS"); d != "" {
		return d, nil
	}
	home, _ := os.UserHomeDir()
	for _, base := range []string{
		filepath.Join(home, "Library", "Application Support", "io.kubebuilder.envtest", "k8s"),
		filepath.Join(home, ".local", "share", "kubebuilder-envtest", "k8s"),
	} {
		m, _ := filepath.Glob(filepath.Join(base, "*"))
		sort.Strings(m)
		for i := len(m) - 1; i >= 0; i-- {
			if _, err := os.Stat(filepath.Join(m[i], "kube-apiserver")); err == nil {
				return m[i], nil
			}
		}
	}
	return "", fmt.Errorf("envtest binaries not found: run `go install sigs.k8s.io/controller-runtime/tools/setup-envtest@release-0.19 && " +
		"setup-envtest use 1.31.x` or set KUBEBUILDER_ASSETS")
}

func TestMain(m *testing.M) {
	os.Exit(run(m))
}

func run(m *testing.M) int {
	ctrllog.SetLogger(logr.Discard())
	assets, err := findAssets()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	env := &envtest.Environment{BinaryAssetsDirectory: assets}
	if _, err := env.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "envtest start:", err)
		return 1
	}
	defer env.Stop()

	user, err := env.AddUser(envtest.User{Name: "spike-admin", Groups: []string{"system:masters"}}, nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	kc, err := user.KubeConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	dir, err := os.MkdirTemp("", "kubesense-plan-spike-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer os.RemoveAll(dir)
	kcPath := filepath.Join(dir, "kubeconfig")
	if err := os.WriteFile(kcPath, kc, 0o600); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	os.Setenv("KUBECONFIG", kcPath) // anything that falls back to $KUBECONFIG still hits envtest
	cfg, err := clientcmd.RESTConfigFromKubeConfig(kc)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := assertLoopback(cfg.Host); err != nil {
		fmt.Fprintln(os.Stderr, "REFUSING TO RUN:", err)
		return 1
	}
	testCS, err = kubernetes.NewForConfig(cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	testTarget = Target{Kubeconfig: kcPath, CacheDir: filepath.Join(dir, "cache"), RequireLoopback: true}
	return m.Run()
}

// ---- helpers ----

func newNS(t *testing.T) string {
	t.Helper()
	n := fmt.Sprintf("spike-%d", atomic.AddInt64(&nsCounter, 1))
	_, err := testCS.CoreV1().Namespaces().Create(context.Background(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: n}}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// copyChart copies testdata/<name> into a temp dir so the test can modify it.
func copyChart(t *testing.T, name string) string {
	t.Helper()
	dst := filepath.Join(t.TempDir(), name)
	src := filepath.Join("testdata", name)
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		if d.IsDir() {
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dst, rel), b, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	return dst
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func editFile(t *testing.T, path, old, new string) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), old) {
		t.Fatalf("%s does not contain %q", path, old)
	}
	writeFile(t, path, strings.Replace(string(b), old, new, 1))
}

func overridesFile(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "overrides.yaml")
	writeFile(t, p, content)
	return p
}

// plainHelm returns an ungated Helm configuration - "someone else running helm".
func plainHelm(t *testing.T, ns string) *action.Configuration {
	t.Helper()
	c, err := testTarget.connect(ns, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return c.helm
}

func plainInstall(t *testing.T, ns, rel, chartDir string, vals map[string]interface{}) {
	t.Helper()
	ch, err := loader.Load(chartDir)
	if err != nil {
		t.Fatal(err)
	}
	in := action.NewInstall(plainHelm(t, ns))
	in.ReleaseName, in.Namespace = rel, ns
	if _, err := in.Run(ch, vals); err != nil {
		t.Fatalf("plain install: %v", err)
	}
}

func plainUpgrade(t *testing.T, ns, rel, chartDir string, vals map[string]interface{}) {
	t.Helper()
	ch, err := loader.Load(chartDir)
	if err != nil {
		t.Fatal(err)
	}
	up := action.NewUpgrade(plainHelm(t, ns))
	up.Namespace = ns
	if _, err := up.Run(rel, ch, vals); err != nil {
		t.Fatalf("plain upgrade: %v", err)
	}
}

// snapshot captures name -> resourceVersion for every object kind the fixtures
// can write, including Helm release records (sh.helm.release.v1.* Secrets) and
// cluster-scoped CRDs. Equal snapshots before and after apply prove that apply
// wrote nothing.
func snapshot(t *testing.T, ns string) map[string]string {
	t.Helper()
	ctx := context.Background()
	out := map[string]string{}
	secs, err := testCS.CoreV1().Secrets(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range secs.Items {
		out["Secret/"+s.Name] = s.ResourceVersion
	}
	cms, err := testCS.CoreV1().ConfigMaps(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cms.Items {
		if c.Name == "kube-root-ca.crt" {
			continue
		}
		out["ConfigMap/"+c.Name] = c.ResourceVersion
	}
	deps, err := testCS.AppsV1().Deployments(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range deps.Items {
		out["Deployment/"+d.Name] = d.ResourceVersion
	}
	c, err := testTarget.connect(ns, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	crds, err := c.dynamic.Resource(schema.GroupVersionResource{Group: "apiextensions.k8s.io", Version: "v1", Resource: "customresourcedefinitions"}).List(ctx, metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range crds.Items {
		out["CRD/"+d.GetName()] = d.GetResourceVersion()
	}
	return out
}

func releaseRecords(s map[string]string) []string {
	var out []string
	for k := range s {
		if strings.HasPrefix(k, "Secret/sh.helm.release.v1.") {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

func assertUnchanged(t *testing.T, before, after map[string]string) {
	t.Helper()
	var diffs []string
	for k, v := range before {
		if after[k] != v {
			diffs = append(diffs, fmt.Sprintf("changed/removed %s (rv %s -> %q)", k, v, after[k]))
		}
	}
	for k := range after {
		if _, ok := before[k]; !ok {
			diffs = append(diffs, "created "+k)
		}
	}
	if len(diffs) > 0 {
		sort.Strings(diffs)
		t.Fatalf("apply wrote to the cluster although it refused:\n  %s", strings.Join(diffs, "\n  "))
	}
}
