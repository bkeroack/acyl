package models

import (
	"bytes"
	"fmt"
	"strings"
	"text/template"
	"time"

	"github.com/lib/pq"

	nitroerrors "github.com/bkeroack/acyl/pkg/nitro/errors"
	"github.com/pkg/errors"
	"golang.org/x/crypto/sha3"
)

// RepoConfigDependency models a dependency repo for an environment
type RepoConfigDependency struct {
	Name               string                `yaml:"name" json:"name"`                       // Unique name for the dependency
	Repo               string                `yaml:"repo" json:"repo"`                       // Repo indicates a GitHub repository which contains a top-level acyl.yml
	ChartPath          string                `yaml:"chart_path" json:"chart_path"`           // Path to the chart within the triggering repository
	ChartRepoPath      string                `yaml:"chart_repo_path" json:"chart_repo_path"` // GitHub repo and path to chart (no acyl.yml)
	ChartVarsPath      string                `yaml:"chart_vars_path" json:"chart_vars_path"`
	ChartVarsRepoPath  string                `yaml:"chart_vars_repo_path" json:"chart_vars_repo_path"`
	DisableBranchMatch bool                  `yaml:"disable_branch_match" json:"disable_branch_match"`
	DefaultBranch      string                `yaml:"default_branch" json:"default_branch"`
	Requires           []string              `yaml:"requires" json:"requires"`
	ValueOverrides     []string              `yaml:"value_overrides" json:"value_overrides"`
	AppMetadata        RepoConfigAppMetadata `yaml:"-" json:"app_metadata"` // set by nitro
	Parent             string                `yaml:"-" json:"-"`            // Name of the parent dependency if this is a transitive dep, set by nitro
}

// BranchMatchable indicates whether the depencency can participate in branch matching and can be found in RefMap
func (rcd RepoConfigDependency) BranchMatchable() bool {
	return rcd.Repo != "" && rcd.ChartPath == "" && rcd.ChartRepoPath == ""
}

func truncateString(s string, n uint) string {
	if n == 0 {
		return ""
	}
	rs := []rune(s)
	if len(rs) > int(n) {
		return string(rs[0 : n-1])
	}
	return s
}

// GetName returns a name derived from a repository name. This is used to generate a name for the triggering repository.
func GetName(repo string) string {
	return truncateString(strings.Replace(repo, "/", "-", -1), 64)
}

// RepoConfigAppMetadata models app-specific metadata for the primary application
type RepoConfigAppMetadata struct {
	Repo              string   `yaml:"-" json:"repo"`   // set by nitro
	Ref               string   `yaml:"-" json:"ref"`    // set by nitro
	Branch            string   `yaml:"-" json:"branch"` // set by nitro
	ChartPath         string   `yaml:"chart_path" json:"chart_path"`
	ChartRepoPath     string   `yaml:"chart_repo_path" json:"chart_repo_path"`
	ChartVarsPath     string   `yaml:"chart_vars_path" json:"chart_vars_path"`
	ChartVarsRepoPath string   `yaml:"chart_vars_repo_path" json:"chart_vars_repo_path"`
	Image             string   `yaml:"image" json:"image"`
	DockerfilePath    string   `yaml:"dockerfile_path" json:"dockerfile_path"`
	ChartTagValue     string   `yaml:"image_tag_value" json:"image_tag_value"`
	NamespaceValue    string   `yaml:"namespace_value" json:"namespace_value"`
	EnvNameValue      string   `yaml:"env_name_value" json:"env_name_value"`
	ValueOverrides    []string `yaml:"value_overrides" json:"value_overrides"`
}

const (
	DefaultChartTagValue  = "image.tag"
	DefaultNamespaceValue = "namespace"
	DefaultEnvNameValue   = "env_name"
	DefaultDockerfilePath = "Dockerfile"
)

// SetValueDefaults sets default chart value names if empty
func (ram *RepoConfigAppMetadata) SetValueDefaults() {
	if ram.ChartTagValue == "" {
		ram.ChartTagValue = DefaultChartTagValue
	}
	if ram.NamespaceValue == "" {
		ram.NamespaceValue = DefaultNamespaceValue
	}
	if ram.EnvNameValue == "" {
		ram.EnvNameValue = DefaultEnvNameValue
	}
	if ram.DockerfilePath == "" {
		ram.DockerfilePath = DefaultDockerfilePath
	}
}

