package plan

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"helm.sh/helm/v3/pkg/release"
	"helm.sh/helm/v3/pkg/releaseutil"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/yaml"
)

// ObjectRef identifies one Kubernetes object.
type ObjectRef struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Namespace  string `json:"namespace,omitempty"`
	Name       string `json:"name"`
}

func (r ObjectRef) String() string {
	if r.Namespace != "" {
		return fmt.Sprintf("%s/%s (%s, ns %s)", r.Kind, r.Name, r.APIVersion, r.Namespace)
	}
	return fmt.Sprintf("%s/%s (%s)", r.Kind, r.Name, r.APIVersion)
}

// Short is "Kind/name".
func (r ObjectRef) Short() string { return r.Kind + "/" + r.Name }

func (r ObjectRef) key() string {
	gv, _ := schema.ParseGroupVersion(r.APIVersion)
	return gv.Group + "|" + r.Kind + "|" + r.Namespace + "|" + r.Name
}

// LiveObject is the live-baseline entry for one object the plan touches.
type LiveObject struct {
	Ref  ObjectRef `json:"ref"`
	Hash string    `json:"hash"` // AbsentHash when the object does not exist
}

type renderedObject struct {
	Ref     ObjectRef
	Content map[string]interface{}
	Source  string
}

// parseManifest splits a Helm manifest stream into objects. Namespace defaults
// to the release namespace for namespaced kinds.
func (c *conn) parseManifest(manifest, defaultNS string) ([]renderedObject, error) {
	var out []renderedObject
	docs := releaseutil.SplitManifests(manifest)
	keys := make([]string, 0, len(docs))
	for k := range docs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		doc := docs[k]
		var m map[string]interface{}
		if err := yaml.Unmarshal([]byte(doc), &m); err != nil {
			return nil, fmt.Errorf("parse manifest: %w", err)
		}
		if len(m) == 0 {
			continue
		}
		u := &unstructured.Unstructured{Object: m}
		ref := ObjectRef{APIVersion: u.GetAPIVersion(), Kind: u.GetKind(), Namespace: u.GetNamespace(), Name: u.GetName()}
		mapping, err := c.mapping(ref)
		if err != nil {
			return nil, err
		}
		if mapping.Scope.Name() == meta.RESTScopeNameNamespace {
			if ref.Namespace == "" {
				ref.Namespace = defaultNS
			}
		} else {
			ref.Namespace = ""
		}
		out = append(out, renderedObject{Ref: ref, Content: m, Source: sourceOf(doc)})
	}
	return out, nil
}

func sourceOf(doc string) string {
	for _, l := range strings.Split(doc, "\n") {
		if strings.HasPrefix(l, "# Source: ") {
			return strings.TrimPrefix(l, "# Source: ")
		}
	}
	return ""
}

func hookManifests(hooks []*release.Hook) string {
	var b strings.Builder
	for _, h := range hooks {
		fmt.Fprintf(&b, "---\n# Source: %s\n%s\n", h.Path, h.Manifest)
	}
	return b.String()
}

func (c *conn) mapping(ref ObjectRef) (*meta.RESTMapping, error) {
	gv, err := schema.ParseGroupVersion(ref.APIVersion)
	if err != nil {
		return nil, err
	}
	m, err := c.mapper.RESTMapping(gv.WithKind(ref.Kind).GroupKind(), gv.Version)
	if err != nil {
		return nil, fmt.Errorf("resolve %s: %w", ref, err)
	}
	return m, nil
}

func (c *conn) getLive(ctx context.Context, ref ObjectRef) (*unstructured.Unstructured, error) {
	m, err := c.mapping(ref)
	if err != nil {
		return nil, err
	}
	var ri interface {
		Get(context.Context, string, metav1.GetOptions, ...string) (*unstructured.Unstructured, error)
	}
	if m.Scope.Name() == meta.RESTScopeNameNamespace {
		ri = c.dynamic.Resource(m.Resource).Namespace(ref.Namespace)
	} else {
		ri = c.dynamic.Resource(m.Resource)
	}
	u, err := ri.Get(ctx, ref.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	return u, err
}

// liveBaseline hashes the live state of every ref (deduplicated, sorted).
func (c *conn) liveBaseline(ctx context.Context, refs []ObjectRef) ([]LiveObject, error) {
	seen := map[string]bool{}
	var out []LiveObject
	for _, r := range refs {
		if seen[r.key()] {
			continue
		}
		seen[r.key()] = true
		u, err := c.getLive(ctx, r)
		if err != nil {
			return nil, err
		}
		h, err := LiveHash(u)
		if err != nil {
			return nil, err
		}
		out = append(out, LiveObject{Ref: r, Hash: h})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Ref.key() < out[j].Ref.key() })
	return out, nil
}

// LiveBaselineError lists objects whose live configuration changed.
type LiveBaselineError struct{ Changed []string }

func (e *LiveBaselineError) Error() string {
	return "live baseline changed since plan: " + strings.Join(e.Changed, "; ")
}

// recheckLive compares the live state against the plan's entries.
func (c *conn) recheckLive(ctx context.Context, want []LiveObject) (int, error) {
	var changed []string
	n := 0
	for _, w := range want {
		u, err := c.getLive(ctx, w.Ref)
		n++
		if err != nil {
			return n, err
		}
		h, err := LiveHash(u)
		if err != nil {
			return n, err
		}
		if h != w.Hash {
			changed = append(changed, fmt.Sprintf("%s: %s -> %s", w.Ref.Short(), shortHash(w.Hash), shortHash(h)))
		}
	}
	if len(changed) > 0 {
		return n, &LiveBaselineError{Changed: changed}
	}
	return n, nil
}
