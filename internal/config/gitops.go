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
	Repo   string `yaml:"repo"`
	Branch string `yaml:"branch"`
	// ComposeFile is polymorphic: it is decoded into a generic any and then
	// normalized by parseComposeFiles into []model.ComposeFileSpec. It accepts a
	// scalar string ("compose.yaml"), a list of strings ([a.yaml, b.yaml]), or a
	// list of objects ({file: ..., overrides: [...], pull_policy: always|changed}).
	// Mixed lists (strings and objects) are allowed. The scalar form preserves
	// swarm-cd drop-in compatibility.
	ComposeFile          any      `yaml:"compose_file"`
	ValuesFile           string   `yaml:"values_file"`
	SopsFiles            []string `yaml:"sops_files"`
	SopsSecretsDiscovery bool     `yaml:"sops_secrets_discovery"`
	// PullPolicy is a swarm-hpa extension (no swarm-cd equivalent); it overrides
	// the global --gitops-pull-policy for this stack, and is itself overridden by
	// a per-file pull_policy on a ComposeFileSpec.
	PullPolicy string `yaml:"pull_policy"`
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
		files, err := parseComposeFiles(name, s.ComposeFile)
		if err != nil {
			return nil, err
		}
		switch s.PullPolicy {
		case "", "always", "changed":
		default:
			return nil, fmt.Errorf("gitops: stack %q pull_policy must be always|changed, got %q", name, s.PullPolicy)
		}
		branch := s.Branch
		if branch == "" {
			branch = "main"
		}
		stacks = append(stacks, model.StackConfig{
			Name:                 name,
			Repo:                 s.Repo,
			Branch:               branch,
			ComposeFiles:         files,
			ValuesFile:           s.ValuesFile,
			SopsFiles:            s.SopsFiles,
			SopsSecretsDiscovery: s.SopsSecretsDiscovery,
			PullPolicy:           s.PullPolicy,
		})
	}
	return stacks, nil
}

// parseComposeFiles normalizes the polymorphic stacks.yaml compose_file value
// into an ordered []model.ComposeFileSpec (one spec = one merge group = one
// `docker stack deploy`). It accepts:
//   - a scalar string:  compose_file: compose.yaml
//   - a list of strings: compose_file: [a.yaml, b.yaml]
//   - a list of objects: compose_file: [{file: a.yaml, overrides: [a.prod.yaml], pull_policy: always}, ...]
//   - a mixed list (strings and objects) — strings inherit the stack/global policy
//
// Each object's pull_policy must be "", "always", or "changed". At least one file
// is required. The scalar form is fully backward compatible with the original
// single-string compose_file (swarm-cd drop-in).
//
// goccy/go-yaml decodes a YAML scalar into a Go string and a sequence/mapping
// into []any/map[string]any, so type-switching on any covers all four shapes
// without depending on a custom UnmarshalYAML hook.
func parseComposeFiles(stack string, v any) ([]model.ComposeFileSpec, error) {
	switch t := v.(type) {
	case nil:
		return nil, fmt.Errorf("gitops: stack %q has no compose_file", stack)
	case string:
		if t == "" {
			return nil, fmt.Errorf("gitops: stack %q has no compose_file", stack)
		}
		return []model.ComposeFileSpec{{File: t}}, nil
	case []any:
		if len(t) == 0 {
			return nil, fmt.Errorf("gitops: stack %q has no compose_file", stack)
		}
		out := make([]model.ComposeFileSpec, 0, len(t))
		for i, item := range t {
			spec, err := parseComposeFileItem(stack, i, item)
			if err != nil {
				return nil, err
			}
			out = append(out, spec)
		}
		return out, nil
	default:
		return nil, fmt.Errorf("gitops: stack %q compose_file must be a string or a list, got %T", stack, v)
	}
}

// parseComposeFileItem normalizes one element of a compose_file list into a merge
// group. A string element is a bare base file that inherits the stack/global pull
// policy; an object element may additionally carry `overrides` (files merged into
// the SAME deploy via extra -c flags) and a per-group pull_policy.
func parseComposeFileItem(stack string, idx int, item any) (model.ComposeFileSpec, error) {
	switch t := item.(type) {
	case string:
		if t == "" {
			return model.ComposeFileSpec{}, fmt.Errorf("gitops: stack %q compose_file[%d] is empty", stack, idx)
		}
		return model.ComposeFileSpec{File: t}, nil
	case map[string]any:
		file, _ := t["file"].(string)
		if file == "" {
			return model.ComposeFileSpec{}, fmt.Errorf("gitops: stack %q compose_file[%d] has no file", stack, idx)
		}
		policy, _ := t["pull_policy"].(string)
		switch policy {
		case "", "always", "changed":
		default:
			return model.ComposeFileSpec{}, fmt.Errorf("gitops: stack %q compose_file[%d] pull_policy must be always|changed, got %q", stack, idx, policy)
		}
		overrides, err := parseOverrides(stack, idx, file, t["overrides"])
		if err != nil {
			return model.ComposeFileSpec{}, err
		}
		return model.ComposeFileSpec{File: file, Overrides: overrides, PullPolicy: policy}, nil
	default:
		return model.ComposeFileSpec{}, fmt.Errorf("gitops: stack %q compose_file[%d] must be a string or an object, got %T", stack, idx, item)
	}
}

// parseOverrides normalizes the `overrides` key of one compose_file object into
// an ordered []string of repo-relative paths. Overrides are merged into the SAME
// `docker stack deploy` as base (extra -c flags), so order matters: later files
// win. An absent or empty list yields nil (a plain single-file deploy).
//
// A repeated file is rejected — whether it repeats the base or an earlier
// override. Passing the same document twice to `docker stack deploy` is always a
// config mistake: it would merge the file onto itself rather than doing what the
// author intended.
func parseOverrides(stack string, idx int, base string, v any) ([]string, error) {
	if v == nil {
		return nil, nil
	}
	list, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("gitops: stack %q compose_file[%d] overrides must be a list of strings, got %T", stack, idx, v)
	}
	if len(list) == 0 {
		return nil, nil
	}
	out := make([]string, 0, len(list))
	seen := map[string]bool{base: true}
	for i, raw := range list {
		path, ok := raw.(string)
		if !ok || path == "" {
			return nil, fmt.Errorf("gitops: stack %q compose_file[%d] overrides[%d] must be a non-empty string, got %T", stack, idx, i, raw)
		}
		if path == base {
			return nil, fmt.Errorf("gitops: stack %q compose_file[%d] overrides[%d] duplicates the base file %q", stack, idx, i, base)
		}
		if seen[path] {
			return nil, fmt.Errorf("gitops: stack %q compose_file[%d] overrides[%d] duplicates an earlier override %q", stack, idx, i, path)
		}
		seen[path] = true
		out = append(out, path)
	}
	return out, nil
}
