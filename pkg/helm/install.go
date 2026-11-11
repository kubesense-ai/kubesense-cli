package helm

import (
	"fmt"
	"log"

	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/cli"
)

func installRelease(actionConfig *action.Configuration, settings *cli.EnvSettings, namespace string, releaseName string, chartName string, values map[string]interface{}) {
	client := action.NewInstall(actionConfig)
	client.Namespace = namespace
	client.ReleaseName = releaseName

	// Load chart
	chartPath, err := client.ChartPathOptions.LocateChart(chartName, settings)
	if err != nil {
		log.Fatalf("Failed to locate chart: %v", err)
	}

	chart, err := loader.Load(chartPath)
	if err != nil {
		log.Fatalf("Failed to load chart: %v", err)
	}

	// Install the chart
	_, err = client.Run(chart, values)
	if err != nil {
		log.Fatalf("Failed to install chart: %v", err)
	}

	fmt.Printf("Chart %s installed successfully\n", chartName)
}
