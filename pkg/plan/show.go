package plan

import (
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"time"
)

// diffObjects classifies objects as add / change / destroy, comparing the new
// render against the current release's recorded manifest (what Helm diffs).
func diffObjects(current, target []renderedObject, hook bool) []Change {
	cur := map[string]renderedObject{}
	for _, o := range current {
		cur[o.Ref.key()] = o
	}
	var out []Change
	seen := map[string]bool{}
	for _, t := range target {
		seen[t.Ref.key()] = true
		c, ok := cur[t.Ref.key()]
		switch {
		case !ok:
			out = append(out, Change{Op: "+", Ref: t.Ref, Hook: hook})
		case !reflect.DeepEqual(withoutExpectedChurn(c), withoutExpectedChurn(t)):
			details := diffPaths(withoutExpectedChurn(c), withoutExpectedChurn(t), "", 8)
			if t.Ref.Kind == "Secret" {
				details = maskAll(details)
			} else {
				details = maskValuePaths(details)
			}
			out = append(out, Change{Op: "~", Ref: t.Ref, Hook: hook, Details: details})
		}
	}
	for _, c := range current {
		if !seen[c.Ref.key()] {
			out = append(out, Change{Op: "-", Ref: c.Ref, Hook: hook})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Ref.key() < out[j].Ref.key() })
	return out
}

// withoutExpectedChurn drops fields that change on every upgrade by design, so
// an upgrade that changes nothing plans no changes. Today that is the release
// revision the chart records in the deployment-info Secret (kept out of its
// content hash for the same reason).
func withoutExpectedChurn(o renderedObject) map[string]interface{} {
	if o.Ref.Kind != "Secret" || !strings.HasSuffix(o.Ref.Name, "-deployment-info") {
		return o.Content
	}
	out := make(map[string]interface{}, len(o.Content))
	for k, v := range o.Content {
		out[k] = v
	}
	for _, field := range []string{"data", "stringData"} {
		if m, ok := out[field].(map[string]interface{}); ok {
			cp := make(map[string]interface{}, len(m))
			for k, v := range m {
				if k != "release-revision" {
					cp[k] = v
				}
			}
			out[field] = cp
		}
	}
	return out
}

// diffPaths lists leaf differences "path: old -> new" (at most max entries).
func diffPaths(a, b interface{}, path string, max int) []string {
	var out []string
	var walk func(a, b interface{}, p string)
	walk = func(a, b interface{}, p string) {
		if len(out) >= max {
			return
		}
		am, aok := a.(map[string]interface{})
		bm, bok := b.(map[string]interface{})
		if aok && bok {
			keys := map[string]bool{}
			for k := range am {
				keys[k] = true
			}
			for k := range bm {
				keys[k] = true
			}
			ks := make([]string, 0, len(keys))
			for k := range keys {
				ks = append(ks, k)
			}
			sort.Strings(ks)
			for _, k := range ks {
				walk(am[k], bm[k], p+"."+k)
			}
			return
		}
		al, aok := a.([]interface{})
		bl, bok := b.([]interface{})
		if aok && bok && len(al) == len(bl) {
			for i := range al {
				walk(al[i], bl[i], fmt.Sprintf("%s[%d]", p, i))
			}
			return
		}
		if !reflect.DeepEqual(a, b) {
			out = append(out, fmt.Sprintf("%s: %s -> %s", strings.TrimPrefix(p, "."), short(a), short(b)))
		}
	}
	walk(a, b, path)
	return out
}

// sensitiveKey matches path segments whose values are credentials. Plan output
// is pasted into tickets and chats; the values it would show are the
// customer's, and need not be.
var sensitiveKey = regexp.MustCompile(`(?i)(password|passwd|secret|token|apikey|api_key|credential|private|cert|tls\.key|\bkey\b|accesskey|access_key)`)

const masked = "(sensitive value changed)"

// maskValuePaths hides the old and new values of "path: old -> new" lines
// whose path names a credential.
func maskValuePaths(lines []string) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		path, _, ok := strings.Cut(l, ": ")
		if ok && sensitiveKey.MatchString(path) {
			out[i] = path + ": " + masked
			continue
		}
		out[i] = l
	}
	return out
}

