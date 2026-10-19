package plan

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"

	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chartutil"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// Target identifies the cluster. Kubeconfig follows kubectl's rules when empty
// ($KUBECONFIG, then ~/.kube/config); the context and API server actually used
// are recorded on the plan and printed, so the reviewer sees which cluster it
// was made against.
type Target struct {
	Kubeconfig string // optional; kubectl's loading rules when empty
	Context    string // optional; the kubeconfig's current context when empty
	CacheDir   string // discovery cache; defaults to a private temp dir, never ~/.kube/cache

	// RequireLoopback refuses any API server that is not loopback. Tests set
	// it so the suite can only ever reach envtest or kind.
	RequireLoopback bool
}

type conn struct {
	flags     *genericclioptions.ConfigFlags
	rest      *rest.Config
	clientset kubernetes.Interface
	dynamic   dynamic.Interface
	mapper    meta.RESTMapper
	helm      *action.Configuration
	serverURL string
	context   string
}

func (t Target) connect(namespace string, gate *Gate, logf func(string, ...interface{})) (*conn, error) {
	if t.Kubeconfig != "" {
		if _, err := os.Stat(t.Kubeconfig); err != nil {
			return nil, fmt.Errorf("kubeconfig: %w", err)
		}
	}
	kc, ctx, ns := t.Kubeconfig, t.Context, namespace
	flags := genericclioptions.NewConfigFlags(false)
	if kc != "" {
		flags.KubeConfig = &kc
	}
	flags.Namespace = &ns
	if ctx != "" {
		flags.Context = &ctx
	}
	cacheDir := t.CacheDir
	if cacheDir == "" {
		cacheDir = filepath.Join(os.TempDir(), "kubesense-plan-discovery-cache")
	}
	flags.CacheDir = &cacheDir
	rc, err := flags.ToRESTConfig()
	if err != nil {
		return nil, err
	}
	if t.RequireLoopback {
		if err := assertLoopback(rc.Host); err != nil {
			return nil, err
		}
	}
	raw, err := flags.ToRawKubeConfigLoader().RawConfig()
	if err != nil {
		return nil, err
	}
	contextName := t.Context
	if contextName == "" {
		contextName = raw.CurrentContext
	}
	cs, err := kubernetes.NewForConfig(rc)
	if err != nil {
		return nil, err
	}
	dyn, err := dynamic.NewForConfig(rc)
	if err != nil {
		return nil, err
	}
	mapper, err := flags.ToRESTMapper()
	if err != nil {
		return nil, err
	}
	if logf == nil {
		logf = func(string, ...interface{}) {}
	}
	cfg := new(action.Configuration)
	if err := cfg.Init(flags, namespace, "secret", logf); err != nil {
		return nil, err
	}
	if gate != nil {
		gate.installOn(cfg)
	}
	return &conn{flags: flags, rest: rc, clientset: cs, dynamic: dyn, mapper: mapper, helm: cfg, serverURL: rc.Host, context: contextName}, nil
}

func assertLoopback(host string) error {
	u, err := url.Parse(host)
	if err != nil {
		return fmt.Errorf("safety: cannot parse API server URL %q: %w", host, err)
	}
	h := u.Hostname()
	if h == "localhost" {
		return nil
	}
	if ip := net.ParseIP(h); ip != nil && ip.IsLoopback() {
		return nil
	}
	return fmt.Errorf("safety: API server %q is not loopback; only local test clusters are allowed here", host)
}

// clusterID is the kube-system namespace's UID: stable for the life of a
// cluster and different between clusters, unlike the API server URL, which
// load balancers and port-forwards change.
func (c *conn) clusterID(ctx context.Context) (string, error) {
	ns, err := c.clientset.CoreV1().Namespaces().Get(ctx, "kube-system", metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("read cluster identity (kube-system namespace): %w", err)
	}
	return string(ns.UID), nil
}

// capabilities reads Capabilities fresh from discovery exactly the way Helm's
// unexported getCapabilities does, so the result can be compared to the plan and
// then pinned into cfg.Capabilities (Helm uses a non-nil value as-is).
func (c *conn) capabilities() (*chartutil.Capabilities, error) {
	dc, err := c.flags.ToDiscoveryClient()
	if err != nil {
		return nil, err
	}
	dc.Invalidate()
	kv, err := dc.ServerVersion()
	if err != nil {
		return nil, err
	}
	apis, err := action.GetVersionSet(dc)
	if err != nil {
		return nil, err
	}
	return &chartutil.Capabilities{
		APIVersions: apis,
		KubeVersion: chartutil.KubeVersion{Version: kv.GitVersion, Major: kv.Major, Minor: kv.Minor},
		HelmVersion: chartutil.DefaultCapabilities.HelmVersion,
	}, nil
}
