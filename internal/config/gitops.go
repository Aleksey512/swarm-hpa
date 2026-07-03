package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/goccy/go-yaml"

	"github.com/Aleksey512/swarm-hpa/internal/core/model"
)

// LoadGitOps reads repos.yaml and stacks.yaml from configsPath and returns the
// repo configs and stack definitions. The file shapes match swarm-cd for drop-in
// compatibility. A stack's branch defaults to "main" when omitted.
func LoadGitOps(configsPath string) (map[string]model.RepoConfig, []model.StackConfig, error) {
	repos, err := loadReposFile(filepath.Join(configsPath, "repos.yaml"))
	if err != nil {
		return nil, nil, err
	}
	stacks, err := loadStacksFile(configsPath, repos)
	if err != nil {
		return nil, nil, err
	}
	return repos, stacks, nil
}

// fileRepo mirrors one entry of swarm-cd's repos.yaml.
type fileRepo struct {
	URL          string `yaml:"url"`
	Username     string `yaml:"username"`
	Password     string `yaml:"password"`
	PasswordFile string `yaml:"password_file"`
}

// fileStack mirrors one entry of swarm-cd's stacks.yaml.
type fileStack struct {
	Repo        string `yaml:"repo"`
	Branch      string `yaml:"branch"`
	ComposeFile string `yaml:"compose_file"`
	ValuesFile  string `yaml:"values_file"`
}

func loadReposFile(path string) (map[string]model.RepoConfig, error) {
	b, err := os.ReadFile(path) //nolint:gosec // G304: path is an admin-controlled config file (repos.yaml/stacks.yaml)
	if err != nil {
		return nil, fmt.Errorf("gitops: read repos file %q: %w", path, err)
	}
	var raw map[string]fileRepo
	if err := yaml.Unmarshal(b, &raw); err != nil {
		return nil, fmt.Errorf("gitops: parse repos file %q: %w", path, err)
	}
	repos := make(map[string]model.RepoConfig, len(raw))
	for name, r := range raw {
		if r.URL == "" {
			return nil, fmt.Errorf("gitops: repo %q has no url", name)
		}
		repos[name] = model.RepoConfig{
			URL:          r.URL,
			Username:     r.Username,
			Password:     r.Password,
			PasswordFile: r.PasswordFile,
		}
	}
	return repos, nil
}

func loadStacksFile(configsPath string, repos map[string]model.RepoConfig) ([]model.StackConfig, error) {
	path := filepath.Join(configsPath, "stacks.yaml")
	b, err := os.ReadFile(path) //nolint:gosec // G304: path is an admin-controlled config file (repos.yaml/stacks.yaml)
	if err != nil {
		return nil, fmt.Errorf("gitops: read stacks file %q: %w", path, err)
	}
	var raw map[string]fileStack
	if err := yaml.Unmarshal(b, &raw); err != nil {
		return nil, fmt.Errorf("gitops: parse stacks file %q: %w", path, err)
	}
	stacks := make([]model.StackConfig, 0, len(raw))
	for name, s := range raw {
		if s.Repo == "" {
			return nil, fmt.Errorf("gitops: stack %q has no repo", name)
		}
		if _, ok := repos[s.Repo]; !ok {
			return nil, fmt.Errorf("gitops: stack %q references unknown repo %q", name, s.Repo)
		}
		if s.ComposeFile == "" {
			return nil, fmt.Errorf("gitops: stack %q has no compose_file", name)
		}
		branch := s.Branch
		if branch == "" {
			branch = "main"
		}
		stacks = append(stacks, model.StackConfig{
			Name:        name,
			Repo:        s.Repo,
			Branch:      branch,
			ComposeFile: s.ComposeFile,
			ValuesFile:  s.ValuesFile,
		})
	}
	return stacks, nil
}
