package plan

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"

	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chartutil"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// Target identifies the cluster explicitly. The spike never falls back to the
// default kubeconfig or the current context of ~/.kube/config.
type Target struct {
	Kubeconfig string // required, explicit path
	Context    string // optional context inside that file
	CacheDir   string // discovery cache; defaults to a private temp dir, never ~/.kube/cache
}

// spikeRequireLocal is the spike's safety interlock: every connection (read or
// write) must point at a loopback API server (envtest / kind). Production would
// drop this and add a cluster-identity check instead.
const spikeRequireLocal = true

type conn struct {
	flags     *genericclioptions.ConfigFlags
	rest      *rest.Config
	clientset kubernetes.Interface
	dynamic   dynamic.Interface
	mapper    meta.RESTMapper
	helm      *action.Configuration
	serverURL string
}

func (t Target) connect(namespace string, gate *Gate, logf func(string, ...interface{})) (*conn, error) {
	if t.Kubeconfig == "" {
		return nil, fmt.Errorf("an explicit kubeconfig path is required (refusing to use the default kubeconfig)")
	}
	if _, err := os.Stat(t.Kubeconfig); err != nil {
		return nil, fmt.Errorf("kubeconfig: %w", err)
	}
	kc, ctx, ns := t.Kubeconfig, t.Context, namespace
	flags := genericclioptions.NewConfigFlags(false)
	flags.KubeConfig = &kc
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
	if err := assertLoopback(rc.Host); err != nil {
		return nil, err
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
	return &conn{flags: flags, rest: rc, clientset: cs, dynamic: dyn, mapper: mapper, helm: cfg, serverURL: rc.Host}, nil
}

func assertLoopback(host string) error {
	if !spikeRequireLocal {
		return nil
	}
	u, err := url.Parse(host)
	if err != nil {
		return fmt.Errorf("spike safety: cannot parse API server URL %q: %w", host, err)
	}
	h := u.Hostname()
	if h == "localhost" {
		return nil
	}
	if ip := net.ParseIP(h); ip != nil && ip.IsLoopback() {
		return nil
	}
	return fmt.Errorf("spike safety: API server %q is not loopback; the spike only talks to local test clusters", host)
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
