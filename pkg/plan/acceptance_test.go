package plan

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"helm.sh/helm/v3/pkg/action"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// Acceptance criterion (CONTROL-PLANE-DESIGN.md §3.5): for install AND upgrade,
// each change between plan and apply makes apply refuse with no deployment
// resource and no Helm release record written. Every refusal test below takes a
// snapshot (resourceVersion of every Secret incl. sh.helm.release.v1.*,
// ConfigMap, Deployment and CRD) immediately before apply and compares it after.

const relName = "ks"

var modes = []string{"install", "upgrade"}

type fixture struct {
	t         *testing.T
	mode      string
	ns        string
	chart     string
	overrides string
}

func setup(t *testing.T, mode, chartName string) *fixture {
	t.Helper()
	f := &fixture{t: t, mode: mode, ns: newNS(t), chart: copyChart(t, chartName)}
	switch chartName {
	case "app":
		f.overrides = overridesFile(t, "message: planned\nimageTag: \"1.37\"\n")
	default:
		f.overrides = overridesFile(t, "{}\n")
	}
	if chartName == "lookup" {
		f.setInput("lookup-input", "a")
		f.setInput("lookup-hook-input", "a")
	}
	if mode == "upgrade" {
		plainInstall(t, f.ns, relName, f.chart, nil) // revision 1, managed (chart renders deployment-info)
	}
	return f
}

func (f *fixture) setInput(name, value string) {
	ctx := context.Background()
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: name}, Data: map[string]string{"value": value}}
	if _, err := testCS.CoreV1().ConfigMaps(f.ns).Get(ctx, name, metav1.GetOptions{}); err == nil {
		_, err = testCS.CoreV1().ConfigMaps(f.ns).Update(ctx, cm, metav1.UpdateOptions{})
		if err != nil {
			f.t.Fatal(err)
		}
		return
	}
	if _, err := testCS.CoreV1().ConfigMaps(f.ns).Create(ctx, cm, metav1.CreateOptions{}); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) plan(allowImpure bool) *Plan {
	f.t.Helper()
	p, err := MakePlan(context.Background(), Options{
		Target: testTarget, ReleaseName: relName, Namespace: f.ns, ChartPath: f.chart,
		OverridesFile: f.overrides, AllowImpureTemplates: allowImpure, Strict: true,
	})
	if err != nil {
		f.t.Fatalf("plan: %v", err)
	}
	if p.Action != f.mode {
		f.t.Fatalf("plan action %s, want %s", p.Action, f.mode)
	}
	// Round-trip through the plan file, as `kubesense apply <file>` would.
	path := filepath.Join(f.t.TempDir(), "test.plan")
	if err := p.Save(path); err != nil {
		f.t.Fatal(err)
	}
	p2, err := LoadPlan(path)
	if err != nil {
		f.t.Fatal(err)
	}
	return p2
}

// refuse applies p and asserts a refusal with the given code/stage and an
// unchanged cluster. inWindow, if set, runs after every precheck passed and
// right before Helm is invoked; the "before" snapshot is taken after it so
// the mutation itself is not counted as an apply write.
func (f *fixture) refuse(p *Plan, o ApplyOptions, inWindow func(), wantCode, wantStage string) *Refusal {
	f.t.Helper()
	o.Target = testTarget
	var before map[string]string
	if inWindow != nil {
		o.beforeExecute = func() { inWindow(); before = snapshot(f.t, f.ns) }
	} else {
		before = snapshot(f.t, f.ns)
	}
	_, err := Apply(context.Background(), p, o)
	if err == nil {
		f.t.Fatalf("apply succeeded, want refusal %s at %s", wantCode, wantStage)
	}
	after := snapshot(f.t, f.ns)
	assertUnchanged(f.t, before, after)
	var r *Refusal
	if !errors.As(err, &r) {
		f.t.Fatalf("apply failed without a Refusal (but wrote nothing): %v", err)
	}
	if (wantCode != "" && r.Code != wantCode) || (wantStage != "" && r.Stage != wantStage) {
		f.t.Fatalf("refusal %s at %s, want %s at %s: %v", r.Code, r.Stage, wantCode, wantStage, err)
	}
	f.t.Logf("refused as expected: %v", err)
	return r
}

