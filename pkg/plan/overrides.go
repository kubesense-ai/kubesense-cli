package plan

import (
	"fmt"
	"io"
	"reflect"
	"sort"
	"strings"

	"sigs.k8s.io/yaml"
)

// overrideChange is one setting in your values that the plan changes.
type overrideChange struct {
	Section string // top-level key: aggregator, api, global, ...
	Path    string // the rest of the path, e.g. image.tag
	Op      string // + added, - removed, ~ changed
	Before  interface{}
	After   interface{}
}

// overrideChanges walks your old and new values. Maps are walked key by key;
// a list of the same length element by element, so one edited entry is one
// change; any other list, and every scalar, is one setting.
func overrideChanges(before, after map[string]interface{}) []overrideChange {
	var out []overrideChange
	var walk func(a, b interface{}, path []string)
	walk = func(a, b interface{}, path []string) {
		am, aok := a.(map[string]interface{})
		bm, bok := b.(map[string]interface{})
		// A map added or removed whole is walked too, so each of its
		// settings is listed by its full path (api.image.tag, not "api").
		if aok && b == nil && len(am) > 0 {
			bm, bok = map[string]interface{}{}, true
		}
		if bok && a == nil && len(bm) > 0 {
			am, aok = map[string]interface{}{}, true
		}
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
				walk(am[k], bm[k], append(append([]string(nil), path...), k))
			}
			return
		}
		al, aok := a.([]interface{})
		bl, bok := b.([]interface{})
		if aok && bok && len(al) == len(bl) {
			for i := range al {
				p := append([]string(nil), path...)
				p[len(p)-1] = fmt.Sprintf("%s[%d]", p[len(p)-1], i)
				walk(al[i], bl[i], p)
			}
			return
		}
		if reflect.DeepEqual(a, b) {
			return
		}
		c := overrideChange{Section: path[0], Path: strings.Join(path[1:], "."), Op: "~", Before: a, After: b}
		switch {
		case a == nil:
			c.Op = "+"
		case b == nil:
			c.Op = "-"
		}
		out = append(out, c)
	}
	walk(before, after, nil)
	return out
}

// Values longer than this, or on several lines, are shown as a block.
const inlineMax = 48

// valueText renders a value: a string as itself, anything else as YAML.
func valueText(v interface{}) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		if t == "" {
			return `""`
		}
		return t
	}
	b, err := yaml.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return strings.TrimRight(string(b), "\n")
}

const blockMaxLines = 25

// block prints text under a marker, one line each, capped.
func block(w io.Writer, marker string, text string, color func(string) string) {
	lines := strings.Split(text, "\n")
	for i, l := range lines {
		if i == blockMaxLines {
			fmt.Fprintf(w, "        %s\n", Dim(fmt.Sprintf("… %d more lines", len(lines)-blockMaxLines)))
			return
		}
		fmt.Fprintf(w, "        %s\n", color(marker+" "+l))
	}
}

// showOverrides prints your values' changes grouped by section. Credentials
// are masked; long values are shown whole, on their own lines, never cut.
func (p *Plan) showOverrides(w io.Writer) {
	changes := overrideChanges(StripSuppliedValues(p.Baseline.CurrentOverrides), StripSuppliedValues(p.Overrides))
	if len(changes) == 0 {
		return
	}
	fmt.Fprintf(w, "\n%s %s\n", Bold("Your values:"), Dim(fmt.Sprintf("%d setting(s) changed", len(changes))))
	section, indent := "", "  "
	for _, c := range changes {
		name := c.Path
		if name == "" {
			// A top-level setting: no section heading of its own.
			name, indent = c.Section, "  "
			section = ""
		} else {
			if c.Section != section {
				section = c.Section
				fmt.Fprintf(w, "  %s\n", Bold(section))
			}
			indent = "    "
		}
		if sensitiveKey.MatchString(c.Section + "." + c.Path) {
			fmt.Fprintf(w, "%s%s  %s\n", indent, opColor(c.Op, c.Op+" "+name), Dim(masked))
			continue
		}
		before, after := valueText(c.Before), valueText(c.After)
		// Short scalars on one line; maps, lists and long text as a block.
		scalar := func(v interface{}) bool {
			switch v.(type) {
			case map[string]interface{}, []interface{}:
				return false
			}
			return true
		}
		inline := func(s string) bool { return len(s) <= inlineMax && !strings.Contains(s, "\n") }
		switch {
		case !scalar(c.Before) || !scalar(c.After):
			fmt.Fprintf(w, "%s%s\n", indent, opColor(c.Op, c.Op+" "+name))
			if c.Before != nil {
				block(w, "-", before, Red)
			}
			if c.After != nil {
				block(w, "+", after, Green)
			}
		case c.Op == "~" && inline(before) && inline(after):
			fmt.Fprintf(w, "%s%s  %s → %s\n", indent, opColor("~", "~ "+name), Red(before), Green(after))
		case c.Op == "+" && inline(after):
			fmt.Fprintf(w, "%s%s  %s\n", indent, opColor("+", "+ "+name), Green(after))
		case c.Op == "-" && inline(before):
			fmt.Fprintf(w, "%s%s  %s\n", indent, opColor("-", "- "+name), Red(before)+Dim(" (removed)"))
		default:
			fmt.Fprintf(w, "%s%s\n", indent, opColor(c.Op, c.Op+" "+name))
			if c.Before != nil {
				block(w, "-", before, Red)
			}
			if c.After != nil {
				block(w, "+", after, Green)
			}
		}
	}
}
