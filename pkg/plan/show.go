package plan

import (
	"encoding/json"
	"fmt"
	"io"
	"reflect"
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
		case !reflect.DeepEqual(c.Content, t.Content):
			out = append(out, Change{Op: "~", Ref: t.Ref, Hook: hook, Details: diffPaths(c.Content, t.Content, "", 8)})
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
	fmt.Fprintf(w, "Target:   %s / %s  release %s   (%s)\n", p.ServerURL, p.Namespace, p.ReleaseName, p.Action)
	fmt.Fprintf(w, "Baseline: %s, %s\n", p.Baseline.State, rev)
	fmt.Fprintf(w, "Chart:    %s-%s  %s\n", p.Chart.Name, p.Chart.Version, shortHash(p.Chart.Digest))
	fmt.Fprintf(w, "Render:   kube %s, %d API versions; manifests %s, hooks %s; pure-render lint %s\n",
		p.Capabilities.KubeVersion, p.Capabilities.APICount, shortHash(p.ManifestHash), shortHash(p.HooksHash), p.PureRender)
	fmt.Fprintf(w, "Created:  %s by kubesense %s (Helm %s); expires %s\n",
		p.CreatedAt.Format(time.RFC3339), p.CLIVersion, p.HelmVersion, p.ExpiresAt.Format(time.RFC3339))
	if p.Speculative {
		fmt.Fprintln(w, "NOTE: speculative plan - cannot be applied")
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
	fmt.Fprintf(w, "\nPlan: %d to add, %d to change, %d to destroy. %d live objects pinned.\n", add, chg, del, len(p.LiveObjects))
}
