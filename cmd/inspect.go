package cmd

import (
	"context"
	"os"

	"kubesense-cli/pkg/plan"

	"github.com/spf13/cobra"
)

var (
	inspKubeconfig, inspContext, inspNamespace, inspRelease string
	historyRevision                                         int
)

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "What is deployed now: release, access token, and every workload's readiness",
	Long: `Shows the release's revision, status and chart, whether an access token is
installed, and each Deployment, StatefulSet and DaemonSet of the release with
why it is not ready (OOMKilled, CrashLoopBackOff, ImagePullBackOff, ...).
Read-only.

Example:
  kubesense status -n prod`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		s, err := plan.ReadStatus(context.Background(), plan.Target{Kubeconfig: inspKubeconfig, Context: inspContext}, inspNamespace, inspRelease)
		if err != nil {
			return err
		}
		s.Show(os.Stdout)
		return nil
	},
}

var historyCmd = &cobra.Command{
	Use:   "history",
	Short: "Every revision of the release: when, chart, and what changed in your values",
	Long: `Lists the release's revisions, newest first, with the chart, the status, how
many of your settings changed and every image tag change. --revision N shows
what revision N changed in your values, in full. Read-only.

Examples:
  kubesense history -n prod
  kubesense history -n prod --revision 14`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		revs, err := plan.ReadHistory(plan.Target{Kubeconfig: inspKubeconfig, Context: inspContext}, inspNamespace, inspRelease)
		if err != nil {
			return err
		}
		if historyRevision > 0 {
			return plan.ShowRevisionChanges(os.Stdout, revs, historyRevision)
		}
		plan.ShowHistory(os.Stdout, revs)
		return nil
	},
}

func init() {
	for _, c := range []*cobra.Command{statusCmd, historyCmd} {
		f := c.Flags()
		f.StringVar(&inspKubeconfig, "kubeconfig", "", "kubeconfig file (default: $KUBECONFIG, then ~/.kube/config)")
		f.StringVar(&inspContext, "context", "", "kubeconfig context (default: the current context)")
		f.StringVarP(&inspNamespace, "namespace", "n", "kubesense", "release namespace")
		f.StringVar(&inspRelease, "release", "kubesense", "Helm release name")
	}
	historyCmd.Flags().IntVar(&historyRevision, "revision", 0, "show what this revision changed in your values")
	rootCmd.AddCommand(statusCmd, historyCmd)
}
