// Package plan implements the spike for a Terraform-style plan / show / apply
// workflow on top of the embedded Helm SDK (helm.sh/helm/v3 v3.16.1).
//
// See SPIKE-PLAN-APPLY.md at the repository root for findings.
package plan

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"sort"
	"time"

	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/chartutil"
	"helm.sh/helm/v3/pkg/release"
	"helm.sh/helm/v3/pkg/storage/driver"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// Version is the CLI version; set with -ldflags "-X kubesense-cli/pkg/plan.Version=...".
var Version = "dev"

// FormatVersion of the plan file.
const FormatVersion = 1

// DeploymentInfoSecret marks a "managed" baseline.
const DeploymentInfoSecret = "kubesense-deployment-info"

// Baseline states (CONTROL-PLANE-DESIGN.md §3.5 "Baseline states").
const (
	StateFresh   = "fresh"
	StateAdopted = "adopted"
	StateManaged = "managed"
	StateGitOps  = "gitops"
)

// HelmVersion returns the embedded Helm module version.
func HelmVersion() string {
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, d := range bi.Deps {
			if d.Path == "helm.sh/helm/v3" {
				if d.Replace != nil {
					return d.Replace.Version
				}
				return d.Version
			}
		}
	}
	return "v3.16.1" // pinned in go.mod; fallback when build info is unavailable
}

// Plan is the serialized, reviewable, applyable plan.
type Plan struct {
	FormatVersion int       `json:"formatVersion"`
	CLIVersion    string    `json:"cliVersion"`
	HelmVersion   string    `json:"helmVersion"`
	CreatedAt     time.Time `json:"createdAt"`
	ExpiresAt     time.Time `json:"expiresAt"`
	Speculative   bool      `json:"speculative"`

	ServerURL   string `json:"serverURL"`
	ReleaseName string `json:"releaseName"`
	Namespace   string `json:"namespace"`
	Action      string `json:"action"` // install | upgrade

	Chart ChartRef `json:"chart"`
	// Overrides are the customer-supplied values; apply uses these, never disk.
	// NOTE: may contain credentials - the plan file is written 0600.
	Overrides     map[string]interface{} `json:"overrides"`
	OverridesHash string                 `json:"overridesHash"`
	PureRender    string                 `json:"pureRender"` // "enforced" | "skipped"

	Capabilities CapabilitiesRecord `json:"capabilities"`
	Baseline     Baseline           `json:"baseline"`
	LiveObjects  []LiveObject       `json:"liveObjects"`

	ManifestHash string       `json:"manifestHash"`
	HooksHash    string       `json:"hooksHash"`
	Manifest     string       `json:"manifest"`
	Hooks        []hookRecord `json:"hooks"`

	Changes      []Change `json:"changes"`
	ValueChanges []string `json:"valueChanges"`
}

// ChartRef pins the chart artifact.
type ChartRef struct {
	Path    string `json:"path"`
	Name    string `json:"name"`
	Version string `json:"version"`
	Digest  string `json:"digest"`
}

// CapabilitiesRecord is what templates see as .Capabilities.
type CapabilitiesRecord struct {
	KubeVersion string `json:"kubeVersion"`
	APICount    int    `json:"apiCount"`
	Hash        string `json:"hash"`
}

// Baseline is the release baseline captured at plan time.
type Baseline struct {
	State              string                 `json:"state"`
	Revision           int                    `json:"revision"` // 0 when fresh
	Status             string                 `json:"status,omitempty"`
	RecordsDigest      string                 `json:"recordsDigest"`
	DeploymentInfoHash string                 `json:"deploymentInfoHash"`
	CurrentOverrides   map[string]interface{} `json:"currentOverrides,omitempty"`
}

// Change is one line of the diff summary.
type Change struct {
	Op      string    `json:"op"` // "+", "~", "-"
	Ref     ObjectRef `json:"ref"`
	Hook    bool      `json:"hook,omitempty"`
	Details []string  `json:"details,omitempty"`
}

// Options for MakePlan.
type Options struct {
	Target        Target
	ReleaseName   string
	Namespace     string
	ChartPath     string
	OverridesFile string
	TTL           time.Duration // default 1h
	Speculative   bool
	Logf          func(string, ...interface{})

	// AllowImpureTemplates skips the pure-render lint. Spike/test only: it
	// exists to demonstrate what the execution-boundary check catches on its own.
	AllowImpureTemplates bool
}

// Load reads and validates a chart from disk.
func loadChart(path string) (*chart.Chart, string, error) {
	c, err := loader.Load(path)
	if err != nil {
		return nil, "", err
	}
	d, err := ChartDigest(c)
	return c, d, err
}

