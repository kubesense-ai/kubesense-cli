package plan

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chartutil"
	"helm.sh/helm/v3/pkg/release"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// AbsentHash is the live hash recorded for an object that does not exist.
const AbsentHash = "absent"

func sha(b []byte) string {
	s := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(s[:])
}

// canonicalHash hashes v as canonical JSON (encoding/json sorts map keys).
func canonicalHash(v interface{}) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return sha(b), nil
}

// ChartDigest hashes everything Helm loaded for a chart, recursively including
// dependencies: metadata, lock, every raw file (templates, values, crds/, files)
// and, for each dependency, the same again. Two charts with the same digest
// render identically for identical inputs.
func ChartDigest(c *chart.Chart) (string, error) {
	h := sha256.New()
	if err := writeChart(h, c, c.Name()); err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

func writeChart(w io.Writer, c *chart.Chart, prefix string) error {
	meta, err := json.Marshal(c.Metadata)
	if err != nil {
		return err
	}
	lock, err := json.Marshal(c.Lock)
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "chart %s\nmeta %d %s\nlock %d %s\n", prefix, len(meta), meta, len(lock), lock)
	files := append([]*chart.File{}, c.Raw...)
	sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })
	for _, f := range files {
		fmt.Fprintf(w, "file %s %d\n", f.Name, len(f.Data))
		w.Write(f.Data)
	}
	deps := append([]*chart.Chart{}, c.Dependencies()...)
	sort.Slice(deps, func(i, j int) bool { return deps[i].Name() < deps[j].Name() })
	for _, d := range deps {
		if err := writeChart(w, d, prefix+"/charts/"+d.Name()); err != nil {
			return err
		}
	}
	return nil
}

// ManifestHash hashes the rendered (post-rendered) manifest stream exactly as
// Helm stores it in release.Manifest.
func ManifestHash(manifest string) string { return sha([]byte(manifest)) }

type hookRecord struct {
	Name           string   `json:"name"`
	Kind           string   `json:"kind"`
	Path           string   `json:"path"`
	Manifest       string   `json:"manifest"`
	Events         []string `json:"events"`
	Weight         int      `json:"weight"`
	DeletePolicies []string `json:"deletePolicies"`
}

func hookRecords(hooks []*release.Hook) []hookRecord {
	out := make([]hookRecord, 0, len(hooks))
	for _, h := range hooks {
		r := hookRecord{Name: h.Name, Kind: h.Kind, Path: h.Path, Manifest: h.Manifest, Weight: h.Weight}
		for _, e := range h.Events {
			r.Events = append(r.Events, string(e))
		}
		for _, p := range h.DeletePolicies {
			r.DeletePolicies = append(r.DeletePolicies, string(p))
		}
		sort.Strings(r.Events)
		sort.Strings(r.DeletePolicies)
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Path != out[j].Path {
			return out[i].Path < out[j].Path
		}
		return out[i].Kind+"/"+out[i].Name < out[j].Kind+"/"+out[j].Name
	})
	return out
}

// HooksHash hashes the rendered hooks (content, events, weight, delete policy),
// excluding LastRun, which Helm fills in during execution.
func HooksHash(hooks []*release.Hook) string {
	h, err := canonicalHash(hookRecords(hooks))
	if err != nil {
		panic(err) // plain strings and ints cannot fail to marshal
	}
	return h
}

// CapabilitiesHash covers everything from Capabilities that templates can see.
func CapabilitiesHash(c *chartutil.Capabilities) string {
	apis := make([]string, 0, len(c.APIVersions))
	apis = append(apis, c.APIVersions...)
	sort.Strings(apis)
	h, _ := canonicalHash(map[string]interface{}{
		"kubeVersion": c.KubeVersion.Version,
		"major":       c.KubeVersion.Major,
		"minor":       c.KubeVersion.Minor,
		"apiVersions": apis,
		"helmVersion": c.HelmVersion.Version,
	})
	return h
}

// normalizeLive keeps the configuration of a live object and drops everything the
// server or controllers own: status, resourceVersion, managedFields, uid,
// creationTimestamp, generation, selfLink, ownerReferences-free metadata noise.
func normalizeLive(u *unstructured.Unstructured) map[string]interface{} {
	obj := u.DeepCopy().Object
	delete(obj, "status")
	meta := map[string]interface{}{
		"name":      u.GetName(),
		"namespace": u.GetNamespace(),
	}
	if l := u.GetLabels(); len(l) > 0 {
		meta["labels"] = l
	}
	if a := u.GetAnnotations(); len(a) > 0 {
		meta["annotations"] = a
	}
	obj["metadata"] = meta
	return obj
}

// LiveHash hashes the normalized live configuration of an object.
func LiveHash(u *unstructured.Unstructured) (string, error) {
	if u == nil {
		return AbsentHash, nil
	}
	return canonicalHash(normalizeLive(u))
}

func shortHash(h string) string {
	h = strings.TrimPrefix(h, "sha256:")
	if len(h) > 12 {
		return h[:12]
	}
	return h
}
