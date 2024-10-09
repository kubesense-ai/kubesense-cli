package helm

import (
	"log"
	"os"

	"github.com/spf13/cobra"
	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/cli"
)

// uninstallRelease uninstalls a Helm release by its name
func UninstallChart(cmd *cobra.Command, args []string) {
	var installType string
	if len(args) > 0 {
		installType = args[0]
	}
	releaseName := "kubesense"
	namespace := "kubesense"
	os.Setenv("HELM_NAMESPACE", namespace)
	if installType == "server" {
		releaseName = "kubesense-server"
	}
	if installType == "sensor" {
		releaseName = "kubesensor"
	}
	var settings = cli.New()
	actionConfig := new(action.Configuration)
	if err := actionConfig.Init(settings.RESTClientGetter(), namespace, os.Getenv("HELM_DRIVER"), log.Printf); err != nil {
		log.Fatalf("Failed to initialize Helm configuration: %v", err)
	}
	// Create a new Helm uninstall action client
	client := action.NewUninstall(actionConfig)

	// Perform the uninstall action
	_, err := client.Run(releaseName)
	if err != nil {
		log.Fatalf("failed to uninstall release: %v", err)
	}
}