func forModes(t *testing.T, fn func(t *testing.T, mode string)) {
	for _, m := range modes {
		m := m
		t.Run(m, func(t *testing.T) { fn(t, m) })
	}
}

// ---------------------------------------------------------------- positive

func TestAccept_Positive(t *testing.T) {
	forModes(t, func(t *testing.T, mode string) {
		f := setup(t, mode, "app")
		p := f.plan(false)
		var buf bytes.Buffer
		p.Show(&buf)
		t.Logf("show:\n%s", buf.String())

		before := releaseRecords(snapshot(t, f.ns))
		res, err := Apply(context.Background(), p, ApplyOptions{Target: testTarget})
		if err != nil {
			t.Fatalf("apply: %v", err)
		}
		after := releaseRecords(snapshot(t, f.ns))
		if len(after) != len(before)+1 {
			t.Fatalf("release records %v -> %v, want exactly one new", before, after)
		}
		if res.ManifestHash != p.ManifestHash || res.HooksHash != p.HooksHash {
			t.Fatalf("applied hashes %s/%s != planned %s/%s", res.ManifestHash, res.HooksHash, p.ManifestHash, p.HooksHash)
		}
		// Independently read the stored release record back.
		got, err := action.NewGet(plainHelm(t, f.ns)).Run(relName)
		if err != nil {
			t.Fatal(err)
		}
		if ManifestHash(got.Manifest) != p.ManifestHash || HooksHash(got.Hooks) != p.HooksHash {
			t.Fatalf("stored release record does not match the plan")
		}
		if got.Version != p.Baseline.Revision+1 || got.Info.Status.String() != "deployed" {
			t.Fatalf("stored release rev %d %s", got.Version, got.Info.Status)
		}
		cm, err := testCS.CoreV1().ConfigMaps(f.ns).Get(context.Background(), relName+"-config", metav1.GetOptions{})
		if err != nil || cm.Data["message"] != "planned" {
			t.Fatalf("configmap not applied: %v %v", cm.Data, err)
		}
		t.Logf("RESIDUAL WINDOW (%s): boundary validation took %s (%d live GETs + release-history read); "+
			"validation end -> first release-record write: %s, 0 API calls in between",
			mode, res.ValidateDuration, res.LiveChecks, res.BoundaryToWrite)
	})
}

// -------------------------------------------------- ordinary manifest change

func TestAccept_ManifestChange_ChartDigest(t *testing.T) {
	forModes(t, func(t *testing.T, mode string) {
		f := setup(t, mode, "app")
		p := f.plan(false)
		editFile(t, filepath.Join(f.chart, "templates/configmap.yaml"), "\ndata:\n", "\ndata:\n  extra: \"sneaked-in\"\n")
		f.refuse(p, ApplyOptions{}, nil, RefuseChartDigest, StagePrecheck)
	})
}

// Digest check bypassed: the post-renderer, seeing the real execution's
// manifests, must catch it.
func TestAccept_ManifestChange_RenderCheck(t *testing.T) {
	forModes(t, func(t *testing.T, mode string) {
		f := setup(t, mode, "app")
		p := f.plan(false)
		editFile(t, filepath.Join(f.chart, "templates/configmap.yaml"), "\ndata:\n", "\ndata:\n  extra: \"sneaked-in\"\n")
		f.refuse(p, ApplyOptions{skipDigestCheck: true}, nil, RefuseManifests, StagePostRender)
	})
}

// --------------------------------------------------------- hook-only change

