package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

var (
	// Set these variables during build time
	version = "v1.0.0"
	commit  = "none"
	date    = "unknown"
	builtBy = "unknown"
)

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print the version number of the CLI",
	Long:  `All software has versions. This is the CLI's`,
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Printf("Version: %s\nCommit: %s\nDate: %s\n", version, commit, date)
	},
}
