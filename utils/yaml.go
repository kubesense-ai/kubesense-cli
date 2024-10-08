package utils

import (
	"encoding/base64"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"kubesense-cli/types"

	"gopkg.in/yaml.v2"
)

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

func WriteValuesToYamlFile(values map[string]interface{}, currentContext string) {
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

func ReadValuesFromYamlFile(currentContext string) (types.ValuesStruct, error) {
	homedir, _ := os.UserHomeDir()
	kubesensePath := filepath.Join(homedir, ".kubesense")
	valuesYamlFilePath := filepath.Join(kubesensePath, currentContext+"-"+"values.yaml")
	valuesYaml, err := os.ReadFile(valuesYamlFilePath)
	if err != nil {
		return types.ValuesStruct{}, fmt.Errorf("ERROR reading the file %v", err)
	}
	var values types.ValuesStruct
	err = yaml.Unmarshal(valuesYaml, &values)
	if err != nil {
		fmt.Println(values, err)
		return types.ValuesStruct{}, fmt.Errorf("ERROR unmarshaling yaml %v", err)
	}

	return values, nil
}

func Base64Decode(str string) (string, bool) {
	data, err := base64.StdEncoding.DecodeString(str)
	if err != nil {
		return "", true
	}
	return string(data), false
}