func TestAccept_HookOnlyChange(t *testing.T) {
	forModes(t, func(t *testing.T, mode string) {
		t.Run("chart-digest", func(t *testing.T) {
			f := setup(t, mode, "app")
			p := f.plan(false)
			editFile(t, filepath.Join(f.chart, "templates/hook.yaml"), "\ndata:\n", "\ndata:\n  extra: \"hook-only\"\n")
			f.refuse(p, ApplyOptions{}, nil, RefuseChartDigest, StagePrecheck)
		})
		t.Run("execution-boundary", func(t *testing.T) {
			f := setup(t, mode, "app")
			p := f.plan(false)
			editFile(t, filepath.Join(f.chart, "templates/hook.yaml"), "\ndata:\n", "\ndata:\n  extra: \"hook-only\"\n")
			// Manifests are unchanged, so the post-renderer passes; only the
			// gate at the first release-record write sees the hook.
			f.refuse(p, ApplyOptions{skipDigestCheck: true}, nil, RefuseHooks, StageBoundary)
		})
	})
}

// Documents the gap the doc predicts: a post-renderer alone (no storage gate)
// does NOT see hooks. This test passes when the hook change gets APPLIED.
func TestGap_PostRendererOnlyMissesHookChange(t *testing.T) {
	forModes(t, func(t *testing.T, mode string) {
		f := setup(t, mode, "app")
		p := f.plan(false)
		editFile(t, filepath.Join(f.chart, "templates/hook.yaml"), "\ndata:\n", "\ndata:\n  extra: \"hook-only\"\n")
		res, err := Apply(context.Background(), p, ApplyOptions{Target: testTarget, skipDigestCheck: true, gateOff: true})
		if err != nil {
			t.Fatalf("expected the post-renderer-only configuration to MISS the hook change, got: %v", err)
		}
		if res.HooksHash == p.HooksHash {
			t.Fatal("hooks unexpectedly equal")
		}
		cm, err := testCS.CoreV1().ConfigMaps(f.ns).Get(context.Background(), relName+"-pre", metav1.GetOptions{})
		if err != nil || cm.Data["extra"] != "hook-only" {
			t.Fatalf("hook not executed? %v %v", cm, err)
		}
		t.Logf("GAP CONFIRMED (%s): post-renderer-only apply executed an unplanned hook", mode)
	})
}

// ---------------------------------------------------------- lookup input

func TestAccept_LookupInputChange(t *testing.T) {
	forModes(t, func(t *testing.T, mode string) {
		t.Run("lint-rejects-at-plan", func(t *testing.T) {
			f := setup(t, mode, "lookup")
			before := snapshot(t, f.ns)
			_, err := MakePlan(context.Background(), Options{Target: testTarget, ReleaseName: relName, Namespace: f.ns, ChartPath: f.chart, OverridesFile: f.overrides, Strict: true})
			var le *LintError
			if !errors.As(err, &le) {
				t.Fatalf("want LintError, got %v", err)
			}
			if !strings.Contains(err.Error(), `"lookup"`) {
				t.Fatalf("lint did not name lookup: %v", err)
			}
			assertUnchanged(t, before, snapshot(t, f.ns))
		})
		t.Run("no-lint/manifest-lookup", func(t *testing.T) {
			f := setup(t, mode, "lookup")
			p := f.plan(true)
			f.setInput("lookup-input", "b")
			f.refuse(p, ApplyOptions{}, nil, RefuseManifests, StagePostRender)
		})
		t.Run("no-lint/hook-lookup", func(t *testing.T) {
			f := setup(t, mode, "lookup")
			p := f.plan(true)
			f.setInput("lookup-hook-input", "b")
			f.refuse(p, ApplyOptions{}, nil, RefuseHooks, StageBoundary)
		})
		t.Run("no-lint/unchanged-input-applies", func(t *testing.T) {
			f := setup(t, mode, "lookup")
			p := f.plan(true)
			if _, err := Apply(context.Background(), p, ApplyOptions{Target: testTarget}); err != nil {
				t.Fatalf("apply: %v", err)
			}
		})
	})
}

