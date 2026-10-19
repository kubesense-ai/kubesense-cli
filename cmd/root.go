package cmd

import (
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "kubesense",
	Short: "A customized CLI to install kubesense",
	// plan's exit code 2 is not a usage error.
	SilenceUsage:  true,
	SilenceErrors: true,
}

// exitError ends the process with a code and no message: `plan
// --detailed-exitcode` exits 2 when there are changes.
type exitError struct{ code int }

func (e *exitError) Error() string { return fmt.Sprintf("exit status %d", e.code) }

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		var ee *exitError
		if errors.As(err, &ee) {
			os.Exit(ee.code)
		}
		fmt.Println(err)
		os.Exit(1)
	}
}

func init() {
	rootCmd.AddCommand(installCmd)
	rootCmd.AddCommand(uninstallCmd)
	rootCmd.AddCommand(configureCmd)
	rootCmd.AddCommand(planCmd)
	rootCmd.AddCommand(showCmd)
	rootCmd.AddCommand(valuesCmd)
	rootCmd.AddCommand(versionCmd)
}
