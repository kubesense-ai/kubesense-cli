package cmd

import (
	"fmt"

	"kubesense-cli/pkg/plan"

	"github.com/spf13/cobra"
)

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print the CLI version and the Helm version it embeds",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Printf("kubesense %s (Helm %s)\n", plan.Version, plan.HelmVersion())
	},
}
