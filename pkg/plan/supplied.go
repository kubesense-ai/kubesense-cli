package plan

import (
	"sigs.k8s.io/yaml"
)

// The values you supplied (the merged -f files), attached to the release as a
// string at global.kubesenseManager.suppliedValues. Helm gives a chart only the
// computed values, with every default merged in; this is how the chart (and the
// kubesense-manager after it) can show "your values" next to them. It is part
// of the overrides before the plan renders, so the plan covers it and apply
// sends exactly what was planned.

// suppliedValuesPath is where the chart reads the string.
var suppliedValuesPath = []string{"global", "kubesenseManager", "suppliedValues"}

// StripSuppliedValues returns vals without the attached copy: it is derived
// from the rest, so it is never diffed, pulled or attached again inside itself.
func StripSuppliedValues(vals map[string]interface{}) map[string]interface{} {
	return without(vals, suppliedValuesPath)
}

// withSuppliedValues attaches the YAML of vals (minus any earlier copy).
func withSuppliedValues(vals map[string]interface{}) (map[string]interface{}, error) {
	own := StripSuppliedValues(vals)
	if len(own) == 0 {
		return own, nil
	}
	b, err := yaml.Marshal(own)
	if err != nil {
		return nil, err
	}
	attach := map[string]interface{}{"global": map[string]interface{}{
		"kubesenseManager": map[string]interface{}{"suppliedValues": string(b)},
	}}
	return roundTrip(mergeMaps(own, attach))
}

// without copies m, leaving out the key at path and any maps that became empty.
func without(m map[string]interface{}, path []string) map[string]interface{} {
	out := make(map[string]interface{}, len(m))
	for k, v := range m {
		out[k] = v
	}
	if len(path) == 0 {
		return out
	}
	if len(path) == 1 {
		delete(out, path[0])
		return out
	}
	child, ok := out[path[0]].(map[string]interface{})
	if !ok {
		return out
	}
	if c := without(child, path[1:]); len(c) > 0 {
		out[path[0]] = c
	} else {
		delete(out, path[0])
	}
	return out
}
