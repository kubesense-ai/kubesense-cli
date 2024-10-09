package helm

import "helm.sh/helm/v3/pkg/action"

func CheckReleaseExists(actionConfig *action.Configuration, releaseName string) (bool, interface{}) {
	client := action.NewGet(actionConfig)
	release, err := client.Run(releaseName)
	if err != nil {
		return false, err
	}
	return true, release
}
