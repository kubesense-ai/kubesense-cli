package plan

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/release"
	"helm.sh/helm/v3/pkg/storage/driver"
)

// The access token is the credential KubeSense pods pull images with, from
// KubeSense's registry. It lives in its own Helm release next to KubeSense.
// It is optional: a customer that mirrors KubeSense images into its own
// registry has no access-token release and needs none.
const (
	AccessTokenRelease = "access-token"
	AccessTokenChart   = "access-token" // chart name in the KubeSense repository
	RefuseAccessToken  = "access-token-baseline"
)

// AccessTokenState is the access-token release as found when planning.
type AccessTokenState struct {
	Installed bool      `json:"installed"`
	Revision  int       `json:"revision,omitempty"`
	KeyID     string    `json:"keyID,omitempty"`
	Deployed  time.Time `json:"deployed,omitempty"`
}

// AccessTokenStep installs or rotates the access token. Planned only when a
// token is given; apply runs it before the KubeSense release, so a new pull
// secret exists before pods roll.
type AccessTokenStep struct {
	Action string `json:"action"` // install | upgrade | unchanged
	// Values are KEY_ID and KEY_SECRET. NOTE: the secret is in the plan file,
	// which is written 0600 like the overrides.
	Values           map[string]interface{} `json:"values"`
	Chart            ChartRef               `json:"chart"`
	BaselineRevision int                    `json:"baselineRevision"`
}

// KeyID is the token's key ID (not secret).
func (s *AccessTokenStep) KeyID() string { v, _ := s.Values["KEY_ID"].(string); return v }

// ParseAccessToken reads the token KubeSense issues: base64 of "KEY_ID:KEY_SECRET".
func ParseAccessToken(raw string) (id, secret string, err error) {
	b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(raw))
	if err != nil {
		return "", "", errors.New("access token is not base64: paste it exactly as issued")
	}
	id, secret, ok := strings.Cut(string(b), ":")
	if !ok || id == "" || secret == "" {
		return "", "", errors.New("access token does not decode to KEY_ID:KEY_SECRET")
	}
	return id, secret, nil
}

// ReadAccessToken reads a token from a file, or stdin for "-". Never from a
// flag value: that would leave it in shell history.
func ReadAccessToken(path string, stdin io.Reader) (string, error) {
	if path == "-" {
		b, err := io.ReadAll(stdin)
		return string(b), err
	}
	b, err := os.ReadFile(path)
	return string(b), err
}

// MaskKeyID shows enough of a key ID to recognise it: AKIA…7Q.
func MaskKeyID(id string) string {
	if len(id) <= 6 {
		return strings.Repeat("•", len(id))
	}
	return id[:4] + "…" + id[len(id)-2:]
}

func (c *conn) accessTokenState() (AccessTokenState, map[string]interface{}, error) {
	rel, err := c.helm.Releases.Last(AccessTokenRelease)
	if errors.Is(err, driver.ErrReleaseNotFound) {
		return AccessTokenState{}, nil, nil
	}
	if err != nil {
		return AccessTokenState{}, nil, err
	}
	if rel.Info.Status.IsPending() {
		return AccessTokenState{}, nil, fmt.Errorf("release %s is %s (another operation in progress?)", AccessTokenRelease, rel.Info.Status)
	}
	st := AccessTokenState{Installed: true, Revision: rel.Version}
	if id, ok := rel.Config["KEY_ID"].(string); ok {
		st.KeyID = id
	}
	if !rel.Info.LastDeployed.IsZero() {
		st.Deployed = rel.Info.LastDeployed.Time
	}
	return st, rel.Config, nil
}

// planAccessToken records the access-token release's state and, when a token
// is given, the step that installs or rotates it.
func (c *conn) planAccessToken(o Options) (AccessTokenState, *AccessTokenStep, error) {
	st, current, err := c.accessTokenState()
	if err != nil || o.AccessToken == "" {
		return st, nil, err
	}
	id, secret, err := ParseAccessToken(o.AccessToken)
	if err != nil {
		return st, nil, err
	}
	step := &AccessTokenStep{
		Action:           "install",
		Values:           map[string]interface{}{"KEY_ID": id, "KEY_SECRET": secret},
		BaselineRevision: st.Revision,
	}
	if st.Installed {
		step.Action = "upgrade"
		if current["KEY_ID"] == id && current["KEY_SECRET"] == secret {
			step.Action = "unchanged"
		}
	}
	chartName := o.AccessTokenChart
	if chartName == "" {
		chartName = AccessTokenChart
	}
	path, err := resolveChart(Options{ChartPath: chartName, ChartRepo: o.ChartRepo})
	if err != nil {
		return st, nil, err
	}
	chrt, digest, err := loadChart(path)
	if err != nil {
		return st, nil, err
	}
	step.Chart = ChartRef{Path: path, Name: chrt.Name(), Version: chrt.Metadata.Version, Digest: digest}
	return st, step, nil
}

