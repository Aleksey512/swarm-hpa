package stackdeploy

import (
	"fmt"
	"path/filepath"
)

// fileObjects returns the compose objects that carry a "file" key, as references
// into the original map (callers mutate them, e.g. to set "name" during
// rotation). Mirrors swarm-cd getFileObjects. A non-string file value is an
// error; objects without a file key are skipped.
func fileObjects(objects map[string]any) (map[string]map[string]any, error) {
	result := make(map[string]map[string]any, len(objects))
	for name, raw := range objects {
		obj, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("invalid compose: %s object must be a map", name)
		}
		fileVal, has := obj["file"]
		if !has {
			continue
		}
		if _, ok := fileVal.(string); !ok {
			return nil, fmt.Errorf("invalid compose: %s.file must be a string", name)
		}
		result[name] = obj
	}
	return result, nil
}

// DiscoverSecretFiles returns the repo-relative paths of file-backed secrets
// declared in the compose (swarm-cd sops_secrets_discovery). composeDir is the
// repo-relative directory of the compose file, so a secret file "secrets/tls.crt"
// resolves to composeDir/secrets/tls.crt. Configs are NOT discovered here (only
// secrets are sops-decrypted).
func DiscoverSecretFiles(compose map[string]any, composeDir string) ([]string, error) {
	secrets, ok := compose["secrets"].(map[string]any)
	if !ok {
		return nil, nil
	}
	objs, err := fileObjects(secrets)
	if err != nil {
		return nil, fmt.Errorf("secrets: %w", err)
	}
	out := make([]string, 0, len(objs))
	for _, obj := range objs {
		out = append(out, filepath.Join(composeDir, obj["file"].(string)))
	}
	return out, nil
}
