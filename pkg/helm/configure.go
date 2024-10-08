package helm

import (
	"fmt"
	"io"
	"kubesense-cli/pkg/k8s"
	"kubesense-cli/types"
	"kubesense-cli/utils"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v2"
	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/cli"
	"k8s.io/client-go/tools/clientcmd"
)

func ConfigureKubesense(cmd *cobra.Command, args []string) {
	accessToken := args[0]
	kubeconfig := filepath.Join(os.Getenv("HOME"), ".kube", "config")
	config := clientcmd.GetConfigFromFileOrDie(kubeconfig)
	if config != nil {
		fmt.Println("Current k8s Context being used is", "\033[1m", config.CurrentContext, "\033[0m")
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
	// chartIndex := helmRepoIndex.Entries.AccessToken[0]
	chartName := helmRepoIndex.Entries.AccessToken[0].Urls[0]
	releaseName := "access-token"
	namespace := "kubesense"

	os.Setenv("HELM_NAMESPACE", namespace)
	var settings = cli.New()
	actionConfig := new(action.Configuration)
	if err := actionConfig.Init(settings.RESTClientGetter(), namespace, os.Getenv("HELM_DRIVER"), log.Printf); err != nil {
		log.Fatalf("Failed to initialize Helm configuration: %v", err)
	}
	isReleaseExists, _ := CheckReleaseExists(actionConfig, releaseName)
	// if err != nil {
	// 	log.Fatalln("Error checking for releasename", err)
	// }
	accessToken, _ = utils.Base64Decode(accessToken)
	idAndSecret := strings.SplitN(accessToken, ":", 2)
	values := map[string]interface{}{
		"KEY_ID":     idAndSecret[0],
		"KEY_SECRET": idAndSecret[1],
	}

	if isReleaseExists {
		// chartMetaData := release.(*helmRelease.Release).Chart.Metadata
		upgradeRelease(actionConfig, settings, namespace, releaseName, chartName, values)
	} else {
		log.Println("Creating access-token in " + namespace + " namespace")
		k8s.CreateNamespaceIfNotExists(namespace)
		installRelease(actionConfig, settings, namespace, releaseName, chartName, values)
	}
}
