package plan

import (
	"context"
	"errors"
	"net"
	"path/filepath"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func nfsPlan(t *testing.T, f *fixture, values string, nfs bool) (*Plan, error) {
	t.Helper()
	chart, _ := filepath.Abs("testdata/nfs")
	return MakePlan(context.Background(), Options{
		Target: testTarget, ReleaseName: relName, Namespace: f.ns, ChartPath: f.chart,
		OverridesFile: overridesFile(t, values), NFS: nfs, NFSChart: chart,
	})
}

func TestNFSInstallSetsHostThenReuses(t *testing.T) {
	ctx := context.Background()
	f := &fixture{t: t, mode: "upgrade", ns: newNS(t), chart: copyChart(t, "app")}
	plainInstall(t, f.ns, relName, f.chart, map[string]interface{}{"message": "same"})
	vals := "message: same\nglobal:\n  image: {pullSecrets: my-pull}\n  dynamicLogParsing: {enabled: true}\n  alertconfig: {enabled: false}\n"

	p, err := nfsPlan(t, f, vals, true)
	if err != nil {
		t.Fatal(err)
	}
	if p.NFS == nil || p.NFS.Action != "install" || strings.Join(p.NFS.Paths, ",") != "global.dynamicLogParsing.nfs.host" {
		t.Fatalf("step %+v", p.NFS)
	}
	if out := shown(p); !strings.Contains(out, "+ install NFS server (release kubesense-nfs") {
		t.Fatalf("plan output:\n%s", out)
	}
	res, err := Apply(ctx, p, ApplyOptions{Target: testTarget})
	if err != nil || !strings.Contains(res.NFS, "NFS server installed") {
		t.Fatalf("apply: %+v %v", res, err)
	}
	svc, err := testCS.CoreV1().Services(f.ns).Get(ctx, NFSService, metav1.GetOptions{})
	if err != nil || svc.Annotations["pull-secret"] != "my-pull" {
		t.Fatalf("nfs service: %v %v", err, svc)
	}
	ip := svc.Spec.ClusterIP
	if net.ParseIP(ip) == nil {
		t.Fatalf("no ClusterIP: %q", ip)
	}
	c, err := testTarget.connect(f.ns, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	rel, err := c.helm.Releases.Last(relName)
	if err != nil {
		t.Fatal(err)
	}
	nfs := getMap(rel.Config, "global", "dynamicLogParsing", "nfs")
	if nfs["host"] != ip || nfs["enabled"] != true {
		t.Fatalf("release values nfs = %v, want host %s", nfs, ip)
	}
	if getMap(rel.Config, "global", "alertconfig", "nfs") != nil {
		t.Fatal("alertconfig is off: must not be pointed at NFS")
	}

	// Next plan finds the server and uses its IP; nothing to install.
	p2, err := nfsPlan(t, f, vals, true)
	if err != nil {
		t.Fatal(err)
	}
	if p2.NFS.Action != "use" || p2.HasChanges() {
		t.Fatalf("second plan: step %+v changes %v %v", p2.NFS, p2.Changes, p2.ValueChanges)
	}
	if !strings.Contains(shown(p2), "using NFS server "+ip) {
		t.Fatalf("plan output:\n%s", shown(p2))
	}
}

func TestNFSNeedsAFeatureAndNoStrict(t *testing.T) {
	f := &fixture{t: t, mode: "upgrade", ns: newNS(t), chart: copyChart(t, "app")}
	plainInstall(t, f.ns, relName, f.chart, map[string]interface{}{"message": "same"})
	if _, err := nfsPlan(t, f, "message: same\n", true); err == nil || !strings.Contains(err.Error(), "nothing uses NFS") {
		t.Fatalf("want nothing-uses-NFS error, got %v", err)
	}
	_, err := nfsPlan(t, f, "message: same\nglobal:\n  dynamicLogParsing: {enabled: true, filesystem: {enabled: true}}\n", true)
	if err == nil || !strings.Contains(err.Error(), "not both") {
		t.Fatalf("want filesystem conflict, got %v", err)
	}
	chart, _ := filepath.Abs("testdata/nfs")
	_, err = MakePlan(context.Background(), Options{Target: testTarget, ReleaseName: relName, Namespace: f.ns, ChartPath: f.chart, Strict: true,
		AllowImpureTemplates: true, OverridesFile: overridesFile(t, "message: same\nglobal:\n  alertconfig: {enabled: true}\n"), NFS: true, NFSChart: chart})
	if err == nil || !strings.Contains(err.Error(), "--strict") {
		t.Fatalf("want strict refusal, got %v", err)
	}
	var r *Refusal
	_ = errors.As(err, &r)
}

func TestSharedStorageWarnings(t *testing.T) {
	on := func(extra map[string]interface{}) map[string]interface{} {
		f := map[string]interface{}{"enabled": true}
		for k, v := range extra {
			f[k] = v
		}
		return map[string]interface{}{"global": map[string]interface{}{"dynamicLogParsing": f}}
	}
	if w := sharedStorageWarnings(on(nil), false); len(w) != 1 || !strings.Contains(w[0], "add --nfs") {
		t.Fatalf("want a warning, got %v", w)
	}
	for _, v := range []map[string]interface{}{
		on(map[string]interface{}{"nfs": map[string]interface{}{"host": "10.0.0.5"}}),
		on(map[string]interface{}{"filesystem": map[string]interface{}{"enabled": true}}),
	} {
		if w := sharedStorageWarnings(v, false); len(w) != 0 {
			t.Fatalf("storage given, want no warning: %v", w)
		}
	}
	if w := sharedStorageWarnings(on(nil), true); len(w) != 0 {
		t.Fatalf("--nfs given: %v", w)
	}
}
