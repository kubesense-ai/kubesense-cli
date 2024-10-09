package types

type PromptContent struct {
	ErrorMsg     string
	Label        string
	DefaultValue string
	AllowEdit    bool
	Regex        string
}

type Entry struct {
	AppVersion string   `yaml:"appVersion"`
	Version    string   `yaml:"version"`
	Urls       []string `yaml:"urls"`
}

type TypeEntries struct {
	Kubesense   []Entry `yaml:"kubesense"`
	Kubesensor  []Entry `yaml:"kubesensor"`
	Server      []Entry `yaml:"kubesense-server"`
	AccessToken []Entry `yaml:"access-token"`
}
type HelmRepoIndex struct {
	Entries TypeEntries `yaml:"entries"`
}

type MatchExpressionsStruct struct {
	Key      string `yaml:"key"`
	Values   string `yaml:"values"`
	Operator string `yaml:"operator"`
}
type LabelSelectorStruct struct {
	MatchExpressions []MatchExpressionsStruct `yaml:"matchExpressions"`
}
type TolerationsStruct struct {
	Key      string `yaml:"key"`
	Operator string `yaml:"operator"`
	Value    string `yaml:"value"`
	Effect   string `yaml:"effect"`
}
type GlobalValuesStruct struct {
	ClusterName               string                 `yaml:"cluster_name"`
	DashboardHostName         string                 `yaml:"dashboardHostName"`
	NodeAffinityLabelSelector []*LabelSelectorStruct `yaml:"nodeAffinityLabelSelector"`
	Tolerations               []*TolerationsStruct   `yaml:"tolerations"`
}
type ValuesStruct struct {
	Global GlobalValuesStruct `yaml:"global"`
}