func readOverrides(path string) (map[string]interface{}, error) {
	vals := map[string]interface{}{}
	if path != "" {
		v, err := chartutil.ReadValuesFile(path)
		if err != nil {
			return nil, err
		}
		vals = v
	}
	return roundTrip(vals)
}

// roundTrip normalizes values through JSON so plan-time rendering uses exactly
// the representation apply will decode from the plan file.
func roundTrip(v map[string]interface{}) (map[string]interface{}, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	out := map[string]interface{}{}
	return out, json.Unmarshal(b, &out)
}

// MakePlan reads the baseline, renders via a Helm dry-run against the live API
// server, and records everything apply must recheck. It performs no writes: the
// connection carries a gate that never opens.
func MakePlan(ctx context.Context, o Options) (*Plan, error) {
	if o.ReleaseName == "" || o.Namespace == "" || o.ChartPath == "" {
		return nil, errors.New("release name, namespace and chart path are required")
	}
	if o.TTL == 0 {
		o.TTL = time.Hour
	}
	chartPath, err := filepath.Abs(o.ChartPath)
	if err != nil {
		return nil, err
	}
	chrt, digest, err := loadChart(chartPath)
	if err != nil {
		return nil, err
	}
	if err := CheckNoCRDs(chrt); err != nil {
		return nil, err
	}
	overrides, err := readOverrides(o.OverridesFile)
	if err != nil {
		return nil, err
	}
	pure := "enforced"
	if o.AllowImpureTemplates {
		pure = "skipped"
	} else if err := LintPureRender(chrt, overrides); err != nil {
		return nil, err
	}

	readOnly := newGate(func(*release.Release) error { return errors.New("plan is read-only") })
	c, err := o.Target.connect(o.Namespace, readOnly, o.Logf)
	if err != nil {
		return nil, err
	}
	caps, err := c.capabilities()
	if err != nil {
		return nil, err
	}
	c.helm.Capabilities = caps

	bl, cur, err := c.readBaseline(ctx, o.ReleaseName, o.Namespace)
	if err != nil {
		return nil, err
	}
	if bl.State == StateGitOps {
		return nil, errors.New("baseline is GitOps-managed (Argo CD / Flux metadata present): use `render` for review; apply is not supported")
	}

	rel, err := c.dryRun(o.ReleaseName, o.Namespace, bl, chrt, overrides)
	if err != nil {
		return nil, fmt.Errorf("render: %w", err)
	}

	p := &Plan{
		FormatVersion: FormatVersion,
		CLIVersion:    Version,
		HelmVersion:   HelmVersion(),
		CreatedAt:     time.Now().UTC(),
		Speculative:   o.Speculative,
		ServerURL:     c.serverURL,
		ReleaseName:   o.ReleaseName,
		Namespace:     o.Namespace,
		Action:        "install",
		Chart:         ChartRef{Path: chartPath, Name: chrt.Name(), Version: chrt.Metadata.Version, Digest: digest},
		Overrides:     overrides,
		PureRender:    pure,
		Capabilities:  CapabilitiesRecord{KubeVersion: caps.KubeVersion.Version, APICount: len(caps.APIVersions), Hash: CapabilitiesHash(caps)},
		Baseline:      bl,
		ManifestHash:  ManifestHash(rel.Manifest),
		HooksHash:     HooksHash(rel.Hooks),
		Manifest:      rel.Manifest,
		Hooks:         hookRecords(rel.Hooks),
	}
	p.ExpiresAt = p.CreatedAt.Add(o.TTL)
	if bl.State != StateFresh {
		p.Action = "upgrade"
	}
	if p.OverridesHash, err = canonicalHash(overrides); err != nil {
		return nil, err
	}

	// Objects the plan touches: rendered manifests + hooks, the current
	// release's manifests + hooks (updated or destroyed), and deployment-info.
	target, err := c.parseManifest(rel.Manifest, o.Namespace)
	if err != nil {
		return nil, err
	}
	targetHooks, err := c.parseManifest(hookManifests(rel.Hooks), o.Namespace)
	if err != nil {
		return nil, err
	}
	var current, currentHooks []renderedObject
	if cur != nil {
		if current, err = c.parseManifest(cur.Manifest, o.Namespace); err != nil {
			return nil, err
		}
		if currentHooks, err = c.parseManifest(hookManifests(cur.Hooks), o.Namespace); err != nil {
			return nil, err
		}
	}
	var refs []ObjectRef
	for _, set := range [][]renderedObject{target, targetHooks, current, currentHooks} {
		for _, r := range set {
			refs = append(refs, r.Ref)
		}
	}
	refs = append(refs, deploymentInfoRef(o.Namespace))
	if p.LiveObjects, err = c.liveBaseline(ctx, refs); err != nil {
		return nil, err
	}

	p.Changes = append(diffObjects(current, target, false), diffObjects(currentHooks, targetHooks, true)...)
	p.ValueChanges = diffPaths(bl.CurrentOverrides, overrides, "", 50)
	return p, nil
}

