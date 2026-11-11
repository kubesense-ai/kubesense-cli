package plan

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"helm.sh/helm/v3/pkg/release"
	"helm.sh/helm/v3/pkg/storage/driver"
)

// Status is what is deployed now: the release, the access token and how every
// workload is doing. Read-only.
type Status struct {
	Namespace, ReleaseName string
	Cluster                string
	Release                *release.Release
	AccessToken            AccessTokenState
	Workloads              []WorkloadStatus
}

// ReadStatus reads the release, the access token and the workloads.
func ReadStatus(ctx context.Context, t Target, namespace, name string) (*Status, error) {
	c, err := t.connect(namespace, newGate(func(*release.Release) error { return errors.New("status is read-only") }), nil)
	if err != nil {
		return nil, err
	}
	s := &Status{Namespace: namespace, ReleaseName: name, Cluster: c.context + " (" + c.serverURL + ")"}
	s.Release, err = c.helm.Releases.Last(name)
	if err != nil && !errors.Is(err, driver.ErrReleaseNotFound) {
		return nil, err
	}
	if s.AccessToken, _, err = c.accessTokenState(); err != nil {
		return nil, err
	}
	if s.Workloads, err = releaseWorkloads(ctx, c.clientset, namespace, name); err != nil {
		return nil, err
	}
	return s, nil
}

func ago(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
	return fmt.Sprintf("%dd ago", int(d.Hours()/24))
}

func statusColor(s release.Status) string {
	switch {
	case s == release.StatusDeployed:
		return Green(s.String())
	case s.IsPending():
		return Yellow(s.String())
	case s == release.StatusFailed:
		return Red(s.String())
	}
	return s.String()
}

// Show prints the status.
func (s *Status) Show(w io.Writer) {
	fmt.Fprintf(w, "%s %s\n", Bold("Cluster: "), s.Cluster)
	r := s.Release
	if r == nil {
		fmt.Fprintf(w, "%s no release %s in namespace %s\n", Bold("Release: "), s.ReleaseName, s.Namespace)
	} else {
		fmt.Fprintf(w, "%s %s in %s, revision %d (%s), %s (%s)\n", Bold("Release: "), s.ReleaseName, s.Namespace,
			r.Version, statusColor(r.Info.Status), r.Info.LastDeployed.Local().Format("2 Jan 15:04"), ago(r.Info.LastDeployed.Time))
		fmt.Fprintf(w, "%s %s-%s\n", Bold("Chart:   "), r.Chart.Metadata.Name, r.Chart.Metadata.Version)
		if r.Info.Status.IsPending() {
			fmt.Fprintf(w, "%s %s\n", Yellow("WARNING: "), Yellow("an operation is in progress or was interrupted; plan and apply are refused until it finishes"))
		}
		if r.Info.Status == release.StatusFailed && r.Info.Description != "" {
			fmt.Fprintf(w, "%s %s\n", Red("Failed:  "), r.Info.Description)
		}
	}
	(&Plan{AccessTokenState: s.AccessToken}).showAccessToken(w)

	if len(s.Workloads) == 0 {
		return
	}
	ready := 0
	for _, wl := range s.Workloads {
		if wl.Done() {
			ready++
		}
	}
	summary := fmt.Sprintf("%d/%d ready", ready, len(s.Workloads))
	if ready == len(s.Workloads) {
		summary = Green(summary)
	} else {
		summary = Yellow(summary)
	}
	fmt.Fprintf(w, "\n%s %s\n", Bold("Workloads:"), summary)
	// Not ready first: that is what someone running status is looking for.
	ws := append([]WorkloadStatus(nil), s.Workloads...)
	sort.SliceStable(ws, func(i, j int) bool { return !ws[i].Done() && ws[j].Done() })
	for _, wl := range ws {
		if wl.Done() {
			fmt.Fprintf(w, "  %s %s\n", Green("✓"), wl)
		} else {
			fmt.Fprintf(w, "  %s\n", Red("! "+wl.String()))
		}
	}
}

