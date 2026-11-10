package cmd

import (
	"bytes"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"kubesense-cli/pkg/plan"

	"github.com/spf13/cobra"
	"golang.org/x/term"
)

var debug, noColor bool

// debugf prints what Helm is doing (rendering, each resource it creates,
// patches or waits for) when --debug is set; nil otherwise, which Helm treats
// as silent.
func debugf() func(string, ...interface{}) {
	if !debug {
		return nil
	}
	start := time.Now()
	return func(format string, args ...interface{}) {
		msg := strings.TrimRight(fmt.Sprintf(format, args...), "\n")
		fmt.Fprintf(os.Stderr, "%s %s\n", plan.Dim(fmt.Sprintf("[helm %6.1fs]", time.Since(start).Seconds())), msg)
	}
}

var rootCmd = &cobra.Command{
	Use:   "kubesense",
	Short: "Plan and apply KubeSense upgrades: review every change before it reaches the cluster",
	// plan's exit code 2 is not a usage error.
	SilenceUsage:  true,
	SilenceErrors: true,
	PersistentPreRun: func(cmd *cobra.Command, args []string) {
		plan.Color = !noColor && os.Getenv("NO_COLOR") == "" && term.IsTerminal(int(os.Stdout.Fd()))
	},
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
		fmt.Fprintln(os.Stderr, plan.Red("Error: ")+err.Error())
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
	rootCmd.PersistentFlags().BoolVar(&debug, "debug", false, "print what Helm does: rendering, and every resource it creates, updates or waits for")
	rootCmd.PersistentFlags().BoolVar(&noColor, "no-color", false, "plain output (also: NO_COLOR=1; colour is off when output is not a terminal)")
	rootCmd.AddCommand(configureCmd)
	rootCmd.AddCommand(planCmd)
	rootCmd.AddCommand(showCmd)
	rootCmd.AddCommand(applyCmd)
	rootCmd.AddCommand(valuesCmd)
	rootCmd.AddCommand(versionCmd)
}
