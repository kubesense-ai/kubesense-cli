package plan

import (
	"bytes"
	"context"
	"reflect"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

func TestSuppliedValues(t *testing.T) {
	own := map[string]interface{}{
		"api":    map[string]interface{}{"image": map[string]interface{}{"tag": "2.48.0"}},
		"global": map[string]interface{}{"kubesenseManager": map[string]interface{}{"enabled": true}},
	}
	got, err := withSuppliedValues(own)
	if err != nil {
		t.Fatal(err)
	}
	s, _ := got["global"].(map[string]interface{})["kubesenseManager"].(map[string]interface{})["suppliedValues"].(string)
	var back map[string]interface{}
	if err := yaml.Unmarshal([]byte(s), &back); err != nil || !reflect.DeepEqual(back, mustRoundTrip(t, own)) {
		t.Fatalf("suppliedValues = %q (%v), want the YAML of the overrides", s, err)
	}
	// Attaching again (values pulled from a release that has a copy) gives
	// the same result, not a copy nested inside a copy.
	again, err := withSuppliedValues(got)
	if err != nil || !reflect.DeepEqual(again, got) {
		t.Fatalf("re-attaching changed the values:\n%v\n%v", again, got)
	}
	if !reflect.DeepEqual(StripSuppliedValues(got), mustRoundTrip(t, own)) {
		t.Fatalf("strip did not restore the overrides: %v", StripSuppliedValues(got))
	}
	// Nothing supplied: nothing attached.
	if got, _ := withSuppliedValues(map[string]interface{}{}); len(got) != 0 {
		t.Fatalf("empty overrides gained %v", got)
	}
}

func mustRoundTrip(t *testing.T, v map[string]interface{}) map[string]interface{} {
	t.Helper()
	out, err := roundTrip(v)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// A release installed by plain helm has no copy: the first plan attaches one,
// which is a change to the release even though no setting changed.
func TestFirstPlanAfterRawHelmAttachesCopy(t *testing.T) {
	f := &fixture{t: t, mode: "upgrade", ns: newNS(t), chart: copyChart(t, "app")}
	rawHelmInstall(t, f.ns, relName, f.chart, map[string]interface{}{"message": "same"})
	p := reviewPlan(t, f, "message: same\n")
	if len(p.ValueChanges) != 0 {
		t.Fatalf("no setting changed, but the plan lists %v", p.ValueChanges)
	}
	if _, err := Apply(context.Background(), p, ApplyOptions{Target: testTarget}); err != nil {
		t.Fatal(err)
	}
	if again := reviewPlan(t, f, "message: same\n"); again.HasChanges() {
		t.Fatalf("after one apply the same values must plan no changes:\n%s", shown(again))
	}
}

func TestShowOverridesGroupsAndKeepsLongValues(t *testing.T) {
	long := "http://kubesense-datastore-shard0-0.kubesense-datastore-headless.prod.svc.cluster.local:8123"
	p := &Plan{
		Baseline: Baseline{CurrentOverrides: map[string]interface{}{
			"api":        map[string]interface{}{"image": map[string]interface{}{"tag": "1.0"}},
			"aggregator": map[string]interface{}{"endpoint": "http://old", "route": map[string]interface{}{"a": "b"}},
		}},
		Overrides: map[string]interface{}{
			"api":        map[string]interface{}{"image": map[string]interface{}{"tag": "1.1"}},
			"aggregator": map[string]interface{}{"endpoint": long, "routes": []interface{}{map[string]interface{}{"name": "x"}}},
		},
	}
	var b bytes.Buffer
	p.showOverrides(&b)
	out := b.String()
	for _, want := range []string{
		"  aggregator\n",
		"    ~ endpoint\n        - http://old\n        + " + long + "\n", // whole, never cut
		"    - route\n        - a: b\n",                                  // removed map as YAML
		"    + routes\n        + - name: x\n",                            // added list as YAML
		"  api\n    ~ image.tag  1.0 → 1.1\n",                            // short change inline
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("output lacks %q:\n%s", want, out)
		}
	}
}
