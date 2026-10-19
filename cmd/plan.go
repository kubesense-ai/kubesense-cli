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

Examples:
  kubesense plan --chart kubesense-server --version 2.3.9 -f overrides.yaml
  kubesense plan --chart ./charts/kubesense-server -f base.yaml -f prod.yaml --out upgrade.plan`,
	RunE: func(cmd *cobra.Command, args []string) error {
		p, err := plan.MakePlan(context.Background(), plan.Options{
			Target:      plan.Target{Kubeconfig: planKubeconfig, Context: planContext},
			ReleaseName: planRelease, Namespace: planNamespace,
			ChartPath: planChart, ChartVersion: planChartVersion, ChartRepo: planChartRepo,
			OverridesFiles: planValues,
			TTL:            planTTL,
			Speculative:    planOut == "",
		})
		if err != nil {
			return err
		}
		p.Show(os.Stdout)
		if planOut != "" {
			if err := p.Save(planOut); err != nil {
				return err
			}
			fmt.Printf("\nSaved the plan to %s. It contains your overrides, including any credentials: it is readable only by you, keep it out of Git. Review it with: kubesense show %s\n", planOut, planOut)
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
	f.BoolVar(&planDetailedExitCode, "detailed-exitcode", false, "exit 0 with no changes, 2 with changes, 1 on error")
	_ = planCmd.MarkFlagRequired("chart")
}