// RepoConfig models the config retrieved from the repository via acyl.yml (version >= 2)
type RepoConfig struct {
	Version        uint                  `yaml:"version" json:"version"`
	TargetBranches []string              `yaml:"target_branches" json:"target_branches"`
	TrackBranches  []string              `json:"track_branches" yaml:"track_branches"`
	Application    RepoConfigAppMetadata `yaml:"application" json:"application"`
	Dependencies   DependencyDeclaration `yaml:"dependencies" json:"dependencies"`
	Notifications  Notifications         `yaml:"notifications" json:"notifications"`
}

// RefMap generates RefMap for a particular environment
func (rc RepoConfig) RefMap() (RefMap, error) {
	rm := make(RefMap, rc.Dependencies.Count()+1)
	if rc.Application.Repo == "" {
		return nil, errors.New("empty repo")
	}
	if rc.Application.Branch == "" {
		return nil, errors.New("empty branch")
	}
	rm[rc.Application.Repo] = rc.Application.Branch
	for _, d := range rc.Dependencies.All() {
		if d.Repo != "" {
			if d.AppMetadata.Branch == "" {
				return nil, fmt.Errorf("empty branch for repo dependency: %v", d.Repo)
			}
			rm[d.Repo] = d.AppMetadata.Branch
		}
	}
	return rm, nil
}

// CommitSHAMap generates a CommitSHAMap for a particular environment
func (rc RepoConfig) CommitSHAMap() (RefMap, error) {
	rm := make(RefMap, rc.Dependencies.Count()+1)
	if rc.Application.Repo == "" {
		return nil, errors.New("empty repo")
	}
	if rc.Application.Ref == "" {
		return nil, errors.New("empty ref")
	}
	rm[rc.Application.Repo] = rc.Application.Ref
	for _, d := range rc.Dependencies.All() {
		if d.Repo != "" {
			if d.AppMetadata.Ref == "" {
				return nil, fmt.Errorf("empty ref for repo dependency: %v", d.Repo)
			}
			rm[d.Repo] = d.AppMetadata.Ref
		}
	}
	return rm, nil
}

// NameToRefMap returns a map of name (triggering repo and dependencies) to ref
func (rc RepoConfig) NameToRefMap() map[string]string {
	out := make(map[string]string, rc.Dependencies.Count()+1)
	name := GetName(rc.Application.Repo)
	out[name] = rc.Application.Ref
	for _, d := range rc.Dependencies.All() {
		out[d.Name] = d.AppMetadata.Ref
	}
	return out
}

// DependencyDeclaration models the dependencies for an environment
type DependencyDeclaration struct {
	Direct      []RepoConfigDependency `yaml:"direct" json:"direct"`
	Environment []RepoConfigDependency `yaml:"environment" json:"environment"`
}

// Count returns the total count of all dependencies
func (dd DependencyDeclaration) Count() int {
	return len(dd.Direct) + len(dd.Environment)
}

// All returns all dependencies (direct + environment)
func (dd DependencyDeclaration) All() []RepoConfigDependency {
	return append(dd.Direct, dd.Environment...)
}

// RefMapCount returns the count of dependencies that can participate in branch matching
func (dd DependencyDeclaration) RefMapCount() int {
	var count int
	for _, d := range dd.Direct {
		if d.BranchMatchable() {
			count++
		}
	}
	for _, d := range dd.Environment {
		if d.BranchMatchable() {
			count++
		}
	}
	return count
}

