package stackdeploy

import (
	"crypto/md5" //nolint:gosec // G501: md5 is a content-addressing hash for rotation object names, not a security primitive (swarm-cd parity)
	"fmt"
	"path/filepath"
)

// ApplyRotation renames file-backed configs and secrets in the compose map to
// <stack>-<name>-<hash> so Swarm picks up changed content (configs/secrets are
// immutable, so a changed hash = a new object name = Swarm re-deploys with the
// new content). Mirrors swarm-cd rotateObjects. fileResolver reads a
// repo-relative path and returns its (already-decrypted) bytes; composeDir is the
// repo-relative directory of the compose file. Returns the count of renamed
// objects. Non-file objects are left untouched.
func ApplyRotation(compose map[string]any, stack, composeDir string, fileResolver func(relRepoPath string) ([]byte, error)) (int, error) {
	changed := 0
	for _, section := range []string{"configs", "secrets"} {
		objects, ok := compose[section].(map[string]any)
		if !ok {
			continue
		}
		objs, err := fileObjects(objects)
		if err != nil {
			return changed, fmt.Errorf("%s: %w", section, err)
		}
		for name, obj := range objs {
			rel := filepath.Join(composeDir, obj["file"].(string))
			content, err := fileResolver(rel)
			if err != nil {
				return changed, fmt.Errorf("rotate: read %q: %w", rel, err)
			}
			hash := fmt.Sprintf("%x", md5.Sum(content))[:8] //nolint:gosec // G401: content hash for naming, not cryptography
			obj["name"] = stack + "-" + name + "-" + hash
			changed++
		}
	}
	return changed, nil
}
