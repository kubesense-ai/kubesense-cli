package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"helm.sh/helm/v3/pkg/action"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"sigs.k8s.io/yaml"
)

var (
	valuesKubeconfig, valuesContext, valuesNamespace, valuesRelease, valuesOut string
)

var valuesCmd = &cobra.Command{
	Use:   "values",
	Short: "Work with the values of an installed release",
}

var valuesPullCmd = &cobra.Command{
	Use:   "pull",
	Short: "Write the overrides the release was installed with: the starting point for a change",
	Long: `Writes the values you supplied to the last Helm install or upgrade (what
"helm get values" prints), not the chart's merged defaults. Edit these and plan
with them; upgrading from merged values would pin the old chart's defaults.

They can contain credentials: the file is readable only by you.

For Argo CD or Flux installs, your values live in Git; use those instead.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		flags := genericclioptions.NewConfigFlags(false)
		if valuesKubeconfig != "" {
			flags.KubeConfig = &valuesKubeconfig
		}
		if valuesContext != "" {
			flags.Context = &valuesContext
		}
		flags.Namespace = &valuesNamespace
		cfg := new(action.Configuration)
		if err := cfg.Init(flags, valuesNamespace, "secret", func(string, ...interface{}) {}); err != nil {
			return err
		}
		vals, err := action.NewGetValues(cfg).Run(valuesRelease)
		if err != nil {
			return fmt.Errorf("release %s in %s: %w", valuesRelease, valuesNamespace, err)
		}
		if vals == nil {
			vals = map[string]interface{}{}
		}
		out, err := yaml.Marshal(vals)
		if err != nil {
			return err
		}
		if valuesOut == "" || valuesOut == "-" {
			_, err = os.Stdout.Write(out)
			return err
		}
		if err := os.WriteFile(valuesOut, out, 0o600); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "Wrote the overrides of release %s to %s (readable only by you).\n", valuesRelease, valuesOut)
		return nil
	},
}

func init() {
	f := valuesPullCmd.Flags()
	f.StringVar(&valuesKubeconfig, "kubeconfig", "", "kubeconfig file (default: $KUBECONFIG, then ~/.kube/config)")
	f.StringVar(&valuesContext, "context", "", "kubeconfig context (default: the current context)")
	f.StringVarP(&valuesNamespace, "namespace", "n", "kubesense", "release namespace")
	f.StringVar(&valuesRelease, "release", "kubesense", "Helm release name")
	f.StringVarP(&valuesOut, "output", "o", "", "file to write (default: stdout)")
	valuesCmd.AddCommand(valuesPullCmd)
}
