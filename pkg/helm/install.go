package helm

import (
	"fmt"
	"io"
	"kubesense-cli/pkg/k8s"
	customPrompt "kubesense-cli/pkg/prompt"
	"kubesense-cli/types"
	"kubesense-cli/utils"
	"log"
	"net/http"
	"os"
	"path/filepath"

	"github.com/manifoldco/promptui"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v2"
	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/cli"
	helmRelease "helm.sh/helm/v3/pkg/release"
	"k8s.io/client-go/tools/clientcmd"
)

func InstallChart(cmd *cobra.Command, args []string) {
	kubeconfig := filepath.Join(os.Getenv("HOME"), ".kube", "config")
	config := clientcmd.GetConfigFromFileOrDie(kubeconfig)
	if config != nil {
		fmt.Println("Current k8s Context being used is", "\033[1m", config.CurrentContext, "\033[0m")
	}
	var installType string
	if len(args) > 0 {
		installType = args[0]
	}
	repoUrl := "https://helm.kubesense.ai/"
	resp, err := http.Get(repoUrl + "index.yaml")
	if err != nil {
		log.Fatal(err)
	}
	defer resp.Body.Close()

	// var helmRepoIndex map[string]interface{}
	var helmRepoIndex types.HelmRepoIndex
	// yamlFile, err := os.ReadFile("./index.yml")
	yamlFile, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Printf("yamlFile.Get err   #%v ", err)
	}
	err = yaml.Unmarshal(yamlFile, &helmRepoIndex)
	if err != nil {
		// log.Println((helmRepoIndex["entries"]).(map[interface{}]interface{})["kubesense"].([]interface{})[0].(map[interface{}]interface{})["urls"].([]interface{})[0])
		log.Fatalf("Unmarshal: %v", err)
	}
	chartIndex := helmRepoIndex.Entries.Kubesense[0]
	chartName := helmRepoIndex.Entries.Kubesense[0].Urls[0]
	releaseName := "kubesense"
	namespace := "kubesense"
	if installType == "server" {
		chartIndex = helmRepoIndex.Entries.Server[0]
		chartName = helmRepoIndex.Entries.Server[0].Urls[0]
		releaseName = "kubesense-server"
	}
	if installType == "sensor" {
		chartIndex = helmRepoIndex.Entries.Kubesensor[0]
		chartName = helmRepoIndex.Entries.Kubesensor[0].Urls[0]
		releaseName = "kubesensor"
	}

	// if true {
	// 	values["ignoreLogsNamespace"] = []string{
	// 		"amazon-cloudwatch",
	// 	}
	// }
	os.Setenv("HELM_NAMESPACE", namespace)
	var settings = cli.New()
	actionConfig := new(action.Configuration)
	if err := actionConfig.Init(settings.RESTClientGetter(), namespace, os.Getenv("HELM_DRIVER"), log.Printf); err != nil {
		log.Fatalf("Failed to initialize Helm configuration: %v", err)
	}
	isReleaseExists, release := CheckReleaseExists(actionConfig, releaseName)
	// if err != nil {
	// 	log.Fatalln("Error checking for releasename", err)
	// }
	if isReleaseExists {
		chartMetaData := release.(*helmRelease.Release).Chart.Metadata
		defaultValues, _ := utils.ReadValuesFromYamlFile(config.CurrentContext)
		prompt := promptui.Prompt{
			Label: "Current installed chart version is " + chartMetaData.Version + ", do you want to Upgrade Kubesense to " + chartIndex.Version + "?",
			Templates: &promptui.PromptTemplates{
				Prompt: "{{ . | orange }}",
			},
			IsConfirm: true,
		}
		confirm, _ := prompt.Run()

		if confirm == "y" {
			marshaledValues, _ := yaml.Marshal(defaultValues)
			fmt.Println(string(marshaledValues))
			prompt := promptui.Prompt{
				Label: "Yaml used for previous deployment, do you want to edit?",
				Templates: &promptui.PromptTemplates{
					Prompt: "{{ . | orange }}",
				},
				IsConfirm: true,
			}
			confirm, _ := prompt.Run()
			var values map[string]interface{}
			if confirm == "y" {
				values = customPrompt.GetUserPrompt(defaultValues)
			} else {
				values = map[string]interface{}{
					"cluster_name":      defaultValues.Global.ClusterName,
					"dashboardHostName": defaultValues.Global.DashboardHostName,
				}
				if len(defaultValues.Global.NodeAffinityLabelSelector) > 0 && defaultValues.Global.NodeAffinityLabelSelector[0] != nil && len(defaultValues.Global.NodeAffinityLabelSelector[0].MatchExpressions) > 0 {
					matchExpressionItem := defaultValues.Global.NodeAffinityLabelSelector[0].MatchExpressions[0]
					values["nodeAffinityLabelSelector"] = []map[string]interface{}{
						{
							"matchExpressions": []map[string]string{
								{
									"key":      matchExpressionItem.Key,
									"operator": matchExpressionItem.Operator,
									"values":   matchExpressionItem.Values,
								},
							},
						},
					}
				}
				if len(defaultValues.Global.Tolerations) > 0 && defaultValues.Global.Tolerations[0] != nil {
					tolerationItem := defaultValues.Global.Tolerations[0]
					values["tolerations"] = []map[string]string{
						{
							"key":      tolerationItem.Key,
							"operator": tolerationItem.Operator,
							"value":    tolerationItem.Value,
							"effect":   tolerationItem.Effect,
						},
					}
				}
				values = map[string]interface{}{
					"global": values,
				}
			}
			upgradeRelease(actionConfig, settings, namespace, releaseName, chartName, values)
			utils.WriteValuesToYamlFile(values, config.CurrentContext)
		} else {
			log.Println("Exited without upgrading kubesense")
		}
	} else {
		log.Println("Installation not found, doing a fresh installation")
		values := customPrompt.GetUserPrompt(types.ValuesStruct{})
		k8s.CreateNamespaceIfNotExists(namespace)
		installRelease(actionConfig, settings, namespace, releaseName, chartName, values)
		utils.WriteValuesToYamlFile(values, config.CurrentContext)
	}
}

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