// A chart with randAlphaNum never applies without the lint: every render
// differs. This is why option (a) is required alongside (b).
func TestImpureChartWithoutLintNeverApplies(t *testing.T) {
	forModes(t, func(t *testing.T, mode string) {
		f := setup(t, mode, "random")
		_, err := MakePlan(context.Background(), Options{Target: testTarget, ReleaseName: relName, Namespace: f.ns, ChartPath: f.chart, OverridesFile: f.overrides, Strict: true})
		var le *LintError
		if !errors.As(err, &le) {
			t.Fatalf("want LintError, got %v", err)
		}
		p := f.plan(true)
		f.refuse(p, ApplyOptions{}, nil, RefuseManifests, StagePostRender)
	})
}

// -------------------------------------------------------- live baseline

func TestAccept_LiveBaselineChange(t *testing.T) {
	forModes(t, func(t *testing.T, mode string) {
		mutate := func(f *fixture) func() {
			return func() {
				ctx := context.Background()
				if mode == "install" {
					// Someone creates an object the plan expects to create, WITH
					// Helm ownership metadata for this release - Helm's own
					// existingResourceConflict (install.go:353) accepts that and
					// would silently adopt/overwrite it, so only the live
					// baseline check can catch it.
					_, err := testCS.CoreV1().ConfigMaps(f.ns).Create(ctx, &corev1.ConfigMap{
						ObjectMeta: metav1.ObjectMeta{Name: relName + "-config",
							Labels:      map[string]string{"app.kubernetes.io/managed-by": "Helm"},
							Annotations: map[string]string{"meta.helm.sh/release-name": relName, "meta.helm.sh/release-namespace": f.ns}},
						Data: map[string]string{"message": "manual"}}, metav1.CreateOptions{})
					if err != nil {
						t.Fatal(err)
					}
					return
				}
				// kubectl patch deployment ks-api -p '{"spec":{"replicas":3}}'
				_, err := testCS.AppsV1().Deployments(f.ns).Patch(ctx, relName+"-api", types.MergePatchType,
					[]byte(`{"spec":{"replicas":3}}`), metav1.PatchOptions{})
				if err != nil {
					t.Fatal(err)
				}
			}
		}
		t.Run("before-apply", func(t *testing.T) {
			f := setup(t, mode, "app")
			p := f.plan(false)
			mutate(f)()
			f.refuse(p, ApplyOptions{}, nil, RefuseLiveBaseline, StagePrecheck)
		})
		t.Run("after-precheck", func(t *testing.T) {
			f := setup(t, mode, "app")
			p := f.plan(false)
			f.refuse(p, ApplyOptions{}, mutate(f), RefuseLiveBaseline, StageBoundary)
		})
	})
}

// ----------------------------------------------------- release baseline

func TestAccept_ReleaseBaselineChange(t *testing.T) {
	forModes(t, func(t *testing.T, mode string) {
		other := func(f *fixture) func() {
			return func() {
				vals := map[string]interface{}{"message": "someone-else"}
				if mode == "install" {
					plainInstall(t, f.ns, relName, f.chart, vals)
				} else {
					plainUpgrade(t, f.ns, relName, f.chart, vals)
				}
			}
		}
		t.Run("before-apply", func(t *testing.T) {
			f := setup(t, mode, "app")
			p := f.plan(false)
			other(f)()
			f.refuse(p, ApplyOptions{}, nil, RefuseReleaseBaseline, StagePrecheck)
		})
		t.Run("after-precheck", func(t *testing.T) {
			f := setup(t, mode, "app")
			p := f.plan(false)
			if mode == "install" {
				// Helm's own availableName check (install.go:245) fires first,
				// before rendering; it is a read, so still no write.
				o := ApplyOptions{Target: testTarget}
				var before map[string]string
				o.beforeExecute = func() { other(f)(); before = snapshot(t, f.ns) }
				_, err := Apply(context.Background(), p, o)
				if err == nil || !strings.Contains(err.Error(), "cannot re-use a name") {
					t.Fatalf("want Helm name-in-use refusal, got %v", err)
				}
				assertUnchanged(t, before, snapshot(t, f.ns))
				return
			}
			f.refuse(p, ApplyOptions{}, other(f), RefuseRevision, StageBoundary)
		})
		if mode == "upgrade" {
			t.Run("deployment-info-edited", func(t *testing.T) {
				f := setup(t, mode, "app")
				p := f.plan(false)
				_, err := testCS.CoreV1().Secrets(f.ns).Patch(context.Background(), DeploymentInfoSecret, types.MergePatchType,
					[]byte(`{"stringData":{"chartVersion":"9.9.9"}}`), metav1.PatchOptions{})
				if err != nil {
					t.Fatal(err)
				}
				f.refuse(p, ApplyOptions{}, nil, RefuseDeploymentInfo, StagePrecheck)
			})
		}
	})
}

