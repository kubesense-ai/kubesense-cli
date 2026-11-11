package cmd

import (
	"fmt"

	"kubesense-cli/pkg/plan"

	"github.com/spf13/cobra"
)

// Set at build time by GoReleaser.
var (
	commit = "none"
	date   = "unknown"
)

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print the CLI version and the Helm version it embeds",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Printf("kubesense %s (Helm %s)\ncommit %s, built %s\n", plan.Version, plan.HelmVersion(), commit, date)
	},
}
