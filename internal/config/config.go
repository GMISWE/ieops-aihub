package config

type Config struct {
	Version  int                `yaml:"version"`
	Scenario string             `yaml:"scenario"`
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
}

type Repo struct {
	Name            string `yaml:"name"`
	URL             string `yaml:"url"`
	GithubOwnerRepo string `yaml:"github_owner_repo"`
	Description     string `yaml:"description"`
}
