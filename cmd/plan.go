package cmd

import (
	"context"
	"fmt"
	"os"
	"time"

	"kubesense-cli/pkg/plan"

	"github.com/spf13/cobra"
)

// Default chart repository: the one `kubesense install` already uses.
const defaultChartRepo = "https://helm.kubesense.ai/"

var (
	planKubeconfig, planContext, planNamespace, planRelease string
	planChart, planChartVersion, planChartRepo, planOut     string
	planValues                                              []string
	planStrict                                              bool
	planAccessTokenFile, planAccessTokenChart               string
	planNFS                                                 bool
	planNFSChart                                            string
	planAskAccessToken                                      bool
	planTTL                                                 time.Duration
	planDetailedExitCode                                    bool
)

var planCmd = &cobra.Command{
	Use:   "plan",
	Short: "Show what installing or upgrading KubeSense would change, without changing anything",
	Long: `Renders the chart with your values against the live cluster, the way the
real install or upgrade would (a Helm server-side dry run), and shows the
difference from what is deployed now: values, then every resource to add,
change or remove.

Nothing in the cluster is written: the connection refuses every write.
Credentials are masked in the output.

Start from your overrides, not from effective values:
  kubesense values pull -n kubesense --release kubesense -o overrides.yaml

The access token (image pulls from KubeSense's registry) is optional. With
--access-token, plan asks for the access key ID and secret (the secret is not
shown), or reads $KUBESENSE_ACCESS_KEY_ID and $KUBESENSE_ACCESS_KEY_SECRET.
--access-token-file takes the issued base64 token instead. Without either,
the plan only reports whether one is installed.

Dynamic log parsing and alert config need a ReadWriteMany volume. Without
EFS, Azure Files or Filestore, --nfs installs the bundled NFS server (release
kubesense-nfs) before KubeSense and sets its IP in your values.

Examples:
  kubesense plan --chart kubesense-server --version 2.3.9 -f overrides.yaml
  kubesense plan --chart ./charts/kubesense-server -f base.yaml -f prod.yaml --out upgrade.plan
  kubesense plan --chart kubesense -f overrides.yaml --access-token --out install.plan`,
	RunE: func(cmd *cobra.Command, args []string) error {
		var token string
		if planAccessTokenFile != "" && planAskAccessToken {
			return fmt.Errorf("give --access-token or --access-token-file, not both")
		}
		if planAskAccessToken {
			id, secret, err := askAccessKey()
			if err != nil {
				return fmt.Errorf("access token: %w", err)
			}
			token = plan.EncodeAccessToken(id, secret)
		}
		if planAccessTokenFile != "" {
			t, err := plan.ReadAccessToken(planAccessTokenFile, os.Stdin)
			if err != nil {
				return fmt.Errorf("access token: %w", err)
			}
			token = t
		}
		p, err := plan.MakePlan(context.Background(), plan.Options{
			Target:      plan.Target{Kubeconfig: planKubeconfig, Context: planContext},
			ReleaseName: planRelease, Namespace: planNamespace,
			ChartPath: planChart, ChartVersion: planChartVersion, ChartRepo: planChartRepo,
			OverridesFiles: planValues,
			TTL:            planTTL,
			Speculative:    planOut == "",
			Strict:         planStrict,
			Logf:           debugf(),
			AccessToken:    token, AccessTokenChart: planAccessTokenChart,
			NFS: planNFS, NFSChart: planNFSChart,
		})
		if err != nil {
			return err
		}
		p.Show(os.Stdout)
		if planOut != "" {
			if err := p.Save(planOut); err != nil {
				return err
			}
			fmt.Printf("\nSaved the plan to %s. It contains your overrides, including any credentials: it is readable only by you, keep it out of Git.\nReview it with: kubesense show %s\nApply it with:  kubesense apply %s\n", planOut, planOut, planOut)
		}
		if planDetailedExitCode && p.HasChanges() {
			return &exitError{code: 2}
		}
		return nil
	},
}

var showCmd = &cobra.Command{
	Use:   "show <planfile>",
	Short: "Display a saved plan exactly as it was made",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		p, err := plan.LoadPlan(args[0])
		if err != nil {
			return err
		}
		p.Show(os.Stdout)
		return nil
	},
}

func init() {
	f := planCmd.Flags()
	f.StringVar(&planKubeconfig, "kubeconfig", "", "kubeconfig file (default: $KUBECONFIG, then ~/.kube/config)")
	f.StringVar(&planContext, "context", "", "kubeconfig context (default: the current context)")
	f.StringVarP(&planNamespace, "namespace", "n", "kubesense", "release namespace")
	f.StringVar(&planRelease, "release", "kubesense", "Helm release name")
	f.StringVar(&planChart, "chart", "", "chart: a local directory or archive, or a chart name in --repo (kubesense, kubesense-server, kubesensor)")
	f.StringVar(&planChartVersion, "version", "", "chart version to fetch from --repo (default: latest)")
	f.StringVar(&planChartRepo, "repo", defaultChartRepo, "chart repository for --chart names")
	f.StringArrayVarP(&planValues, "values", "f", nil, "values file with your overrides; repeat to merge, later files win")
	f.StringVar(&planOut, "out", "", "save the plan to this file (readable only by you)")
	f.DurationVar(&planTTL, "ttl", time.Hour, "how long a saved plan stays valid")
	f.BoolVar(&planStrict, "strict", false, "refuse charts that generate secrets, certificates or read the cluster, so apply can check the exact render (the KubeSense chart does all three)")
	f.StringVar(&planAccessTokenFile, "access-token-file", "", "install or rotate the image-pull access token from this file (\"-\" reads stdin); not needed when images come from your own registry")
	f.BoolVar(&planAskAccessToken, "access-token", false, "install or rotate the access token: asks for the access key ID and secret (or reads $KUBESENSE_ACCESS_KEY_ID and $KUBESENSE_ACCESS_KEY_SECRET)")
	f.BoolVar(&planNFS, "nfs", false, "install the bundled NFS server if absent, and point global.dynamicLogParsing / global.alertconfig (whichever you enabled) at it; not needed with EFS, Azure Files or Filestore")
	f.StringVar(&planNFSChart, "nfs-chart", "", "NFS server chart: a local path, or a name in --repo (default: nfs)")
	f.StringVar(&planAccessTokenChart, "access-token-chart", "", "access-token chart: a local path, or a name in --repo (default: access-token)")
	f.BoolVar(&planDetailedExitCode, "detailed-exitcode", false, "exit 0 with no changes, 2 with changes, 1 on error")
	_ = planCmd.MarkFlagRequired("chart")
}
