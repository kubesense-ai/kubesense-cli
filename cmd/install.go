package cmd

import (
	"kubesense-cli/pkg/helm"
	"kubesense-cli/pkg/prompt"
	"kubesense-cli/types"

	"github.com/spf13/cobra"
)

var installCmd = &cobra.Command{
	Use:   "install [CHART]",
	Short: "Installs kubesense",
	Run: func(cmd *cobra.Command, args []string) {
		helm.InstallChart(cmd, args)
	},
}

func promptForInstallValues(defaultValues types.ValuesStruct) map[string]interface{} {
	return prompt.GetUserPrompt(defaultValues)
}