func deploymentInfoRef(ns string) ObjectRef {
	return ObjectRef{APIVersion: "v1", Kind: "Secret", Namespace: ns, Name: DeploymentInfoSecret}
}

func (c *conn) dryRun(name, ns string, bl Baseline, chrt *chart.Chart, vals map[string]interface{}) (*release.Release, error) {
	if bl.State == StateFresh {
		in := action.NewInstall(c.helm)
		in.ReleaseName, in.Namespace = name, ns
		in.DryRun, in.DryRunOption = true, "server" // server: lookup sees the cluster, like the real run
		return in.Run(chrt, vals)
	}
	up := action.NewUpgrade(c.helm)
	up.Namespace = ns
	up.DryRun, up.DryRunOption = true, "server"
	up.ResetValues = true // overrides in the plan are authoritative
	up.MaxHistory = 0
	return up.Run(name, chrt, vals)
}

type recordDigest struct {
	Version      int    `json:"v"`
	Status       string `json:"s"`
	Chart        string `json:"c"`
	ManifestHash string `json:"m"`
	HooksHash    string `json:"h"`
	ConfigHash   string `json:"cfg"`
}

// readBaseline determines fresh / adopted / managed / gitops and digests every
// release record of this name, so any upgrade, rollback or status change in
// between is visible.
func (c *conn) readBaseline(ctx context.Context, name, ns string) (Baseline, *release.Release, error) {
	var bl Baseline
	hist, err := c.helm.Releases.History(name)
	if err != nil && !errors.Is(err, driver.ErrReleaseNotFound) {
		return bl, nil, err
	}
	sort.Slice(hist, func(i, j int) bool { return hist[i].Version < hist[j].Version })
	var recs []recordDigest
	for _, r := range hist {
		ch, _ := canonicalHash(r.Config)
		cv := ""
		if r.Chart != nil && r.Chart.Metadata != nil {
			cv = r.Chart.Metadata.Name + "-" + r.Chart.Metadata.Version
		}
		recs = append(recs, recordDigest{r.Version, r.Info.Status.String(), cv, ManifestHash(r.Manifest), HooksHash(r.Hooks), ch})
	}
	if bl.RecordsDigest, err = canonicalHash(recs); err != nil {
		return bl, nil, err
	}

	di, err := c.getLive(ctx, deploymentInfoRef(ns))
	if err != nil {
		return bl, nil, err
	}
	if bl.DeploymentInfoHash, err = LiveHash(di); err != nil {
		return bl, nil, err
	}

	var last *release.Release
	if len(hist) > 0 {
		last = hist[len(hist)-1]
		bl.Revision, bl.Status = last.Version, last.Info.Status.String()
		if last.Info.Status.IsPending() {
			return bl, nil, fmt.Errorf("release %s is %s (another operation in progress?)", name, last.Info.Status)
		}
		bl.CurrentOverrides = last.Config
	}
	switch {
	case isGitOps(di):
		bl.State = StateGitOps
	case last == nil && di == nil:
		bl.State = StateFresh
	case last == nil:
		return bl, nil, fmt.Errorf("inconsistent baseline: %s exists but there is no Helm release %q", DeploymentInfoSecret, name)
	case di == nil:
		bl.State = StateAdopted
	default:
		bl.State = StateManaged
	}
	// The "current" release Helm diffs against on upgrade is the last deployed
	// (or, failing that, the last) record; use the same.
	var cur *release.Release
	for i := len(hist) - 1; i >= 0; i-- {
		if hist[i].Info.Status == release.StatusDeployed {
			cur = hist[i]
			break
		}
	}
	if cur == nil {
		cur = last
	}
	return bl, cur, nil
}

func isGitOps(u *unstructured.Unstructured) bool {
	if u == nil {
		return false
	}
	for k := range u.GetLabels() {
		if k == "argocd.argoproj.io/instance" || k == "helm.toolkit.fluxcd.io/name" || k == "kustomize.toolkit.fluxcd.io/name" {
			return true
		}
	}
	for k := range u.GetAnnotations() {
		if k == "argocd.argoproj.io/tracking-id" {
			return true
		}
	}
	return false
}

// Save writes the plan with 0600 permissions (it contains the overrides).
func (p *Plan) Save(path string) error {
	b, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}

// LoadPlan reads a plan file.
func LoadPlan(path string) (*Plan, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	p := &Plan{}
	if err := json.Unmarshal(b, p); err != nil {
		return nil, fmt.Errorf("plan file: %w", err)
	}
	if p.FormatVersion != FormatVersion {
		return nil, fmt.Errorf("plan file format %d not supported (want %d)", p.FormatVersion, FormatVersion)
	}
	return p, nil
}