// ValidateNames indicates whether all dependencies have unique names and valid requirement references.
func (dd DependencyDeclaration) ValidateNames() (bool, error) {
	nm := map[string]struct{}{}
	rm := map[string]struct{}{}
	checkNames := func(label string, deps []RepoConfigDependency) (bool, error) {
		for i, d := range deps {
			if d.Name == "" {
				return false, nitroerrors.User(fmt.Errorf("empty name at offset %v in %v dependencies", i, label))
			}
			if _, ok := nm[d.Name]; ok {
				return false, nitroerrors.User(fmt.Errorf("duplicate name at offset %v in %v dependencies", i, label))
			}
			nm[d.Name] = struct{}{}
			if d.BranchMatchable() {
				if _, ok := rm[d.Repo]; ok {
					return false, nitroerrors.User(fmt.Errorf("duplicate repo dependency at offset %v in %v dependencies: %v", i, label, d.Repo))
				}
				rm[d.Repo] = struct{}{}
			}
		}
		for _, d := range deps {
			for j, r := range d.Requires {
				if _, ok := nm[r]; !ok {
					return false, nitroerrors.User(fmt.Errorf("unknown requirement of %v dependency '%v' at offset %v: %v", label, d.Name, j, r))
				}
			}
		}
		return true, nil
	}
	if ok, err := checkNames("direct", dd.Direct); !ok {
		return ok, err
	}
	return checkNames("environment", dd.Environment)
}

// ConfigSignature returns a hash of the config dependency configuration, for determining whether to upgrade an environment or tear down and rebuild
func (rc RepoConfig) ConfigSignature() [32]byte {
	buf := bytes.NewBuffer([]byte{})
	hashDep := func(d RepoConfigDependency) {
		buf.Write([]byte(d.Name))
		buf.Write([]byte(d.Repo))
		buf.Write([]byte(d.ChartPath))
		buf.Write([]byte(d.ChartRepoPath))
		buf.Write([]byte(strings.Join(d.Requires, "")))
	}
	for _, d := range rc.Dependencies.Direct {
		hashDep(d)
	}
	for _, d := range rc.Dependencies.Environment {
		hashDep(d)
	}
	return sha3.Sum256(buf.Bytes())
}

// KubernetesEnvironment models a single environment in k8s
type KubernetesEnvironment struct {
	Created         time.Time   `yaml:"created" json:"created"`
	Updated         pq.NullTime `yaml:"updated" json:"updated"`
	Namespace       string      `yaml:"namespace" json:"namespace"`
	EnvName         string      `yaml:"env_name" json:"env_name" `
	RepoConfigYAML  []byte      `yaml:"config" json:"config"`
	ConfigSignature []byte      `yaml:"config_signature" json:"config_signature"`
	RefMapJSON      string      `yaml:"ref_map_json" json:"ref_map_json"`
	Privileged      bool        `yaml:"privileged" json:"privileged"`
}

func (ke KubernetesEnvironment) Columns() string {
	return strings.Join([]string{"created", "updated", "env_name", "namespace", "repo_config_yaml", "config_signature", "ref_map_json", "privileged"}, ",")
}

func (ke KubernetesEnvironment) InsertColumns() string {
	return strings.Join([]string{"env_name", "namespace", "repo_config_yaml", "config_signature", "ref_map_json", "privileged"}, ",")
}

func (ke KubernetesEnvironment) UpdateColumns() string {
	return strings.Join([]string{"namespace", "repo_config_yaml", "config_signature", "ref_map_json", "privileged"}, ",")
}

func (ke *KubernetesEnvironment) ScanValues() []interface{} {
	return []interface{}{&ke.Created, &ke.Updated, &ke.EnvName, &ke.Namespace, &ke.RepoConfigYAML, &ke.ConfigSignature, &ke.RefMapJSON, &ke.Privileged}
}

func (ke *KubernetesEnvironment) InsertValues() []interface{} {
	return []interface{}{&ke.EnvName, &ke.Namespace, &ke.RepoConfigYAML, &ke.ConfigSignature, &ke.RefMapJSON, &ke.Privileged}
}

func (ke *KubernetesEnvironment) UpdateValues() []interface{} {
	return []interface{}{&ke.Namespace, &ke.RepoConfigYAML, &ke.ConfigSignature, &ke.RefMapJSON, &ke.Privileged}
}

func (ke KubernetesEnvironment) params(colfunc func() string) string {
	params := []string{}
	for i := range strings.Split(colfunc(), ",") {
		params = append(params, fmt.Sprintf("$%v", i+1))
	}
	return strings.Join(params, ", ")
}