// Revision is one entry of the release history with what changed in your
// values against the revision before it.
type Revision struct {
	Rel     *release.Release
	Changes []overrideChange
	Images  []overrideChange // the image tag changes among them
}

// ReadHistory returns the release's revisions, newest first.
func ReadHistory(t Target, namespace, name string) ([]Revision, error) {
	c, err := t.connect(namespace, newGate(func(*release.Release) error { return errors.New("history is read-only") }), nil)
	if err != nil {
		return nil, err
	}
	hist, err := c.helm.Releases.History(name)
	if errors.Is(err, driver.ErrReleaseNotFound) || (err == nil && len(hist) == 0) {
		return nil, fmt.Errorf("no release %s in namespace %s", name, namespace)
	}
	if err != nil {
		return nil, err
	}
	sort.Slice(hist, func(i, j int) bool { return hist[i].Version < hist[j].Version })
	out := make([]Revision, len(hist))
	var prev map[string]interface{}
	for i, r := range hist {
		cur := StripSuppliedValues(r.Config)
		rv := Revision{Rel: r}
		if i > 0 {
			rv.Changes = overrideChanges(prev, cur)
			for _, ch := range rv.Changes {
				if strings.HasSuffix(ch.Section+"."+ch.Path, "image.tag") {
					rv.Images = append(rv.Images, ch)
				}
			}
		}
		prev = cur
		out[len(hist)-1-i] = rv
	}
	return out, nil
}

// ShowHistory prints one line per revision, then its image changes.
func ShowHistory(w io.Writer, revs []Revision) {
	for _, r := range revs {
		rel := r.Rel
		chart := rel.Chart.Metadata.Name + "-" + rel.Chart.Metadata.Version
		fmt.Fprintf(w, "%s  %s  %-12s  %s", Bold(fmt.Sprintf("%4d", rel.Version)),
			rel.Info.LastDeployed.Local().Format("2006-01-02 15:04"), statusColor(rel.Info.Status), chart)
		switch n := len(r.Changes); {
		case r.Rel.Version == revs[len(revs)-1].Rel.Version:
			fmt.Fprintf(w, "  %s", Dim("first recorded"))
		case n == 0:
			fmt.Fprintf(w, "  %s", Dim("no value changes"))
		default:
			fmt.Fprintf(w, "  %s", Yellow(fmt.Sprintf("%d setting(s) changed", n)))
		}
		fmt.Fprintln(w)
		for _, im := range r.Images {
			tag := func(v interface{}) string {
				if v == nil {
					return "chart default"
				}
				return valueText(v)
			}
			fmt.Fprintf(w, "        %s  %s → %s\n", im.Section+"."+im.Path, Red(tag(im.Before)), Green(tag(im.After)))
		}
		if rel.Info.Status == release.StatusFailed && rel.Info.Description != "" {
			fmt.Fprintf(w, "        %s\n", Red(rel.Info.Description))
		}
	}
}

// ShowRevisionChanges prints what one revision changed in your values.
func ShowRevisionChanges(w io.Writer, revs []Revision, version int) error {
	for i, r := range revs {
		if r.Rel.Version != version {
			continue
		}
		var before map[string]interface{}
		if i+1 < len(revs) {
			before = revs[i+1].Rel.Config
		}
		fmt.Fprintf(w, "%s %d, %s, chart %s-%s\n", Bold("Revision"), version,
			r.Rel.Info.LastDeployed.Local().Format("2006-01-02 15:04"), r.Rel.Chart.Metadata.Name, r.Rel.Chart.Metadata.Version)
		p := &Plan{Baseline: Baseline{CurrentOverrides: before}, Overrides: r.Rel.Config}
		if len(overrideChanges(StripSuppliedValues(before), StripSuppliedValues(r.Rel.Config))) == 0 {
			fmt.Fprintln(w, Dim("No change in your values."))
			return nil
		}
		p.showOverrides(w)
		return nil
	}
	return fmt.Errorf("no revision %d", version)
}
