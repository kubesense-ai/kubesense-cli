package prompt

import (
	"errors"
	"fmt"
	"kubesense-cli/types"
	"os"
	"regexp"
	"strings"

	"github.com/manifoldco/promptui"
)

func promptConfirm(pc types.PromptContent) bool {
	prompt := promptui.Prompt{
		Label: pc.Label,
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

func promptGetInput(pc types.PromptContent) string {
	validate := func(input string) error {
		if pc.Regex != "" {
			match, _ := regexp.MatchString(pc.Regex, input)
			if !match {
				return errors.New(pc.ErrorMsg)
			}
		} else {
			if len(input) <= 0 {
				return errors.New(pc.ErrorMsg)
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
		Label:     pc.Label,
		Templates: templates,
		Validate:  validate,
		Default:   pc.DefaultValue,
		AllowEdit: pc.AllowEdit,
	}

	result, err := prompt.Run()
	if err != nil {
		fmt.Printf("Prompt failed %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Input: %s\n", result)

	return result
}

func GetUserPrompt(defaultValues types.ValuesStruct) map[string]interface{} {
	defaultClusterName := ""
	defaultDashboardHostName := "kubesense.example-company.com"
	defaultNodeLabels := ""
	defaultTolerations := ""
	retrievedClusterName := defaultValues.Global.ClusterName
	if retrievedClusterName != "" {
		defaultClusterName = retrievedClusterName
	}
	retrievedDashboardHostName := defaultValues.Global.DashboardHostName
	if retrievedDashboardHostName != "" {
		defaultDashboardHostName = retrievedDashboardHostName
	}
	clusterName := promptGetInput(types.PromptContent{
		"Please provide a cluster name.",
		"Name of the cluster you're installing for?",
		defaultClusterName,
		true,
		"^([0-9]*[a-zA-Z_-]){3,}[0-9]*$",
	})

	dashboardHostName := promptGetInput(types.PromptContent{
		"Please provide a dashboard host name.",
		"What will be the url used for accessing kubesense-webapp?",
		defaultDashboardHostName,
		true,
		"^(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?.)+[a-z0-9][a-z0-9-]{0,61}[a-z0-9]$",
	})

	values := map[string]interface{}{
		"cluster_name":      clusterName,
		"dashboardHostName": dashboardHostName,
	}
	retrievedNodeAffinity := defaultValues.Global.NodeAffinityLabelSelector
	if len(retrievedNodeAffinity) > 0 && retrievedNodeAffinity[0] != nil && len(retrievedNodeAffinity[0].MatchExpressions) > 0 {
		retrievedNodeMatchExpression := retrievedNodeAffinity[0].MatchExpressions[0]
		defaultNodeLabels = retrievedNodeMatchExpression.Key + "=" + retrievedNodeMatchExpression.Values
	}
	if promptConfirm(types.PromptContent{"", "Do you have a specific node selector for kubesense?", "", true, ""}) {
		nodeLabels := promptGetInput(types.PromptContent{
			"Please provide a node labels.",
			"Provide node selector labels(Eg. key=value)",
			defaultNodeLabels,
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
	retrievedTolerations := defaultValues.Global.Tolerations
	if len(defaultValues.Global.Tolerations) > 0 && defaultValues.Global.Tolerations[0] != nil {
		retrievedTolerationsItem := retrievedTolerations[0]
		defaultTolerations = retrievedTolerationsItem.Key + "=" + retrievedTolerationsItem.Value + ":" + retrievedTolerationsItem.Effect
	}
	if promptConfirm(types.PromptContent{"", "Do you have a any tolerations to be added for server components?", "", true, ""}) {
		toleration := promptGetInput(types.PromptContent{
			"Please provide a valid toleration.",
			"Provide tolerations(Eg. key=value:NoSchedule)",
			defaultTolerations,
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
