package cmd

import (
	"kubesense-cli/pkg/helm"

	"github.com/spf13/cobra"
)

var installCmd = &cobra.Command{
	Use:   "install [CHART]",
	Short: "Installs kubesense",
	Run: func(cmd *cobra.Command, args []string) {
		helm.InstallChart(cmd, args)
	},
}
