// Package dockererr classifies Docker/Swarm errors shared across adapters, so the
// autoscaler mutation path (adapter/swarm) and the GitOps deploy path
// (adapter/stackdeploy) agree on which errors are transient and worth retrying.
package dockererr

import (
	"strings"

	"github.com/docker/docker/errdefs"
)

// IsVersionConflict reports whether err is a Swarm optimistic-concurrency
// rejection: a ServiceUpdate carried a stale Version.Index because another writer
// updated the service first. Such errors are transient — re-inspect (or, for a
// stack deploy, re-run the idempotent deploy) and retry.
//
// errdefs.IsConflict covers the SDK-classified 409 Conflict path. The string
// match additionally covers the raft-store "update out of sequence" error, which
// surfaces over Swarm's internal gRPC channel as code=Unknown and is not always
// mapped to a Conflict by errdefs — so relying on IsConflict alone can miss the
// exact error produced by a concurrent autoscaler↔deploy mutation.
func IsVersionConflict(err error) bool {
	if err == nil {
		return false
	}
	if errdefs.IsConflict(err) {
		return true
	}
	return strings.Contains(err.Error(), "update out of sequence")
}
