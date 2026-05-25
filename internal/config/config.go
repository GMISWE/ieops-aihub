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
