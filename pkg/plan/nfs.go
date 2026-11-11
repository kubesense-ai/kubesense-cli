package plan

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/release"
	"helm.sh/helm/v3/pkg/storage/driver"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// The NFS server gives the two features that need a ReadWriteMany volume,
// dynamic log parsing and alert config, one when the cluster has no EFS,
// Azure Files or Filestore. It is optional and lives in its own release, like
// the access token. Its Service ClusterIP is what the KubeSense values point
// at: an IP, because the kubelet mounts NFS and cannot resolve cluster DNS.
const (
	NFSRelease = "kubesense-nfs"
	NFSChart   = "nfs"
	NFSService = "kubesense-nfs" // the chart's fullnameOverride
	RefuseNFS  = "nfs-baseline"
)

// nfsFeatures are the values that can use the NFS server.
var nfsFeatures = []string{"dynamicLogParsing", "alertconfig"}

// NFSState is the NFS release as found when planning.
type NFSState struct {
	Installed bool   `json:"installed"`
	Revision  int    `json:"revision,omitempty"`
	Host      string `json:"host,omitempty"` // its Service ClusterIP
}

// NFSStep installs the NFS server before the KubeSense release. Its IP is
// only known once the Service exists, so apply writes it into Paths then.
type NFSStep struct {
	Action           string   `json:"action"` // install | use
	Chart            ChartRef `json:"chart,omitempty"`
	BaselineRevision int      `json:"baselineRevision"`
	PullSecret       string   `json:"pullSecret,omitempty"`
	// Paths are the values set to the server's IP, e.g.
	// global.dynamicLogParsing.nfs.host.
	Paths []string `json:"paths"`
}

func (c *conn) nfsState(ctx context.Context, ns string) (NFSState, error) {
	rel, err := c.helm.Releases.Last(NFSRelease)
	if errors.Is(err, driver.ErrReleaseNotFound) {
		return NFSState{}, nil
	}
	if err != nil {
		return NFSState{}, err
	}
	if rel.Info.Status.IsPending() {
		return NFSState{}, fmt.Errorf("release %s is %s (another operation in progress?)", NFSRelease, rel.Info.Status)
	}
	st := NFSState{Installed: true, Revision: rel.Version}
	svc, err := c.clientset.CoreV1().Services(ns).Get(ctx, NFSService, metav1.GetOptions{})
	if err == nil {
		st.Host = svc.Spec.ClusterIP
	} else if !apierrors.IsNotFound(err) {
		return st, err
	}
	return st, nil
}

func getMap(m map[string]interface{}, keys ...string) map[string]interface{} {
	for _, k := range keys {
		n, _ := m[k].(map[string]interface{})
		if n == nil {
			return nil
		}
		m = n
	}
	return m
}

// setPath sets a dotted path, creating maps on the way.
func setPath(m map[string]interface{}, path []string, v interface{}) {
	for _, k := range path[:len(path)-1] {
		n, ok := m[k].(map[string]interface{})
		if !ok {
			n = map[string]interface{}{}
			m[k] = n
		}
		m = n
	}
	m[path[len(path)-1]] = v
}

// planNFS points the enabled NFS features at the NFS server, and plans its
// install when it is not there. It changes overrides in place.
func (c *conn) planNFS(ctx context.Context, o Options, overrides map[string]interface{}) (NFSState, *NFSStep, error) {
	st, err := c.nfsState(ctx, o.Namespace)
	if err != nil || !o.NFS {
		return st, nil, err
	}
	var paths []string
	for _, f := range nfsFeatures {
		fm := getMap(overrides, "global", f)
		if on, _ := fm["enabled"].(bool); !on {
			continue
		}
		if fs, _ := getMap(fm, "filesystem")["enabled"].(bool); fs {
			return st, nil, fmt.Errorf("--nfs: global.%s.filesystem.enabled is true; use a ReadWriteMany storage class or NFS, not both", f)
		}
		setPath(overrides, []string{"global", f, "nfs", "enabled"}, true)
		paths = append(paths, "global."+f+".nfs.host")
	}
	if len(paths) == 0 {
		return st, nil, errors.New("--nfs: nothing uses NFS; enable global.dynamicLogParsing or global.alertconfig in your values")
	}
	pull := "registry-credentials"
	if s, ok := getMap(overrides, "global", "image")["pullSecrets"].(string); ok && s != "" {
		pull = s
	}
	step := &NFSStep{Action: "use", BaselineRevision: st.Revision, PullSecret: pull, Paths: paths}
	if st.Installed && st.Host != "" {
		for _, f := range nfsFeatures {
			if containsString(paths, "global."+f+".nfs.host") {
				setPath(overrides, []string{"global", f, "nfs", "host"}, st.Host)
			}
		}
		return st, step, nil
	}
	if st.Installed {
		return st, nil, fmt.Errorf("release %s exists but Service %s does not; check it with: kubectl -n %s get svc,sts", NFSRelease, NFSService, o.Namespace)
	}
	if o.Strict {
		return st, nil, errors.New("--nfs with --strict: the NFS server's IP is only known after it is installed, so the render cannot be checked exactly; install it first, or plan without --strict")
	}
	step.Action = "install"
	chartName := o.NFSChart
	if chartName == "" {
		chartName = NFSChart
	}
	path, err := resolveChart(Options{ChartPath: chartName, ChartRepo: o.ChartRepo})
	if err != nil {
		return st, nil, err
	}
	chrt, digest, err := loadChart(path)
	if err != nil {
		return st, nil, err
	}
	step.Chart = ChartRef{Path: path, Name: chrt.Name(), Version: chrt.Metadata.Version, Digest: digest}
	return st, step, nil
}

