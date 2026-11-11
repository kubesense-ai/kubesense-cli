package plan

import (
	"bytes"
	"fmt"
	"sync"
	"time"

	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/kube"
	"helm.sh/helm/v3/pkg/release"
	"helm.sh/helm/v3/pkg/storage"
	"helm.sh/helm/v3/pkg/storage/driver"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Gate is the execution-boundary validator (option (b)).
//
// It is installed on the Helm action.Configuration in two places:
//
//  1. the release storage driver: the first release-record write (Helm's
//     Releases.Create of the pending-install / pending-upgrade record) is
//     intercepted, and the release object Helm is about to persist - which
//     carries the FINAL post-rendered manifest and the FINAL rendered hooks of
//     the real execution - is validated against the plan, together with a fresh
//     release-baseline and live-baseline recheck. On mismatch the write is never
//     issued.
//  2. a write barrier around the Kubernetes client: any Create/Update/Delete
//     attempted before the gate has opened is refused. This makes the "nothing
//     before the gate" property hold by construction instead of relying on our
//     reading of Helm's call order (it would trip on install-time CRDs, the
//     CreateNamespace path, Replace, history pruning, ...).
type Gate struct {
	validate func(rel *release.Release) error

	mu     sync.Mutex
	opened bool
	err    error

	// Timings for the residual-window measurement.
	ValidateStart   time.Time
	ValidateEnd     time.Time
	FirstWriteStart time.Time
	BlockedWrites   []string
}

func newGate(validate func(*release.Release) error) *Gate { return &Gate{validate: validate} }

// GateError is returned when a write was attempted before validation.
type GateError struct{ Op string }

func (e *GateError) Error() string {
	return fmt.Sprintf("write barrier: %s attempted before the plan was validated at the execution boundary", e.Op)
}

func (g *Gate) requireOpen(op string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.opened {
		return nil
	}
	g.BlockedWrites = append(g.BlockedWrites, op)
	return &GateError{Op: op}
}

func (g *Gate) onFirstRecord(rel *release.Release) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.opened {
		return nil
	}
	if g.err != nil {
		return g.err
	}
	g.ValidateStart = time.Now()
	err := g.validate(rel)
	g.ValidateEnd = time.Now()
	if err != nil {
		g.err = err
		return err
	}
	g.opened = true
	g.FirstWriteStart = time.Now()
	return nil
}

func (g *Gate) installOn(cfg *action.Configuration) {
	if kc, ok := cfg.KubeClient.(*kube.Client); ok {
		cfg.KubeClient = &barrierKube{Client: kc, gate: g}
	} else {
		panic(fmt.Sprintf("unexpected Helm kube client %T", cfg.KubeClient))
	}
	inner := cfg.Releases
	cfg.Releases = storage.Init(&gatedDriver{Driver: inner.Driver, gate: g})
	cfg.Releases.Log = inner.Log
}

// gatedDriver wraps Helm's release storage driver (Secrets in production).
type gatedDriver struct {
	driver.Driver
	gate *Gate
}

func (d *gatedDriver) Create(key string, rls *release.Release) error {
	if err := d.gate.onFirstRecord(rls); err != nil {
		return err
	}
	return d.Driver.Create(key, rls)
}

func (d *gatedDriver) Update(key string, rls *release.Release) error {
	if err := d.gate.requireOpen("release record update " + key); err != nil {
		return err
	}
	return d.Driver.Update(key, rls)
}

func (d *gatedDriver) Delete(key string) (*release.Release, error) {
	if err := d.gate.requireOpen("release record delete " + key); err != nil {
		return nil, err
	}
	return d.Driver.Delete(key)
}

// barrierKube embeds the concrete *kube.Client so every optional interface
// Helm type-asserts for (InterfaceExt, InterfaceResources, ...) is still there.
type barrierKube struct {
	*kube.Client
	gate *Gate
}

func (b *barrierKube) Create(resources kube.ResourceList) (*kube.Result, error) {
	if err := b.gate.requireOpen(describe("create", resources)); err != nil {
		return nil, err
	}
	return b.Client.Create(resources)
}

func (b *barrierKube) Update(original, target kube.ResourceList, force bool) (*kube.Result, error) {
	if err := b.gate.requireOpen(describe("update", target)); err != nil {
		return nil, err
	}
	return b.Client.Update(original, target, force)
}

func (b *barrierKube) Delete(resources kube.ResourceList) (*kube.Result, []error) {
	if err := b.gate.requireOpen(describe("delete", resources)); err != nil {
		return nil, []error{err}
	}
	return b.Client.Delete(resources)
}

func (b *barrierKube) DeleteWithPropagationPolicy(resources kube.ResourceList, policy metav1.DeletionPropagation) (*kube.Result, []error) {
	if err := b.gate.requireOpen(describe("delete", resources)); err != nil {
		return nil, []error{err}
	}
	return b.Client.DeleteWithPropagationPolicy(resources, policy)
}

func describe(op string, rl kube.ResourceList) string {
	var b bytes.Buffer
	b.WriteString(op)
	for i, r := range rl {
		if i == 3 {
			fmt.Fprintf(&b, " (+%d more)", len(rl)-3)
			break
		}
		fmt.Fprintf(&b, " %s/%s", r.Mapping.GroupVersionKind.Kind, r.Name)
	}
	return b.String()
}

// checkingPostRenderer is option (b)'s first, weaker layer: Helm calls it with
// the final non-hook manifest stream of the real render. It can see manifests
// only - v3.16.1 has already split hooks off (pkg/action/action.go:168 before
// :225) - so on its own it cannot catch a hook-only change.
type checkingPostRenderer struct {
	check func(manifest string) error
	ran   bool
}

func (p *checkingPostRenderer) Run(in *bytes.Buffer) (*bytes.Buffer, error) {
	p.ran = true
	s := in.String()
	if err := p.check(s); err != nil {
		return nil, err
	}
	return bytes.NewBufferString(s), nil
}
