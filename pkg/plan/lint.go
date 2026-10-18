package plan

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"text/template/parse"

	"helm.sh/helm/v3/pkg/chart"
)

// ImpureFuncs are template functions whose output is not a function of the
// plan's captured inputs (chart digest, overrides, release options,
// Capabilities). A chart that calls any of them cannot be planned under the
// pure-render contract (option (a) of the spike).
var ImpureFuncs = map[string]string{
	"lookup":                   "reads live cluster state at render time",
	"now":                      "wall clock",
	"randAlphaNum":             "random",
	"randAlpha":                "random",
	"randNumeric":              "random",
	"randAscii":                "random",
	"randBytes":                "random",
	"randInt":                  "random",
	"shuffle":                  "random",
	"uuidv4":                   "random",
	"genPrivateKey":            "random key material",
	"genCA":                    "random key material + wall clock",
	"genCAWithKey":             "wall clock",
	"genSelfSignedCert":        "random key material + wall clock",
	"genSelfSignedCertWithKey": "wall clock",
	"genSignedCert":            "random key material + wall clock",
	"genSignedCertWithKey":     "wall clock",
	"htpasswd":                 "random bcrypt salt",
	"bcrypt":                   "random bcrypt salt",
	"encryptAES":               "random IV",
	"getHostByName":            "DNS lookup at render time",
}

// LintError lists every impure call found.
type LintError struct{ Findings []string }

func (e *LintError) Error() string {
	return "chart violates the pure-render contract:\n  " + strings.Join(e.Findings, "\n  ")
}

// LintPureRender parses every template (and every dependency's templates) and
// reports calls to ImpureFuncs. It walks the parse tree, so comments and string
// literals do not produce false positives. It also flags `tpl` and any
// value string that contains template delimiters, because `tpl` turns values
// into templates the parse-tree walk cannot see.
func LintPureRender(c *chart.Chart, overrides map[string]interface{}) error {
	var findings []string
	walkCharts(c, c.Name(), func(ch *chart.Chart, prefix string) {
		for _, t := range ch.Templates {
			fs, err := impureCalls(t.Data)
			if err != nil {
				findings = append(findings, fmt.Sprintf("%s/%s: parse error: %v", prefix, t.Name, err))
				continue
			}
			for _, f := range fs {
				findings = append(findings, fmt.Sprintf("%s/%s: %s", prefix, t.Name, f))
			}
		}
		for _, s := range templatedStrings(ch.Values, "") {
			findings = append(findings, fmt.Sprintf("%s/values.yaml: %s contains template delimiters (rendered via tpl?)", prefix, s))
		}
	})
	for _, s := range templatedStrings(overrides, "") {
		findings = append(findings, fmt.Sprintf("overrides: %s contains template delimiters (rendered via tpl?)", s))
	}
	if len(findings) > 0 {
		sort.Strings(findings)
		return &LintError{Findings: findings}
	}
	return nil
}

func walkCharts(c *chart.Chart, prefix string, fn func(*chart.Chart, string)) {
	fn(c, prefix)
	for _, d := range c.Dependencies() {
		walkCharts(d, prefix+"/charts/"+d.Name(), fn)
	}
}

func impureCalls(src []byte) ([]string, error) {
	trees := map[string]*parse.Tree{}
	t := parse.New("t")
	t.Mode = parse.SkipFuncCheck
	if _, err := t.Parse(string(src), "{{", "}}", trees); err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []string
	var walk func(n parse.Node)
	walk = func(n parse.Node) {
		if n == nil {
			return
		}
		switch x := n.(type) {
		case *parse.ListNode:
			if x == nil {
				return
			}
			for _, c := range x.Nodes {
				walk(c)
			}
		case *parse.ActionNode:
			walk(x.Pipe)
		case *parse.PipeNode:
			if x == nil {
				return
			}
			for _, c := range x.Cmds {
				walk(c)
			}
		case *parse.CommandNode:
			for _, a := range x.Args {
				walk(a)
			}
		case *parse.IdentifierNode:
			why, bad := ImpureFuncs[x.Ident]
			if x.Ident == "tpl" {
				why, bad = "renders a value string as a template (inputs may call impure functions)", true
			}
			if bad && !seen[x.Ident] {
				seen[x.Ident] = true
				out = append(out, fmt.Sprintf("calls %q (%s)", x.Ident, why))
			}
		case *parse.ChainNode:
			walk(x.Node)
		case *parse.IfNode:
			walk(x.Pipe)
			walk(x.List)
			walk(x.ElseList)
		case *parse.RangeNode:
			walk(x.Pipe)
			walk(x.List)
			walk(x.ElseList)
		case *parse.WithNode:
			walk(x.Pipe)
			walk(x.List)
			walk(x.ElseList)
		case *parse.TemplateNode:
			walk(x.Pipe)
		}
	}
	for _, tr := range trees {
		walk(tr.Root)
	}
	sort.Strings(out)
	return out, nil
}

var delims = regexp.MustCompile(`\{\{.*\}\}`)

func templatedStrings(v interface{}, path string) []string {
	var out []string
	switch x := v.(type) {
	case map[string]interface{}:
		for k, c := range x {
			out = append(out, templatedStrings(c, path+"."+k)...)
		}
	case []interface{}:
		for i, c := range x {
			out = append(out, templatedStrings(c, fmt.Sprintf("%s[%d]", path, i))...)
		}
	case string:
		if delims.MatchString(x) {
			out = append(out, path)
		}
	}
	return out
}

// CRDError is returned when a chart or any dependency ships a crds/ directory.
type CRDError struct{ Files []string }

func (e *CRDError) Error() string {
	return fmt.Sprintf("chart ships install-time CRDs (%s); Helm v3.16.1 creates crds/ objects before rendering, "+
		"so a later plan mismatch could not prevent that write. v1 rejects such charts", strings.Join(e.Files, ", "))
}

// CheckNoCRDs rejects charts that ship crds/ at any level. It inspects both
// CRDObjects (what Helm would install, manifest extensions only) and the raw
// files, so a crds/ file with an unusual extension is also refused.
func CheckNoCRDs(c *chart.Chart) error {
	var files []string
	for _, o := range c.CRDObjects() {
		files = append(files, o.Filename)
	}
	walkCharts(c, c.Name(), func(ch *chart.Chart, prefix string) {
		for _, f := range ch.Files {
			if strings.HasPrefix(f.Name, "crds/") {
				files = append(files, prefix+"/"+f.Name)
			}
		}
	})
	if len(files) > 0 {
		sort.Strings(files)
		return &CRDError{Files: dedupe(files)}
	}
	return nil
}

func dedupe(s []string) []string {
	out := s[:0]
	for i, v := range s {
		if i == 0 || v != s[i-1] {
			out = append(out, v)
		}
	}
	return out
}