// maskAll hides every value: a Secret's data is sensitive whatever its keys
// are called.
func maskAll(lines []string) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		path, _, _ := strings.Cut(l, ": ")
		out[i] = path + ": " + masked
	}
	return out
}

func short(v interface{}) string {
	if v == nil {
		return "(none)"
	}
	b, _ := json.Marshal(v)
	s := string(b)
	if len(s) > 60 {
		s = s[:57] + "..."
	}
	return s
}

// Show prints the plan exactly as saved; it never recomputes anything.
func (p *Plan) Show(w io.Writer) {
	rev := "-"
	if p.Baseline.State != StateFresh {
		rev = fmt.Sprintf("revision %d (%s) -> %d", p.Baseline.Revision, p.Baseline.Status, p.Baseline.Revision+1)
	} else {
		rev = "no release -> revision 1"
	}
	cluster := p.ServerURL
	if p.Context != "" {
		cluster = fmt.Sprintf("%s (%s)", p.Context, p.ServerURL)
	}
	fmt.Fprintf(w, "Cluster:  %s\n", cluster)
	if p.ClusterID != "" {
		fmt.Fprintf(w, "          cluster ID %s (kube-system UID)\n", p.ClusterID)
	}
	fmt.Fprintf(w, "Target:   namespace %s, release %s (%s)\n", p.Namespace, p.ReleaseName, p.Action)
	fmt.Fprintf(w, "Baseline: %s, %s\n", p.Baseline.State, rev)
	fmt.Fprintf(w, "Chart:    %s-%s  %s\n", p.Chart.Name, p.Chart.Version, shortHash(p.Chart.Digest))
	fmt.Fprintf(w, "Render:   kube %s, %d API versions; manifests %s, hooks %s; pure-render lint %s\n",
		p.Capabilities.KubeVersion, p.Capabilities.APICount, shortHash(p.ManifestHash), shortHash(p.HooksHash), p.PureRender)
	fmt.Fprintf(w, "Created:  %s by kubesense %s (Helm %s); expires %s\n",
		p.CreatedAt.Format(time.RFC3339), p.CLIVersion, p.HelmVersion, p.ExpiresAt.Format(time.RFC3339))
	for _, wn := range p.Warnings {
		fmt.Fprintf(w, "WARNING:  %s\n", wn)
	}
	if p.Baseline.State == StateAdopted {
		fmt.Fprintf(w, "NOTE: adopted release - %s will be created by this upgrade\n", DeploymentInfoSecret)
	}
	if len(p.ValueChanges) > 0 {
		fmt.Fprintln(w, "\nOverrides:")
		for _, v := range p.ValueChanges {
			fmt.Fprintf(w, "  ~ %s\n", v)
		}
	}
	fmt.Fprintln(w, "\nResources:")
	add, chg, del := 0, 0, 0
	for _, c := range p.Changes {
		tag := ""
		if c.Hook {
			tag = " [hook]"
		}
		fmt.Fprintf(w, "  %s %s%s\n", c.Op, c.Ref.Short(), tag)
		for _, d := range c.Details {
			fmt.Fprintf(w, "        %s\n", d)
		}
		switch c.Op {
		case "+":
			add++
		case "~":
			chg++
		case "-":
			del++
		}
	}
	if len(p.Changes) == 0 {
		fmt.Fprintln(w, "  (no changes)")
	}
	fmt.Fprintf(w, "\nPlan: %d to add, %d to change, %d to destroy.\n", add, chg, del)
	if p.hasMasked() {
		fmt.Fprintln(w, "Values marked (sensitive value changed) are hidden here; a saved plan file holds the overrides and is readable only by you.")
	}
}

func (p *Plan) hasMasked() bool {
	for _, v := range p.ValueChanges {
		if strings.HasSuffix(v, masked) {
			return true
		}
	}
	for _, c := range p.Changes {
		for _, d := range c.Details {
			if strings.HasSuffix(d, masked) {
				return true
			}
		}
	}
	return false
}

// HasChanges reports whether applying the plan would change anything.
func (p *Plan) HasChanges() bool {
	return len(p.Changes) > 0 || len(p.ValueChanges) > 0 || p.Baseline.State == StateFresh || p.Baseline.State == StateAdopted
}
