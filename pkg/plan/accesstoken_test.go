package plan

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func token(id, secret string) string { return base64.StdEncoding.EncodeToString([]byte(id + ":" + secret)) }

func tokenPlan(t *testing.T, f *fixture, tok string) *Plan {
	t.Helper()
	chart, _ := filepath.Abs("testdata/access-token")
	p, err := MakePlan(context.Background(), Options{
		Target: testTarget, ReleaseName: relName, Namespace: f.ns, ChartPath: f.chart,
		OverridesFile: overridesFile(t, "message: same\n"), AccessToken: tok, AccessTokenChart: chart,
	})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	return p
}

func shown(p *Plan) string {
	var b bytes.Buffer
	p.Show(&b)
	return b.String()
}

func TestAccessTokenIsOptional(t *testing.T) {
	f := &fixture{t: t, mode: "upgrade", ns: newNS(t), chart: copyChart(t, "app")}
	plainInstall(t, f.ns, relName, f.chart, map[string]interface{}{"message": "same"})
	p := tokenPlan(t, f, "")
	if p.AccessToken != nil || p.HasChanges() {
		t.Fatalf("no token, no change: want nothing to do, got step %+v changes %v", p.AccessToken, p.HasChanges())
	}
	if out := shown(p); !strings.Contains(out, "no access token installed: fine when images come from your own registry") {
		t.Fatalf("plan output:\n%s", out)
	}
}

func TestAccessTokenInstallRotateAndStale(t *testing.T) {
	ctx := context.Background()
	f := &fixture{t: t, mode: "upgrade", ns: newNS(t), chart: copyChart(t, "app")}
	plainInstall(t, f.ns, relName, f.chart, map[string]interface{}{"message": "same"})

	// Install, with the KubeSense release otherwise unchanged.
	p := tokenPlan(t, f, token("AKIAFIRST01", "s3cret-one"))
	if p.AccessToken == nil || p.AccessToken.Action != "install" || p.HasReleaseChanges() {
		t.Fatalf("want an access-token install only, got %+v", p.AccessToken)
	}
	out := shown(p)
	if !strings.Contains(out, "+ install access token, key AKIA…01") || strings.Contains(out, "s3cret") {
		t.Fatalf("plan output:\n%s", out)
	}
	res, err := Apply(ctx, p, ApplyOptions{Target: testTarget})
	if err != nil || res.Action != "none" || !strings.Contains(res.AccessToken, "installed") {
		t.Fatalf("apply: %+v %v", res, err)
	}
	s, err := testCS.CoreV1().Secrets(f.ns).Get(ctx, "kubesense-access-token", metav1.GetOptions{})
	if err != nil || string(s.Data["KEY_SECRET"]) != "s3cret-one" {
		t.Fatalf("access-token secret: %v", err)
	}

	// The same token again: nothing to do.
	if p := tokenPlan(t, f, token("AKIAFIRST01", "s3cret-one")); p.AccessToken.Action != "unchanged" || p.HasChanges() {
		t.Fatalf("same token: want unchanged, got %+v", p.AccessToken)
	}

	// Two plans to rotate; the first applied makes the second stale.
	a := tokenPlan(t, f, token("AKIASECOND2", "s3cret-two"))
	b := tokenPlan(t, f, token("AKIATHIRD03", "s3cret-three"))
	if out := shown(a); !strings.Contains(out, "rotate access token, key AKIA…01 -> AKIA…D2") {
		t.Fatalf("rotate plan output:\n%s", out)
	}
	if _, err := Apply(ctx, a, ApplyOptions{Target: testTarget}); err != nil {
		t.Fatal(err)
	}
	_, err = Apply(ctx, b, ApplyOptions{Target: testTarget})
	var r *Refusal
	if !errors.As(err, &r) || r.Code != RefuseAccessToken {
		t.Fatalf("stale rotation: want %s refusal, got %v", RefuseAccessToken, err)
	}
	s, _ = testCS.CoreV1().Secrets(f.ns).Get(ctx, "kubesense-access-token", metav1.GetOptions{})
	if string(s.Data["KEY_ID"]) != "AKIASECOND2" {
		t.Fatalf("a refused rotation changed the token to %s", s.Data["KEY_ID"])
	}
}

func TestParseAccessToken(t *testing.T) {
	if _, _, err := ParseAccessToken("not base64!"); err == nil {
		t.Error("accepted a non-base64 token")
	}
	if _, _, err := ParseAccessToken(base64.StdEncoding.EncodeToString([]byte("no-colon"))); err == nil {
		t.Error("accepted a token without KEY_ID:KEY_SECRET")
	}
	if id, sec, err := ParseAccessToken(token("A", "b:c") + "\n"); err != nil || id != "A" || sec != "b:c" {
		t.Errorf("ParseAccessToken = %q %q %v", id, sec, err)
	}
}
