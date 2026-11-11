package cmd

import (
	"context"
	"fmt"
	"os"

	"kubesense-cli/pkg/plan"

	"github.com/spf13/cobra"
)

var (
	pfKubeconfig, pfContext, pfNamespace, pfOutput string
)

var preflightCmd = &cobra.Command{
	Use:     "preflight",
	Aliases: []string{"check"},
	Short:   "Check a cluster has what KubeSense needs before you install",
	Long: `Checks the cluster before the first install, and reports how to fix what is
missing:

  Cluster    Kubernetes 1.21+, your permissions, CoreDNS, metrics-server, OpenShift
  Nodes      Ready nodes, x86-64 nodes for the server, capacity, taints, Fargate
  Storage    default StorageClass, volume expansion, its CSI driver, ReadWriteMany
  Cloud      AWS: EBS CSI driver, AWS Load Balancer Controller, EFS
             GCP: Persistent Disk CSI driver, Autopilot
             Azure: Azure Disk CSI driver, Azure Files
  Network    an ingress controller
  Namespace  pod security allows the logsensor's hostPath mount, existing releases

The cloud is detected from the nodes. Read-only. Exits 1 when a check fails.

Examples:
  kubesense preflight
  kubesense preflight -n observability --context prod-eks
  kubesense preflight -o json`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		r, err := plan.RunPreflight(context.Background(), plan.Target{Kubeconfig: pfKubeconfig, Context: pfContext}, pfNamespace)
		if err != nil {
			return err
		}
		switch pfOutput {
		case "json":
			if err := r.JSON(os.Stdout); err != nil {
				return err
			}
		case "", "text":
			r.Show(os.Stdout)
		default:
			return fmt.Errorf("--output must be text or json")
		}
		if r.Failed() {
			cmd.SilenceErrors = true
			cmd.SilenceUsage = true
			return &exitError{code: 1}
		}
		return nil
	},
}

func init() {
	f := preflightCmd.Flags()
	f.StringVar(&pfKubeconfig, "kubeconfig", "", "kubeconfig file (default: $KUBECONFIG, then ~/.kube/config)")
	f.StringVar(&pfContext, "context", "", "kubeconfig context (default: the current context)")
	f.StringVarP(&pfNamespace, "namespace", "n", "kubesense", "namespace KubeSense will be installed in")
	f.StringVarP(&pfOutput, "output", "o", "text", "text or json")
	rootCmd.AddCommand(preflightCmd)
}
