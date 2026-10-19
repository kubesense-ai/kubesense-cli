package plan

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// Review mode is what `kubesense plan` runs: it must work on real charts
// (impure templates, GitOps installs), never print credentials, and only list
// what the action would actually do.

func reviewPlan(t *testing.T, f *fixture, overrides string) *Plan {
	t.Helper()
	p, err := MakePlan(context.Background(), Options{
		Target: testTarget, ReleaseName: relName, Namespace: f.ns, ChartPath: f.chart,
		OverridesFile: overridesFile(t, overrides),
	})
	if err != nil {
		t.Fatalf("review plan: %v", err)
	}
	return p
}

func TestReviewPlanMasksCredentialsAndShowsTheRest(t *testing.T) {
	f := &fixture{t: t, mode: "upgrade", ns: newNS(t), chart: copyChart(t, "review")}
	plainInstall(t, f.ns, relName, f.chart, map[string]interface{}{
		"message": "a", "dbPassword": "old-pass-111", "apiToken": "old-token-222",
	})
	p := reviewPlan(t, f, "message: b\ndbPassword: new-pass-333\napiToken: new-token-444\n")

	var out bytes.Buffer
	p.Show(&out)
	text := out.String()
	for _, secret := range []string{"old-pass-111", "new-pass-333", "old-token-222", "new-token-444"} {
		if strings.Contains(text, secret) {
			t.Fatalf("plan output leaks %q:\n%s", secret, text)
		}
	}
	for _, want := range []string{
		`data.message: "a" -> "b"`,                       // ordinary setting shown
		"data.apiToken: (sensitive value changed)",       // credential-like key masked
		"stringData.password: (sensitive value changed)", // any Secret data masked
		"dbPassword: (sensitive value changed)",          // overrides diff masked
		`message: "a" -> "b"`,
		"cluster ID ",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("plan output lacks %q:\n%s", want, text)
		}
	}
	// The install-only hook renders differently every time; an upgrade never
	// runs it, so it must not appear.
	if strings.Contains(text, "install-only") {
		t.Fatalf("upgrade plan lists a pre-install hook:\n%s", text)
	}
	// The chart calls randAlphaNum: review mode warns instead of refusing.
	if p.PureRender != "warned" || len(p.Warnings) == 0 || !strings.Contains(p.Warnings[0], "not pure-render") {
		t.Fatalf("want a pure-render warning, got %q / %v", p.PureRender, p.Warnings)
	}
	if !p.HasChanges() {
		t.Fatal("HasChanges = false for a plan with changes")
	}
	// A review plan can never be applied.
	_, err := Apply(context.Background(), p, ApplyOptions{Target: testTarget})
	var r *Refusal
	if !errors.As(err, &r) || r.Code != "not-strict" {
		t.Fatalf("apply of a review plan: want not-strict refusal, got %v", err)
	}
}

func TestReviewPlanNoChanges(t *testing.T) {
	f := &fixture{t: t, mode: "upgrade", ns: newNS(t), chart: copyChart(t, "app")}
	plainInstall(t, f.ns, relName, f.chart, map[string]interface{}{"message": "same"})
	p := reviewPlan(t, f, "message: same\n")
	if p.HasChanges() {
		var out bytes.Buffer
		p.Show(&out)
		t.Fatalf("identical values must plan no changes:\n%s", out.String())
	}
}

func TestReviewPlanWarnsOnGitOps(t *testing.T) {
	f := setup(t, "upgrade", "app")
	if _, err := testCS.CoreV1().Secrets(f.ns).Patch(context.Background(), DeploymentInfoSecret, types.MergePatchType,
		[]byte(`{"metadata":{"labels":{"argocd.argoproj.io/instance":"kubesense"}}}`), metav1.PatchOptions{}); err != nil {
		t.Fatal(err)
	}
	p := reviewPlan(t, f, "message: planned\n")
	if p.Baseline.State != StateGitOps || !strings.Contains(strings.Join(p.Warnings, "\n"), "Argo CD or Flux") {
		t.Fatalf("want a GitOps warning, got state %s warnings %v", p.Baseline.State, p.Warnings)
	}
}

func TestReadOverridesMergesLikeHelm(t *testing.T) {
	a := overridesFile(t, "api:\n  replicas: 1\n  image: {tag: a}\nlist: [1, 2]\n")
	b := overridesFile(t, "api:\n  image: {tag: b}\nlist: [3]\nextra: true\n")
	got, err := readOverrides("", a, b)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]interface{}{
		"api":   map[string]interface{}{"replicas": float64(1), "image": map[string]interface{}{"tag": "b"}},
		"list":  []interface{}{float64(3)},
		"extra": true,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("merged overrides = %#v, want %#v", got, want)
	}
}

func TestMaskValuePaths(t *testing.T) {
	got := maskValuePaths([]string{
		`global.mysql.password: "a" -> "b"`,
		`aggregator.sources.http.auth.token: (none) -> "x"`,
		`api.replicaCount: 1 -> 2`,
	})
	want := []string{
		"global.mysql.password: (sensitive value changed)",
		"aggregator.sources.http.auth.token: (sensitive value changed)",
		"api.replicaCount: 1 -> 2",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q", got)
	}
}

func TestDeploymentInfoRevisionIsNotAChange(t *testing.T) {
	ref := ObjectRef{APIVersion: "v1", Kind: "Secret", Namespace: "ns", Name: "kubesense-deployment-info"}
	cur := renderedObject{Ref: ref, Content: map[string]interface{}{"data": map[string]interface{}{"release-revision": "Ng==", "values.yaml": "a"}}}
	same := renderedObject{Ref: ref, Content: map[string]interface{}{"data": map[string]interface{}{"release-revision": "Nw==", "values.yaml": "a"}}}
	if got := diffObjects([]renderedObject{cur}, []renderedObject{same}, false); len(got) != 0 {
		t.Fatalf("a revision bump alone must not be a change: %+v", got)
	}
	changed := renderedObject{Ref: ref, Content: map[string]interface{}{"data": map[string]interface{}{"release-revision": "Nw==", "values.yaml": "b"}}}
	got := diffObjects([]renderedObject{cur}, []renderedObject{changed}, false)
	if len(got) != 1 || len(got[0].Details) != 1 || got[0].Details[0] != "data.values.yaml: "+masked {
		t.Fatalf("a values change must show, masked, without the revision: %+v", got)
	}
}