func (ke KubernetesEnvironment) InsertParams() string {
	return ke.params(ke.InsertColumns)
}

func (ke KubernetesEnvironment) UpdateParams() string {
	return ke.params(ke.UpdateColumns)
}

// HelmRelease models a single Helm release within K8s associated with an environment
type HelmRelease struct {
	ID           uint      `json:"id"`
	Created      time.Time `json:"created"`
	EnvName      string    `json:"env_name"`
	K8sNamespace string    `json:"k8s_namespace"`
	Release      string    `json:"release"`
	RevisionSHA  string    `json:"revision_sha"`
	Name         string    `json:"name"`
}

func (hr HelmRelease) Columns() string {
	return strings.Join([]string{"id", "created", "env_name", "k8s_namespace", "release", "revision_sha", "name"}, ",")
}

func (hr HelmRelease) InsertColumns() string {
	return strings.Join([]string{"env_name", "k8s_namespace", "release", "revision_sha", "name"}, ",")
}

func (hr *HelmRelease) ScanValues() []interface{} {
	return []interface{}{&hr.ID, &hr.Created, &hr.EnvName, &hr.K8sNamespace, &hr.Release, &hr.RevisionSHA, &hr.Name}
}

func (hr *HelmRelease) InsertValues() []interface{} {
	return []interface{}{&hr.EnvName, &hr.K8sNamespace, &hr.Release, &hr.RevisionSHA, &hr.Name}
}

func (hr HelmRelease) InsertParams() string {
	params := []string{}
	for i := range strings.Split(hr.InsertColumns(), ",") {
		params = append(params, fmt.Sprintf("$%v", i+1))
	}
	return strings.Join(params, ", ")
}

// Notifications models configuration for notifications in acyl.yml v2
type Notifications struct {
	Slack     SlackNotifications              `yaml:"slack" json:"slack"`
	GitHub    GitHubNotifications             `yaml:"github" json:"github"`
	Templates map[string]NotificationTemplate `yaml:"templates" json:"templates"`
}

// FillMissingTemplates fills in any missing templates with defaults
func (n *Notifications) FillMissingTemplates() {
	if n.Templates == nil {
		n.Templates = make(map[string]NotificationTemplate)
	}
	for k, v := range DefaultNotificationTemplates {
		if _, ok := n.Templates[k]; !ok {
			n.Templates[k] = v
		}
	}
}

// GitHubNotifications models GitHub notification options
type GitHubNotifications struct {
	PRComments     bool           `yaml:"pr_comments" json:"pr_comments"`
	CommitStatuses CommitStatuses `yaml:"commit_statuses" json:"commit_statuses"`
}

// SlackNotifications models configuration for slack notifications in acyl.yml v2
type SlackNotifications struct {
	DisableGithubUserDM bool      `yaml:"disable_github_user_dm" json:"disable_github_user_dm"`
	Channels            *[]string `yaml:"channels" json:"channels"`
	Users               []string  `yaml:"users" json:"users"`
}

