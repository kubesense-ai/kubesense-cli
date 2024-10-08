package cmd

import (
	"kubesense-cli/pkg/helm"

	"github.com/spf13/cobra"
)

var configureCmd = &cobra.Command{
	Use:   "configure [ACCESS_TOKEN]",
	Short: "Installs kubesense",
	Run: func(cmd *cobra.Command, args []string) {
		helm.ConfigureKubesense(cmd, args)
	},
	Args: cobra.ExactArgs(1),
}
