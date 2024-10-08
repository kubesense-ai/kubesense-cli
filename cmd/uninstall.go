package cmd

import (
	"kubesense-cli/pkg/helm"

	"github.com/spf13/cobra"
)

var uninstallCmd = &cobra.Command{
	Use:   "uninstall [CHART]",
	Short: "Uninstall kubesense",
	Run: func(cmd *cobra.Command, args []string) {
		helm.UninstallChart(cmd, args)
	},
}
