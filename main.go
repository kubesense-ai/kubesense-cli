package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/manifoldco/promptui"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v2"
	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/cli"
	helmRelease "helm.sh/helm/v3/pkg/release"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

type promptContent struct {
	errorMsg     string
	label        string
	defaultValue string
	allowEdit    bool
	regex        string
}

type Entry struct {
	AppVersion string   `yaml:"appVersion"`
	Version    string   `yaml:"version"`
	Urls       []string `yaml:"urls"`
}

type TypeEntries struct {
	Kubesense  []Entry `yaml:"kubesense"`
	Kubesensor []Entry `yaml:"kubesensor"`
	Server     []Entry `yaml:"kubesense-server"`
}
type HelmRepoIndex struct {
	Entries TypeEntries `yaml:"entries"`
}

// Initialize the CLI environment

func main() {
	rootCmd := &cobra.Command{
		Use:   "helm-cli",
		Short: "A custom CLI to install Helm charts from a chart repository",
	}

	installCmd := &cobra.Command{
		Use:   "install [CHART]",
		Short: "Installs kubesense",
		Run:   installChart,
	}

	rootCmd.AddCommand(installCmd)

	uninstallCmd := &cobra.Command{
		Use:   "uninstall [CHART]",
		Short: "Uninstall kubesense",
		Run:   uninstallChart,
	}
	rootCmd.AddCommand(uninstallCmd)
	if err := rootCmd.Execute(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}

func promptConfirm(pc promptContent) bool {
	prompt := promptui.Prompt{
		Label: pc.label,
		Templates: &promptui.PromptTemplates{
			Prompt: "{{ . | green }}",
		},
		IsConfirm: true,
	}
	confirm, _ := prompt.Run()
	if confirm == "y" {
		return true
	} else {
		return false
	}
}

func promptGetInput(pc promptContent) string {
	validate := func(input string) error {
		if pc.regex != "" {
			match, _ := regexp.MatchString(pc.regex, input)
			if !match {
				return errors.New(pc.errorMsg)
			}
		} else {
			if len(input) <= 0 {
				return errors.New(pc.errorMsg)
			}
		}
		return nil
	}

	templates := &promptui.PromptTemplates{
		Prompt:  "{{ . }} ",
		Valid:   "{{ . | green }} ",
		Invalid: "{{ . | red }} ",
		Success: "{{ . | bold }} ",
	}

	prompt := promptui.Prompt{
		Label:     pc.label,
		Templates: templates,
		Validate:  validate,
		Default:   pc.defaultValue,
		AllowEdit: pc.allowEdit,
	}

	result, err := prompt.Run()
	if err != nil {
		fmt.Printf("Prompt failed %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Input: %s\n", result)

	return result
}
func ifFolderExists(path string) (bool, error) {
	_, err := os.Stat(path)
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}
func getUserPrompt() map[string]interface{} {
	clusterName := promptGetInput(promptContent{
		"Please provide a cluster name.",
		"Name of the cluster you're installing for?",
		"",
		true,
		"^([0-9]*[a-zA-Z_-]){3,}[0-9]*$",
	})

	dashboardHostName := promptGetInput(promptContent{
		"Please provide a dashboard host name.",
		"What namespace would you like to install for?",
		"kubesense.example-company.com",
		true,
		"^(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?.)+[a-z0-9][a-z0-9-]{0,61}[a-z0-9]$",
	})

	values := map[string]interface{}{
		"cluster_name":      clusterName,
		"dashboardHostName": dashboardHostName,
	}
	if promptConfirm(promptContent{"", "Do you have a specific node selector for kubesense?", "", true, ""}) {
		nodeLabels := promptGetInput(promptContent{
			"Please provide a node labels.",
			"Provide node selector labels(Eg. key=value)",
			"",
			true,
			"^([a-zA-Z0-9_-]+)=([a-zA-Z0-9_-]+)$",
		})
		nodeKeyAndLabel := strings.SplitN(nodeLabels, "=", 2)
		values["nodeAffinityLabelSelector"] = []map[string]interface{}{
			{
				"matchExpressions": []map[string]string{
					{
						"key":      nodeKeyAndLabel[0],
						"operator": "In",
						"values":   nodeKeyAndLabel[1],
					},
				},
			},
		}
	}

	if promptConfirm(promptContent{"", "Do you have a any tolerations to be added for server components?", "", true, ""}) {
		toleration := promptGetInput(promptContent{
			"Please provide a valid toleration.",
			"Provide tolerations(Eg. key=value:NoSchedule)",
			"",
			true,
			"^([a-zA-Z0-9_-]+)=([a-zA-Z0-9_-]+):(NoSchedule|NoExecute|PreferNoSchedule)+$",
		})
		keyAndLabel := strings.SplitN(toleration, "=", 2)
		effect := strings.SplitN(keyAndLabel[1], ":", 2)
		values["tolerations"] = []map[string]string{
			{
				"key":      keyAndLabel[0],
				"operator": "Equal",
				"value":    effect[0],
				"effect":   effect[1],
			},
		}
	}
	values = map[string]interface{}{
		"global": values,
	}
	return values
}
func writeValuesToYamlFile(values map[string]interface{}, currentContext string) {
	valuesYaml, _ := yaml.Marshal(values)
	homedir, _ := os.UserHomeDir()
	kubesensePath := filepath.Join(homedir, ".kubesense")
	exists, _ := ifFolderExists(kubesensePath)
	if !exists {
		log.Println("creating folder")
		os.Mkdir(kubesensePath, 0755)
	}
	valuesYamlFilePath := filepath.Join(kubesensePath, currentContext+"-"+"values.yaml")
	err := os.WriteFile(valuesYamlFilePath, valuesYaml, 0644)
	if err != nil {
		log.Fatalf("error writing YAML file: %v", err)
	}
	fmt.Println("Values YAML file used for kubesense deployment is written successfully! to path", valuesYamlFilePath)
}

//	func readValuesFromYamlFile(currentContext string) map[string]interface{} {
//		homedir, _ := os.UserHomeDir()
//		kubesensePath := filepath.Join(homedir, ".kubesense")
//		valuesYamlFilePath := filepath.Join(kubesensePath, currentContext+"-"+"values.yaml")
//		valuesYaml, err := os.ReadFile(valuesYamlFilePath)
//		var values map[string]interface{}
//		err = yaml.Unmarshal(valuesYaml, values)
//		return values
//	}
func installChart(cmd *cobra.Command, args []string) {
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
	var helmRepoIndex HelmRepoIndex
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
	chartName := repoUrl + helmRepoIndex.Entries.Kubesense[0].Urls[0]
	releaseName := "kubesense"
	namespace := "kubesense"
	if installType == "server" {
		chartIndex = helmRepoIndex.Entries.Server[0]
		chartName = repoUrl + helmRepoIndex.Entries.Server[0].Urls[0]
		releaseName = "kubesense-server"
	}
	if installType == "sensor" {
		chartIndex = helmRepoIndex.Entries.Kubesensor[0]
		chartName = repoUrl + helmRepoIndex.Entries.Kubesensor[0].Urls[0]
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
	isReleaseExists, release := checkReleaseExists(actionConfig, releaseName)
	// if err != nil {
	// 	log.Fatalln("Error checking for releasename", err)
	// }
	if isReleaseExists {
		chartMetaData := release.(*helmRelease.Release).Chart.Metadata
		prompt := promptui.Prompt{
			Label: "Current installed chart version is " + chartMetaData.Version + ", do you want to Upgrade Kubesense to " + chartIndex.Version + "?",
			Templates: &promptui.PromptTemplates{
				Prompt: "{{ . | orange }}",
			},
			IsConfirm: true,
		}
		confirm, _ := prompt.Run()
		if confirm == "y" {
			values := getUserPrompt()
			upgradeRelease(actionConfig, settings, namespace, releaseName, chartName, values)
			writeValuesToYamlFile(values, config.CurrentContext)
		} else {
			log.Println("Exited without upgrading kubesense")
		}
	} else {
		log.Println("Installation not found, doing a fresh installation")
		values := getUserPrompt()
		createNamespaceIfNotExists(namespace)
		installRelease(actionConfig, settings, namespace, releaseName, chartName, values)
	}
}

func checkReleaseExists(actionConfig *action.Configuration, releaseName string) (bool, interface{}) {
	client := action.NewGet(actionConfig)
	release, err := client.Run(releaseName)
	if err != nil {
		return false, err
	}
	return true, release
}

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

func createNamespaceIfNotExists(namespace string) error {
	// Use the kubeconfig to create a Kubernetes client
	kubeconfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		clientcmd.NewDefaultClientConfigLoadingRules(),
		&clientcmd.ConfigOverrides{},
	)
	config, err := kubeconfig.ClientConfig()
	if err != nil {
		return fmt.Errorf("failed to get Kubernetes client config: %v", err)
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return fmt.Errorf("failed to create Kubernetes clientset: %v", err)
	}

	// Check if the namespace exists
	_, err = clientset.CoreV1().Namespaces().Get(context.TODO(), namespace, metav1.GetOptions{})
	if err == nil {
		fmt.Printf("Namespace '%s' already exists\n", namespace)
		return nil
	}

	// If namespace doesn't exist, create it
	fmt.Printf("Creating namespace '%s'\n", namespace)
	ns := &v1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: namespace,
		},
	}
	_, err = clientset.CoreV1().Namespaces().Create(context.TODO(), ns, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("failed to create namespace: %v", err)
	}

	return nil
}

// uninstallRelease uninstalls a Helm release by its name
func uninstallChart(cmd *cobra.Command, args []string) {
	var installType string
	if len(args) > 0 {
		installType = args[0]
	}
	releaseName := "kubesense"
	namespace := "kubesense"
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
