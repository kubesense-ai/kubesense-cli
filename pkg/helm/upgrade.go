package helm

import (
	"fmt"
	"log"

	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/cli"
)

func upgradeRelease(actionConfig *action.Configuration, settings *cli.EnvSettings, namespace string, releaseName string, chartName string, values map[string]interface{}) {
	client := action.NewUpgrade(actionConfig)
	client.Namespace = namespace

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
	_, err = client.Run(releaseName, chart, values)
	if err != nil {
		log.Fatalf("Failed to upgrade chart: %v", err)
	}

	fmt.Printf("Chart %s upgraded successfully\n", chartName)
}