func containsString(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

// applyNFS installs the planned NFS server, then writes its IP into the
// overrides the KubeSense release is installed with.
func applyNFS(ctx context.Context, p *Plan, t Target, overrides map[string]interface{}, logf func(string, ...interface{})) (string, error) {
	s := p.NFS
	if s == nil || s.Action != "install" {
		return "", nil
	}
	c, err := t.connect(p.Namespace, nil, logf)
	if err != nil {
		return "", err
	}
	st, err := c.nfsState(ctx, p.Namespace)
	if err != nil {
		return "", err
	}
	if st.Revision != s.BaselineRevision {
		return "", refuse(StagePrecheck, RefuseNFS, "release %s is at revision %d, planned from %d", NFSRelease, st.Revision, s.BaselineRevision)
	}
	chrt, digest, err := loadChart(s.Chart.Path)
	if err != nil {
		return "", refuse(StagePrecheck, RefuseChartDigest, "load %s chart: %v", NFSRelease, err)
	}
	if digest != s.Chart.Digest {
		return "", refuse(StagePrecheck, RefuseChartDigest, "%s chart digest %s != planned %s", NFSRelease, shortHash(digest), shortHash(s.Chart.Digest))
	}
	in := action.NewInstall(c.helm)
	in.ReleaseName, in.Namespace = NFSRelease, p.Namespace
	var rel *release.Release
	rel, err = in.RunWithContext(ctx, chrt, map[string]interface{}{
		"imagePullSecrets": []interface{}{map[string]interface{}{"name": s.PullSecret}},
	})
	if err != nil {
		return "", fmt.Errorf("%s: %w", NFSRelease, err)
	}
	var host string
	for i := 0; i < 30 && host == ""; i++ {
		if st, err = c.nfsState(ctx, p.Namespace); err == nil {
			host = st.Host
		}
		if host == "" {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(time.Second):
			}
		}
	}
	if host == "" {
		return "", fmt.Errorf("%s installed (revision %d) but Service %s has no ClusterIP; KubeSense was not changed", NFSRelease, rel.Version, NFSService)
	}
	for _, path := range s.Paths {
		setPath(overrides, strings.Split(path, "."), host)
	}
	return fmt.Sprintf("NFS server installed, release %s revision %d, at %s (set in %d value(s))", NFSRelease, rel.Version, host, len(s.Paths)), nil
}

// showNFS is the plan's NFS line; nothing when NFS is neither installed nor asked for.
func (p *Plan) showNFS(w io.Writer) {
	st, s := p.NFSState, p.NFS
	switch {
	case s != nil && s.Action == "install":
		fmt.Fprintf(w, "%s %s\n", Bold("NFS:     "), Green(fmt.Sprintf("+ install NFS server (release %s, chart %s-%s); its IP goes into %s at apply",
			NFSRelease, s.Chart.Name, s.Chart.Version, strings.Join(s.Paths, ", "))))
	case s != nil:
		fmt.Fprintf(w, "%s using NFS server %s (release %s) for %s\n", Bold("NFS:     "), st.Host, NFSRelease, strings.Join(s.Paths, ", "))
	case st.Installed:
		fmt.Fprintf(w, "%s NFS server present (release %s, %s)\n", Bold("NFS:     "), NFSRelease, st.Host)
	}
}

// sharedStorageWarnings flags an NFS feature that is on with nothing behind
// it: no --nfs, no nfs.host and no ReadWriteMany filesystem.
func sharedStorageWarnings(overrides map[string]interface{}, nfs bool) []string {
	var out []string
	for _, f := range nfsFeatures {
		fm := getMap(overrides, "global", f)
		if on, _ := fm["enabled"].(bool); !on || nfs {
			continue
		}
		if fs, _ := getMap(fm, "filesystem")["enabled"].(bool); fs {
			continue
		}
		if h, _ := getMap(fm, "nfs")["host"].(string); h != "" {
			continue
		}
		out = append(out, fmt.Sprintf("global.%s is on but has no shared storage: add --nfs to install the NFS server, or set global.%s.filesystem (a ReadWriteMany class such as EFS) or global.%s.nfs.host", f, f, f))
	}
	return out
}