// DefaultNotificationTemplates are the default notification templates if none are supplied
var DefaultNotificationTemplates = map[string]NotificationTemplate{
	"create": NotificationTemplate{
		Title: "🛠 Creating Environment",
		Sections: []NotificationTemplateSection{
			NotificationTemplateSection{
				Title: "{{ .EnvName }}",
				Text:  "{{ .Repo }}\nPR #{{ .PullRequest }}: {{ .SourceBranch }} ➡️ {{ .BaseBranch }}",
				Style: "good",
			},
		},
	},
	"update": NotificationTemplate{
		Title: "🚦 Updating Environment",
		Sections: []NotificationTemplateSection{
			NotificationTemplateSection{
				Title: "{{ .EnvName }}",
				Text:  "{{ .Repo }}\nPR #{{ .PullRequest }}: {{ .SourceBranch }} ➡️ {{ .BaseBranch }}\nUpdating to commit:\nhttps://github.com/{{ .Repo }}/commit/{{ .SourceSHA }}\n\"{{ .CommitMessage }}\" - {{ .User }}",
				Style: "warning",
			},
		},
	},
	"destroy": NotificationTemplate{
		Title: `💣 Destroying Environment{{ if eq .Event "EnvironmentLimitExceeded" }} (Environment Limit Exceeded){{ end }}`,
		Sections: []NotificationTemplateSection{
			NotificationTemplateSection{
				Title: "{{ .EnvName }}",
				Text:  "{{ .Repo }}\nPR #{{ .PullRequest }}: {{ .SourceBranch }} ➡️ {{ .BaseBranch }}",
				Style: "warning",
			},
		},
	},
	"success": NotificationTemplate{
		Title: "🏁 Environment Ready",
		Sections: []NotificationTemplateSection{
			NotificationTemplateSection{
				Title: "{{ .EnvName }}",
				Text:  "{{ .Repo }}\nPR #{{ .PullRequest }}: {{ .SourceBranch }} ➡️ {{ .BaseBranch }}",
				Style: "good",
			},
			NotificationTemplateSection{
				Text:  "https://github.com/{{ .Repo }}/pull/{{ .PullRequest }}\nK8s Namespace: {{ .K8sNamespace }}",
				Style: "good",
			},
		},
	},
	"failure": NotificationTemplate{
		Title: "❌☠️ Environment Error",
		Sections: []NotificationTemplateSection{
			NotificationTemplateSection{
				Title: "{{ .EnvName }}",
				Text:  "{{ .Repo }}\nPR #{{ .PullRequest }}: {{ .SourceBranch }} ➡️ {{ .BaseBranch }}",
				Style: "danger",
			},
			NotificationTemplateSection{
				Title: "{{ .EnvName }}",
				Text:  "{{ .ErrorMessage }}",
				Style: "danger",
			},
		},
	},
}

// NotificationTemplate models a notification template for an event
type NotificationTemplate struct {
	Title    string                        `yaml:"title" json:"title"`
	Sections []NotificationTemplateSection `yaml:"sections" json:"sections"`
}

// NotificationData models the data available to notification templates (all events)
type NotificationData struct {
	EnvName, Repo, SourceBranch, SourceSHA, BaseBranch, BaseSHA, CommitMessage, ErrorMessage, User, K8sNamespace, Event string
	PullRequest                                                                                                         uint
}

func (nt NotificationTemplate) Render(d NotificationData) (*RenderedNotification, error) {
	out := &RenderedNotification{Sections: make([]RenderedNotificationSection, len(nt.Sections))}
	exectmpl := func(name, tmplstr string) (string, error) {
		tmpl, err := template.New(name).Parse(tmplstr)
		if err != nil {
			return "", errors.Wrap(err, "error parsing template")
		}
		out := &bytes.Buffer{}
		if err := tmpl.Execute(out, d); err != nil {
			return "", errors.Wrap(err, "error executing template")
		}
		return out.String(), nil
	}
	title, err := exectmpl("title", nt.Title)
	if err != nil {
		return nil, errors.Wrap(err, "error rendering title")
	}
	out.Title = title
	for i, s := range nt.Sections {
		if len(s.Title) > 0 {
			stitle, err := exectmpl("sectiontitle", s.Title)
			if err != nil {
				return nil, errors.Wrapf(err, "error rendering section %v title", i)
			}
			out.Sections[i].Title = stitle
		}
		if len(s.Text) > 0 {
			stxt, err := exectmpl("sectiontxt", s.Text)
			if err != nil {
				return nil, errors.Wrapf(err, "error rendering section %v text", i)
			}
			out.Sections[i].Text = stxt
		}
		out.Sections[i].Style = s.Style
	}
	return out, nil
}

// NotificationTemplateSection models a section of a notification template
type NotificationTemplateSection struct {
	Title string `yaml:"title" json:"title"`
	Text  string `yaml:"text" json:"text"`
	Style string `yaml:"style" json:"style"`
}

// RenderedNotification models a rendered notification template for an event
type RenderedNotification struct {
	Title    string
	Sections []RenderedNotificationSection
}

// RenderedNotificationSection models a rendered section of a notification
type RenderedNotificationSection struct {
	Title, Text, Style string
}