// applyAccessToken runs the planned step, if any, after checking the
// access-token release is still where the plan found it.
func applyAccessToken(ctx context.Context, p *Plan, t Target, logf func(string, ...interface{})) (string, error) {
	s := p.AccessToken
	if s == nil || s.Action == "unchanged" {
		return "", nil
	}
	c, err := t.connect(p.Namespace, nil, logf)
	if err != nil {
		return "", err
	}
	st, _, err := c.accessTokenState()
	if err != nil {
		return "", err
	}
	stepf("access token: %s (release %s at revision %d, planned from %d)", s.Action, AccessTokenRelease, st.Revision, s.BaselineRevision)
	if st.Revision != s.BaselineRevision {
		return "", refuse(StagePrecheck, RefuseAccessToken, "release %s is at revision %d, planned from %d", AccessTokenRelease, st.Revision, s.BaselineRevision)
	}
	chrt, digest, err := loadChart(s.Chart.Path)
	if err != nil {
		return "", refuse(StagePrecheck, RefuseChartDigest, "load %s chart: %v", AccessTokenRelease, err)
	}
	if digest != s.Chart.Digest {
		return "", refuse(StagePrecheck, RefuseChartDigest, "%s chart digest %s != planned %s", AccessTokenRelease, shortHash(digest), shortHash(s.Chart.Digest))
	}
	var rel *release.Release
	if s.Action == "install" {
		in := action.NewInstall(c.helm)
		in.ReleaseName, in.Namespace = AccessTokenRelease, p.Namespace
		rel, err = in.RunWithContext(ctx, chrt, s.Values)
	} else {
		up := action.NewUpgrade(c.helm)
		up.Namespace, up.ResetValues = p.Namespace, true
		rel, err = up.RunWithContext(ctx, AccessTokenRelease, chrt, s.Values)
	}
	if err != nil {
		return "", fmt.Errorf("%s: %w", AccessTokenRelease, err)
	}
	verb := "installed"
	if s.Action == "upgrade" {
		verb = "rotated"
	}
	return fmt.Sprintf("access token %s (key %s), release %s revision %d", verb, MaskKeyID(s.KeyID()), AccessTokenRelease, rel.Version), nil
}

// showAccessToken is the plan's access-token line.
func (p *Plan) showAccessToken(w io.Writer) {
	st := p.AccessTokenState
	s := p.AccessToken
	switch {
	case s != nil && s.Action == "install":
		fmt.Fprintf(w, "%s %s\n", Bold("Access:  "), Green(fmt.Sprintf("+ install access token, key %s (release %s)", MaskKeyID(s.KeyID()), AccessTokenRelease)))
	case s != nil && s.Action == "upgrade" && st.KeyID != s.KeyID():
		fmt.Fprintf(w, "%s %s\n", Bold("Access:  "), Yellow(fmt.Sprintf("~ rotate access token, key %s -> %s", MaskKeyID(st.KeyID), MaskKeyID(s.KeyID()))))
	case s != nil && s.Action == "upgrade":
		fmt.Fprintf(w, "%s %s\n", Bold("Access:  "), Yellow(fmt.Sprintf("~ replace access token secret, key %s", MaskKeyID(s.KeyID()))))
	case st.Installed:
		when := ""
		if !st.Deployed.IsZero() {
			when = ", set " + st.Deployed.Format("2 Jan 2006")
		}
		fmt.Fprintf(w, Bold("Access:  ")+" access token present, key %s%s; unchanged\n", MaskKeyID(st.KeyID), when)
	default:
		fmt.Fprintf(w, Bold("Access:  ")+" no access token installed: fine when images come from your own registry; add one with --access-token\n")
	}
}

// EncodeAccessToken builds the token KubeSense issues from its two parts.
func EncodeAccessToken(id, secret string) string {
	return base64.StdEncoding.EncodeToString([]byte(id + ":" + secret))
}
