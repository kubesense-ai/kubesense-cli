package plan

import (
	"context"
	"errors"
	"fmt"
	"time"

	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/release"
)

// Refusal codes.
const (
	RefuseSpeculative     = "speculative"
	RefuseExpired         = "expired"
	RefuseCLIVersion      = "cli-version"
	RefuseHelmVersion     = "helm-version"
	RefuseTarget          = "target"
	RefuseChartDigest     = "chart-digest"
	RefuseCRDs            = "crds"
	RefuseImpure          = "impure-templates"
	RefuseCapabilities    = "capabilities"
	RefuseReleaseBaseline = "release-baseline"
	RefuseDeploymentInfo  = "deployment-info"
	RefuseLiveBaseline    = "live-baseline"
	RefuseRevision        = "revision"
	RefuseManifests       = "rendered-manifests"
	RefuseHooks           = "rendered-hooks"
	RefuseWriteBarrier    = "write-before-validation"
)

// Stages at which apply can refuse.
const (
	StagePrecheck   = "precheck"           // before Helm is invoked
	StagePostRender = "post-render"        // inside Helm's render, non-hook manifests only
	StageBoundary   = "execution-boundary" // at Helm's first release-record write
	StageBarrier    = "write-barrier"      // a write was attempted before the boundary
)

// Refusal is returned when apply refuses; nothing was written.
type Refusal struct {
	Stage string
	Code  string
	Err   error
}

func (r *Refusal) Error() string {
	return fmt.Sprintf("apply refused at %s [%s]: %v", r.Stage, r.Code, r.Err)
}
func (r *Refusal) Unwrap() error { return r.Err }

func refuse(stage, code string, format string, args ...interface{}) *Refusal {
	return &Refusal{Stage: stage, Code: code, Err: fmt.Errorf(format, args...)}
}

// ApplyOptions configures Apply.
type ApplyOptions struct {
	Target Target
	Logf   func(string, ...interface{})
	Now    func() time.Time

	// --- spike/test-only knobs (unexported: not reachable from the CLI) ---
	skipDigestCheck bool   // prove the render-hash checks catch what the digest check would
	skipCRDCheck    bool   // prove the write barrier stops Helm's installCRDs on its own
	gateOff         bool   // post-renderer only: demonstrate the hook gap of option (b1)
	beforeExecute   func() // runs after every precheck passed, right before Helm is invoked
}

// ApplyResult reports what was executed.
type ApplyResult struct {
	Action       string
	Revision     int
	Status       string
	ManifestHash string
	HooksHash    string
	// Residual window (see report): the live recheck is a sequence of GETs; the
	// first release-record write follows the end of validation with no API call
	// in between.
	ValidateDuration time.Duration
	BoundaryToWrite  time.Duration
	LiveChecks       int
}

