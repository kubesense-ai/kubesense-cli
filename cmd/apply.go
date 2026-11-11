package cmd

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"kubesense-cli/pkg/plan"

	"github.com/spf13/cobra"
)

var (
	applyKubeconfig, applyContext string
	applyYes                      bool
	applyTimeout                  time.Duration
)

var applyCmd = &cobra.Command{
	Use:   "apply <planfile>",
	Short: "Make the change a saved plan showed, if nothing it was planned from has changed",
	Long: `Applies a plan saved with "kubesense plan --out". The plan carries the
namespace, release, chart, values and access token, so apply takes no other
inputs.

Before anything is written, apply refuses if the cluster, the chart, the
release revision, the cluster's API versions or any live object the chart
reads differ from when the plan was made. A plan made with --strict is also
checked for exactly the rendered manifests it showed.

Apply never creates a namespace and never installs or removes anything the
plan did not show. After the upgrade it follows every workload of the release
until it is ready, and says which one is stuck and why. It exits 3 when the
change was applied but a workload is not ready in time.

Example:
  kubesense plan -n prod --chart ./charts/kubesense -f prod.yaml --out prod.plan
  kubesense apply prod.plan`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		p, err := plan.LoadPlan(args[0])
		if err != nil {
			return err
		}
		// Default to the context the plan was made with; the cluster is still
		// checked by API address and kube-system UID.
		ctxName := applyContext
		if ctxName == "" {
			ctxName = p.Context
		}
		target := plan.Target{Kubeconfig: applyKubeconfig, Context: ctxName}

		fmt.Printf("%s %s, made %s:\n", plan.Bold("Plan"), args[0], p.CreatedAt.Local().Format("2 Jan 15:04"))
		fmt.Printf("  %s release %s in namespace %s on %s\n", p.Action, plan.Bold(p.ReleaseName), plan.Bold(p.Namespace), clusterLabel(p))
		fmt.Printf("  chart %s-%s, %d resource changes, %d values changes\n", p.Chart.Name, p.Chart.Version, len(p.Changes), len(p.ValueChanges))
		if s := p.AccessToken; s != nil && s.Action != "unchanged" {
			verb := map[string]string{"install": "install", "upgrade": "rotate"}[s.Action]
			fmt.Printf("  first: %s\n", plan.Yellow(fmt.Sprintf("%s the access token (key %s)", verb, plan.MaskKeyID(s.KeyID()))))
		}
		if s := p.NFS; s != nil && s.Action == "install" {
			fmt.Printf("  first: %s\n", plan.Yellow(fmt.Sprintf("install the NFS server (release %s)", plan.NFSRelease)))
		}
		if !p.HasChanges() {
			fmt.Println("The plan has no changes; nothing to apply.")
			return nil
		}
		if !applyYes {
			fmt.Printf("\nType the namespace (%s) to apply: ", plan.Bold(p.Namespace))
			line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
			if strings.TrimSpace(line) != p.Namespace {
				return errors.New("not applied: the namespace did not match")
			}
		}

		ctx := context.Background()
		res, err := plan.Apply(ctx, p, plan.ApplyOptions{Target: target, Logf: debugf()})
		if err != nil {
			var r *plan.Refusal
			if errors.As(err, &r) {
				return fmt.Errorf("%w\nNothing was changed. Make a new plan: kubesense plan -n %s ...", err, p.Namespace)
			}
			return err
		}
		if res.AccessToken != "" {
			fmt.Printf("\n%s %s.\n", plan.Green("Applied:"), res.AccessToken)
		}
		if res.NFS != "" {
			fmt.Printf("%s %s.\n", plan.Green("Applied:"), res.NFS)
		}
		if res.Action == "none" {
			fmt.Printf("Release %s unchanged at revision %d.\n", p.ReleaseName, res.Revision)
			return nil
		}
		fmt.Printf("\n%s release %s is at revision %d (%s).\n", plan.Green("Applied:"), p.ReleaseName, res.Revision, res.Status)

		fmt.Printf("Waiting up to %s for the workloads to roll out:\n", applyTimeout)
		out, err := plan.WatchRollout(ctx, target, p.Namespace, p.ReleaseName, applyTimeout, func(w plan.WorkloadStatus) {
			line := plan.Dim("… " + w.String())
			if w.Done() {
				line = plan.Green("✓") + " " + w.String()
			} else if w.Problem != "" {
				line = plan.Red("! " + w.String())
			}
			fmt.Printf("  %s\n", line)
		})
		if err != nil {
			return err
		}
		if stuck := out.NotReady(); len(stuck) > 0 {
			fmt.Printf("\n%s\n", plan.Yellow(fmt.Sprintf("The upgrade was applied, but %d workload(s) are not ready after %s:", len(stuck), applyTimeout)))
			for _, w := range stuck {
				fmt.Printf("  %s\n", plan.Red(w.String()))
			}
			return &exitError{code: 3}
		}
		fmt.Printf("\n%s\n", plan.Green(fmt.Sprintf("All %d workloads are ready.", len(out.Workloads))))
		return nil
	},
}

func clusterLabel(p *plan.Plan) string {
	if p.Context != "" {
		return fmt.Sprintf("%s (%s)", p.Context, p.ServerURL)
	}
	return p.ServerURL
}

func init() {
	f := applyCmd.Flags()
	f.StringVar(&applyKubeconfig, "kubeconfig", "", "kubeconfig file (default: $KUBECONFIG, then ~/.kube/config)")
	f.StringVar(&applyContext, "context", "", "kubeconfig context (default: the one the plan was made with); it must reach the planned cluster")
	f.BoolVar(&applyYes, "yes", false, "apply without asking (for CI)")
	f.DurationVar(&applyTimeout, "timeout", 10*time.Minute, "how long to follow the rollout after applying")
}
