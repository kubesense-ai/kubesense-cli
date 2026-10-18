package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"kubesense-cli/pkg/plan"

	"github.com/spf13/cobra"
)

// SPIKE: plan / show / apply. The plan package refuses any API server that is
// not loopback, and --kubeconfig is mandatory (no fallback to ~/.kube/config).

var (
	paKubeconfig, paContext, paNamespace, paRelease, paChart, paValues, paOut string
	paTTL                                                                     time.Duration
)

var planCmd = &cobra.Command{
	Use:   "plan",
	Short: "[spike] Render against the live baseline and save an applyable plan (no cluster mutation)",
	RunE: func(cmd *cobra.Command, args []string) error {
		p, err := plan.MakePlan(context.Background(), plan.Options{
			Target:      plan.Target{Kubeconfig: paKubeconfig, Context: paContext},
			ReleaseName: paRelease, Namespace: paNamespace, ChartPath: paChart, OverridesFile: paValues,
			TTL: paTTL, Speculative: paOut == "",
		})
		if err != nil {
			return err
		}
		p.Show(os.Stdout)
		if paOut == "" {
			fmt.Println("\nSpeculative plan (no --out): it cannot be applied.")
			return nil
		}
		if err := p.Save(paOut); err != nil {
			return err
		}
		fmt.Printf("\nSaved plan to %s (contains overrides - treat as a secret). Apply with: kubesense apply %s --kubeconfig ...\n", paOut, paOut)
		return nil
	},
}

var showCmd = &cobra.Command{
	Use:   "show <planfile>",
	Short: "[spike] Display a saved plan exactly as saved",
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

var applyCmd = &cobra.Command{
	Use:   "apply <planfile>",
	Short: "[spike] Execute a saved plan only if it still matches at the execution boundary",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		p, err := plan.LoadPlan(args[0])
		if err != nil {
			return err
		}
		res, err := plan.Apply(context.Background(), p, plan.ApplyOptions{
			Target: plan.Target{Kubeconfig: paKubeconfig, Context: paContext},
		})
		var r *plan.Refusal
		if errors.As(err, &r) {
			return fmt.Errorf("%w\nNothing was written. Re-run `kubesense plan`", r)
		}
		if err != nil {
			return err
		}
		fmt.Printf("Applied: %s %s revision %d (%s); manifests %s match the plan.\n",
			res.Action, p.ReleaseName, res.Revision, res.Status, res.ManifestHash)
		fmt.Println("Helm accepted the release; rollout health is not implied - check Platform Health.")
		return nil
	},
}

func init() {
	for _, c := range []*cobra.Command{planCmd, applyCmd} {
		c.Flags().StringVar(&paKubeconfig, "kubeconfig", "", "explicit kubeconfig path (required)")
		c.Flags().StringVar(&paContext, "context", "", "context within --kubeconfig")
		_ = c.MarkFlagRequired("kubeconfig")
	}
	planCmd.Flags().StringVarP(&paNamespace, "namespace", "n", "kubesense", "release namespace")
	planCmd.Flags().StringVar(&paRelease, "release", "kubesense", "release name")
	planCmd.Flags().StringVar(&paChart, "chart", "", "local chart directory or archive (required)")
	planCmd.Flags().StringVarP(&paValues, "values", "f", "", "customer overrides file")
	planCmd.Flags().StringVar(&paOut, "out", "", "write an applyable plan file")
	planCmd.Flags().DurationVar(&paTTL, "ttl", time.Hour, "plan expiry")
	_ = planCmd.MarkFlagRequired("chart")
}
