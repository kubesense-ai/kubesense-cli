package plan

import (
	"context"
	"errors"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// A plan without --strict is applied on its inputs: the chart this repo ships
// generates passwords and reads the cluster, so its render can never match a
// plan byte for byte, but nothing it was planned from may have changed.

func TestApplyInputsPlanOfImpureChart(t *testing.T) {
	f := &fixture{t: t, mode: "upgrade", ns: newNS(t), chart: copyChart(t, "review")}
	plainInstall(t, f.ns, relName, f.chart, map[string]interface{}{"message": "a"})
	p := reviewPlan(t, f, "message: b\n")
	if p.Strict || p.PureRender != "warned" {
		t.Fatalf("want a non-strict plan of an impure chart, got strict=%v pure=%s", p.Strict, p.PureRender)
	}

	res, err := Apply(context.Background(), p, ApplyOptions{Target: testTarget})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if res.Action != "upgrade" || res.Revision != 2 || res.Status != "deployed" {
		t.Fatalf("result %+v, want upgrade to revision 2, deployed", res)
	}

	// The same plan again: the release moved to revision 2 since it was made.
	before := snapshot(t, f.ns)
	_, err = Apply(context.Background(), p, ApplyOptions{Target: testTarget})
	var r *Refusal
	if !errors.As(err, &r) || r.Code != RefuseReleaseBaseline {
		t.Fatalf("re-applying a used plan: want %s refusal, got %v", RefuseReleaseBaseline, err)
	}
	assertUnchanged(t, before, snapshot(t, f.ns))
}

func TestApplyRefusesGitOpsRelease(t *testing.T) {
	f := setup(t, "upgrade", "app")
	if _, err := testCS.CoreV1().Secrets(f.ns).Patch(context.Background(), DeploymentInfoSecret, types.MergePatchType,
		[]byte(`{"metadata":{"labels":{"argocd.argoproj.io/instance":"kubesense"}}}`), metav1.PatchOptions{}); err != nil {
		t.Fatal(err)
	}
	p := reviewPlan(t, f, "message: planned\n")
	f.refuse(p, ApplyOptions{}, nil, RefuseGitOps, StagePrecheck)
}
