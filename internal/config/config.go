package config

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Version  int                `yaml:"version"`
	AIHub    AIHubConfig        `yaml:"aihub"`
	Projects map[string]Project `yaml:"projects"`
}

type AIHubConfig struct {
	URL       string `yaml:"url"`
	APIKeyEnv string `yaml:"api_key_env"`
}

type Project struct {
	Repos       []Repo `yaml:"repos"`
	Description string `yaml:"description"`
	Scenario    string `yaml:"scenario,omitempty"`
}

type Repo struct {
	Name            string `yaml:"name"`
	URL             string `yaml:"url"`
	GithubOwnerRepo string `yaml:"github_owner_repo"`
	Description     string `yaml:"description"`

	// DescriptionBaseline is Description as of the last time `polyforge init`
	// saw this file and the server agree. It is bookkeeping written by init,
	// not a value anyone is expected to author.
	//
	// It exists because Description is the one field here that BOTH sides may
	// edit: the project owner edits it locally and init publishes it upward
	// (aihub#34), while MCP and the web UI edit the server's copy. Two values
	// cannot say which side moved, so init used to resolve every difference in
	// favour of the local file and silently reverted server-side edits
	// (aihub#310). Against a baseline the same comparison becomes three-way and
	// each side's change is identifiable on its own.
	//
	// omitempty on purpose: an ABSENT baseline and an empty one mean the same
	// thing to that comparison, so there is no third state needing a pointer to
	// represent it, and workspaces whose file predates this field migrate with
	// no special case — see reconcileDescription in internal/cli/init.go.
	DescriptionBaseline string `yaml:"description_baseline,omitempty"`
}

// Load reads .polyforge.yaml from the given directory.
func Load(dir string) (*Config, error) {
	path := filepath.Join(dir, ".polyforge.yaml")
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("load .polyforge.yaml: %w", err)
	}
	var cfg Config
	if err := yaml.Unmarshal(b, &cfg); err != nil {
		return nil, fmt.Errorf("parse .polyforge.yaml: %w", err)
	}
	return &cfg, nil
}
