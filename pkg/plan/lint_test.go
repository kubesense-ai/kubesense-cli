package plan

import (
	"strings"
	"testing"
)

func TestImpureCalls(t *testing.T) {
	cases := map[string]string{
		`{{ lookup "v1" "Secret" "ns" "x" }}`:                                             "lookup",
		`{{ if .Values.a }}{{ with .Values.b }}{{ now | date "2006" }}{{ end }}{{ end }}`: "now",
		`{{ define "x" }}{{ randAlphaNum 8 }}{{ end }}`:                                   "randAlphaNum",
		`{{ $k := genPrivateKey "rsa" }}`:                                                 "genPrivateKey",
		`{{ range .Values.l }}{{ uuidv4 }}{{ end }}`:                                      "uuidv4",
		`{{ tpl .Values.t . }}`:                                                           "tpl",
		`{{ include "x" (dict "a" (randNumeric 3)) }}`:                                    "randNumeric",
	}
	for src, want := range cases {
		got, err := impureCalls([]byte(src))
		if err != nil {
			t.Fatalf("%s: %v", src, err)
		}
		if len(got) != 1 || !strings.Contains(got[0], `"`+want+`"`) {
			t.Errorf("%s: got %v, want %s", src, got, want)
		}
	}
	clean := []string{
		`{{/* lookup now randAlphaNum */}}{{ .Values.x | quote }}`,
		`value: "lookup now"`,
		`{{ .Values.lookup }}`,
		`{{ toYaml .Values | indent 2 }}{{ sha256sum .Values.x }}`,
	}
	for _, src := range clean {
		got, err := impureCalls([]byte(src))
		if err != nil || len(got) != 0 {
			t.Errorf("%s: false positive %v %v", src, got, err)
		}
	}
}

func TestTemplatedValuesFlagged(t *testing.T) {
	got := templatedStrings(map[string]interface{}{"a": map[string]interface{}{"b": "{{ lookup \"v1\" \"x\" \"\" \"\" }}"}, "c": "plain"}, "")
	if len(got) != 1 || got[0] != ".a.b" {
		t.Fatalf("got %v", got)
	}
}