// Apply executes the plan only if the invariant holds; otherwise it returns a
// *Refusal and nothing (no release record, no resource) has been written.
func Apply(ctx context.Context, p *Plan, o ApplyOptions) (*ApplyResult, error) {
	if !p.Strict {
		return nil, refuse("precheck", "not-strict", "the plan was made for review, not with --strict; it cannot be applied")
	}
	now := time.Now
	if o.Now != nil {
		now = o.Now
	}
	if p.Speculative {
		return nil, refuse(StagePrecheck, RefuseSpeculative, "speculative plan (no --out) cannot be applied")
	}
	if now().After(p.ExpiresAt) {
		return nil, refuse(StagePrecheck, RefuseExpired, "plan expired at %s", p.ExpiresAt.Format(time.RFC3339))
	}
	if p.CLIVersion != Version {
		return nil, refuse(StagePrecheck, RefuseCLIVersion, "plan made by kubesense %s, this is %s", p.CLIVersion, Version)
	}
	if p.HelmVersion != HelmVersion() {
		return nil, refuse(StagePrecheck, RefuseHelmVersion, "plan rendered with Helm %s, embedded Helm is %s", p.HelmVersion, HelmVersion())
	}

	// 1. Artifact.
	chrt, digest, err := loadChart(p.Chart.Path)
	if err != nil {
		return nil, refuse(StagePrecheck, RefuseChartDigest, "load chart: %v", err)
	}
	if err := CheckNoCRDs(chrt); err != nil && !o.skipCRDCheck {
		return nil, &Refusal{Stage: StagePrecheck, Code: RefuseCRDs, Err: err}
	}
	if digest != p.Chart.Digest && !o.skipDigestCheck {
		return nil, refuse(StagePrecheck, RefuseChartDigest, "chart digest %s != planned %s", shortHash(digest), shortHash(p.Chart.Digest))
	}
	overrides, err := roundTrip(p.Overrides)
	if err != nil {
		return nil, err
	}
	if p.PureRender == "enforced" {
		if err := LintPureRender(chrt, overrides); err != nil {
			return nil, &Refusal{Stage: StagePrecheck, Code: RefuseImpure, Err: err}
		}
	}

	// The gate validates the real execution's release object; built below.
	var c *conn
	gate := newGate(func(rel *release.Release) error {
		if o.gateOff {
			return nil
		}
		return c.validateCandidate(ctx, p, rel)
	})
	c, err = o.Target.connect(p.Namespace, gate, o.Logf)
	if err != nil {
		return nil, err
	}
	if c.serverURL != p.ServerURL {
		return nil, refuse(StagePrecheck, RefuseTarget, "plan was made against %s, kubeconfig points at %s", p.ServerURL, c.serverURL)
	}

	// 2. Render inputs: fetch Capabilities, compare, then PIN them so the real
	// run renders with exactly the verified value (Helm uses a non-nil
	// cfg.Capabilities as-is: pkg/action/action.go:247).
	caps, err := c.capabilities()
	if err != nil {
		return nil, err
	}
	if h := CapabilitiesHash(caps); h != p.Capabilities.Hash {
		return nil, refuse(StagePrecheck, RefuseCapabilities, "cluster Capabilities changed (kube %s, %d APIs; planned kube %s, %d APIs)",
			caps.KubeVersion.Version, len(caps.APIVersions), p.Capabilities.KubeVersion, p.Capabilities.APICount)
	}
	c.helm.Capabilities = caps

	// 3 + 4. Baselines (early, cheap refusal; repeated at the boundary).
	if err := c.checkBaselines(ctx, p, StagePrecheck); err != nil {
		return nil, err
	}

	if o.beforeExecute != nil {
		o.beforeExecute()
	}

	// 5. Execute. The post-renderer checks the non-hook manifests inside the
	// real render; the gate checks manifests + hooks + baselines at the first
	// write; the barrier refuses any write before that.
	pr := &checkingPostRenderer{check: func(m string) error {
		if h := ManifestHash(m); h != p.ManifestHash {
			return refuse(StagePostRender, RefuseManifests, "rendered manifests %s != planned %s", shortHash(h), shortHash(p.ManifestHash))
		}
		return nil
	}}
	var rel *release.Release
	switch p.Action {
	case "install":
		in := action.NewInstall(c.helm)
		in.ReleaseName, in.Namespace = p.ReleaseName, p.Namespace
		in.PostRenderer = pr
		in.CreateNamespace = false // namespace is a precondition, not part of the plan
		rel, err = in.RunWithContext(ctx, chrt, overrides)
	case "upgrade":
		up := action.NewUpgrade(c.helm)
		up.Namespace = p.Namespace
		up.PostRenderer = pr
		up.ResetValues = true
		up.MaxHistory = 0 // pruning would delete records before the gated Create (storage.go:62)
		rel, err = up.RunWithContext(ctx, p.ReleaseName, chrt, overrides)
	default:
		return nil, fmt.Errorf("unknown plan action %q", p.Action)
	}
	res := &ApplyResult{Action: p.Action}
	if gate.opened || gate.err != nil {
		res.LiveChecks = len(p.LiveObjects)
	}
	if !gate.ValidateEnd.IsZero() {
		res.ValidateDuration = gate.ValidateEnd.Sub(gate.ValidateStart)
	}
	if !gate.FirstWriteStart.IsZero() {
		res.BoundaryToWrite = gate.FirstWriteStart.Sub(gate.ValidateEnd)
	}
	if err != nil {
		var r *Refusal
		if errors.As(err, &r) {
			return res, r
		}
		var g *GateError
		if errors.As(err, &g) {
			return res, &Refusal{Stage: StageBarrier, Code: RefuseWriteBarrier, Err: err}
		}
		return res, err
	}
	res.Revision, res.Status = rel.Version, rel.Info.Status.String()
	res.ManifestHash, res.HooksHash = ManifestHash(rel.Manifest), HooksHash(rel.Hooks)
	return res, nil
}

func (c *conn) checkBaselines(ctx context.Context, p *Plan, stage string) error {
	bl, _, err := c.readBaseline(ctx, p.ReleaseName, p.Namespace)
	if err != nil {
		return &Refusal{Stage: stage, Code: RefuseReleaseBaseline, Err: err}
	}
	if bl.State != p.Baseline.State || bl.Revision != p.Baseline.Revision || bl.Status != p.Baseline.Status || bl.RecordsDigest != p.Baseline.RecordsDigest {
		return refuse(stage, RefuseReleaseBaseline, "release baseline changed: planned %s rev %d (%s), now %s rev %d (%s)",
			p.Baseline.State, p.Baseline.Revision, p.Baseline.Status, bl.State, bl.Revision, bl.Status)
	}
	if bl.DeploymentInfoHash != p.Baseline.DeploymentInfoHash {
		return refuse(stage, RefuseDeploymentInfo, "%s changed since plan", DeploymentInfoSecret)
	}
	if _, err := c.recheckLive(ctx, p.LiveObjects); err != nil {
		var lb *LiveBaselineError
		if errors.As(err, &lb) {
			return &Refusal{Stage: stage, Code: RefuseLiveBaseline, Err: err}
		}
		return err
	}
	return nil
}

// validateCandidate runs inside the gate, i.e. after Helm has rendered the real
// execution (manifests post-rendered, hooks sorted) and immediately before its
// first write. rel is the exact object Helm is about to persist and execute.
func (c *conn) validateCandidate(ctx context.Context, p *Plan, rel *release.Release) error {
	want := p.Baseline.Revision + 1
	if rel.Version != want {
		return refuse(StageBoundary, RefuseRevision, "candidate revision %d, planned %d", rel.Version, want)
	}
	if h := ManifestHash(rel.Manifest); h != p.ManifestHash {
		return refuse(StageBoundary, RefuseManifests, "candidate manifests %s != planned %s", shortHash(h), shortHash(p.ManifestHash))
	}
	if h := HooksHash(rel.Hooks); h != p.HooksHash {
		return refuse(StageBoundary, RefuseHooks, "candidate hooks %s != planned %s", shortHash(h), shortHash(p.HooksHash))
	}
	return c.checkBaselines(ctx, p, StageBoundary)
}