// ------------------------------------------------------------------ CRDs

func addTopLevelCRD(t *testing.T, chart string) {
	b, err := os.ReadFile("testdata/crd-files/widgets.example.com.yaml")
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(chart, "crds/widgets.example.com.yaml"), string(b))
}

func addCRDDependency(t *testing.T, chart string) {
	src := copyChart(t, "subchart-with-crds")
	err := filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		b, _ := os.ReadFile(p)
		writeFile(t, filepath.Join(chart, "charts/widgets", rel), string(b))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestAccept_ChartWithCRDs(t *testing.T) {
	forModes(t, func(t *testing.T, mode string) {
		for _, v := range []struct {
			name string
			add  func(*testing.T, string)
		}{{"top-level", addTopLevelCRD}, {"dependency", addCRDDependency}} {
			v := v
			t.Run(v.name+"/plan-rejects", func(t *testing.T) {
				f := setup(t, mode, "app")
				v.add(t, f.chart)
				before := snapshot(t, f.ns)
				_, err := MakePlan(context.Background(), Options{Target: testTarget, ReleaseName: relName, Namespace: f.ns, ChartPath: f.chart, OverridesFile: f.overrides, Strict: true})
				var ce *CRDError
				if !errors.As(err, &ce) {
					t.Fatalf("want CRDError, got %v", err)
				}
				assertUnchanged(t, before, snapshot(t, f.ns))
			})
			t.Run(v.name+"/apply-rejects", func(t *testing.T) {
				f := setup(t, mode, "app")
				p := f.plan(false)
				v.add(t, f.chart)
				// Digest check bypassed to show the CRD check stands on its own.
				f.refuse(p, ApplyOptions{skipDigestCheck: true}, nil, RefuseCRDs, StagePrecheck)
			})
		}
		if mode == "install" {
			// Defence in depth: with the CRD check also bypassed, Helm's
			// installCRDs (install.go:264) runs before rendering - and the write
			// barrier stops it.
			t.Run("barrier-blocks-installCRDs", func(t *testing.T) {
				f := setup(t, mode, "app")
				p := f.plan(false)
				addTopLevelCRD(t, f.chart)
				r := f.refuse(p, ApplyOptions{skipDigestCheck: true, skipCRDCheck: true}, nil, RefuseWriteBarrier, StageBarrier)
				if !strings.Contains(r.Error(), "CustomResourceDefinition") {
					t.Fatalf("barrier did not report the CRD create: %v", r)
				}
			})
		}
	})
}

// ------------------------------------------------- plan-file preconditions

func TestApplyRefusesBadPlanFiles(t *testing.T) {
	f := setup(t, "upgrade", "app")
	base := f.plan(false)
	clone := func() *Plan { c := *base; return &c }
	cases := []struct {
		name string
		p    func() *Plan
		o    ApplyOptions
		code string
	}{
		{"expired", clone, ApplyOptions{Now: func() time.Time { return base.ExpiresAt.Add(time.Second) }}, RefuseExpired},
		{"cli-version", func() *Plan { c := clone(); c.CLIVersion = "0.0.1-other"; return c }, ApplyOptions{}, RefuseCLIVersion},
		{"helm-version", func() *Plan { c := clone(); c.HelmVersion = "v3.99.0"; return c }, ApplyOptions{}, RefuseHelmVersion},
		{"speculative", func() *Plan { c := clone(); c.Speculative = true; return c }, ApplyOptions{}, RefuseSpeculative},
		{"other-cluster", func() *Plan { c := clone(); c.ServerURL = "https://127.0.0.1:1"; return c }, ApplyOptions{}, RefuseTarget},
		{"capabilities", func() *Plan { c := clone(); c.Capabilities.Hash = "sha256:00"; return c }, ApplyOptions{}, RefuseCapabilities},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			f.t = t
			f.refuse(tc.p(), tc.o, nil, tc.code, StagePrecheck)
		})
	}
}

