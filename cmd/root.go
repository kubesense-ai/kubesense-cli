package cmd

import (
	"bytes"
	"errors"
	"fmt"
	"log"
	"os"

	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "kubesense",
	Short: "Plan and apply KubeSense upgrades: review every change before it reaches the cluster",
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

// quietLog drops Helm's chart-loader notes about the meta chart's symlinked
// subcharts (one line per subchart, on every plan) and passes the rest on.
type quietLog struct{}

func (quietLog) Write(b []byte) (int, error) {
	if bytes.Contains(b, []byte("found symbolic link in path")) {
		return len(b), nil
	}
	return os.Stderr.Write(b)
}

func init() {
	log.SetOutput(quietLog{})
	rootCmd.AddCommand(configureCmd)
	rootCmd.AddCommand(planCmd)
	rootCmd.AddCommand(showCmd)
	rootCmd.AddCommand(applyCmd)
	rootCmd.AddCommand(valuesCmd)
	rootCmd.AddCommand(versionCmd)
}