// ------------------------------------------------------------ other states

func TestPlanIsReadOnly(t *testing.T) {
	forModes(t, func(t *testing.T, mode string) {
		f := setup(t, mode, "app")
		before := snapshot(t, f.ns)
		f.plan(false)
		assertUnchanged(t, before, snapshot(t, f.ns))
	})
}

func TestAdoptedBaseline(t *testing.T) {
	ns := newNS(t)
	chart := copyChart(t, "app")
	plainInstall(t, ns, relName, chart, map[string]interface{}{"deploymentInfo": map[string]interface{}{"enabled": false}})
	f := &fixture{t: t, mode: "upgrade", ns: ns, chart: chart, overrides: overridesFile(t, "message: adopted\n")}
	p := f.plan(false)
	if p.Baseline.State != StateAdopted {
		t.Fatalf("state %s, want adopted", p.Baseline.State)
	}
	found := false
	for _, c := range p.Changes {
		if c.Op == "+" && c.Ref.Name == DeploymentInfoSecret {
			found = true
		}
	}
	if !found {
		t.Fatalf("plan does not show deployment-info being created: %+v", p.Changes)
	}
	var buf bytes.Buffer
	p.Show(&buf)
	t.Logf("show:\n%s", buf.String())
	if _, err := Apply(context.Background(), p, ApplyOptions{Target: testTarget}); err != nil {
		t.Fatal(err)
	}
	if p2 := f.plan(false); p2.Baseline.State != StateManaged {
		t.Fatalf("after apply state %s, want managed", p2.Baseline.State)
	}
}

func TestGitOpsBaselineRefused(t *testing.T) {
	f := setup(t, "upgrade", "app")
	_, err := testCS.CoreV1().Secrets(f.ns).Patch(context.Background(), DeploymentInfoSecret, types.MergePatchType,
		[]byte(`{"metadata":{"labels":{"argocd.argoproj.io/instance":"kubesense"}}}`), metav1.PatchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := MakePlan(context.Background(), Options{Target: testTarget, ReleaseName: relName, Namespace: f.ns, ChartPath: f.chart, OverridesFile: f.overrides, Strict: true}); err == nil || !strings.Contains(err.Error(), "GitOps") {
		t.Fatalf("want GitOps refusal, got %v", err)
	}
}

func TestSafety_RefusesNonLoopbackServer(t *testing.T) {
	kc := filepath.Join(t.TempDir(), "kubeconfig")
	writeFile(t, kc, `apiVersion: v1
kind: Config
clusters:
- name: remote
  cluster: {server: "https://10.255.255.1:6443"}
users:
- name: u
  user: {token: x}
contexts:
- name: remote
  context: {cluster: remote, user: u}
current-context: remote
`)
	_, err := Target{Kubeconfig: kc, RequireLoopback: true}.connect("default", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "not loopback") {
		t.Fatalf("want loopback refusal, got %v", err)
	}
}
