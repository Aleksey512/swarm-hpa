#!/usr/bin/env bash
# init-repo.sh — turn examples/gitops/app-repo into a real local git source and
# resolve its absolute file:// URL into repos.yaml, so the example is runnable
# end-to-end without an external git host.
#
# Usage:   bash examples/gitops/init-repo.sh
# Safe to re-run (idempotent).
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
APP_REPO="${SCRIPT_DIR}/app-repo"
REPOS_YAML="${SCRIPT_DIR}/repos.yaml"

# 1. Make app-repo a git repo with at least one commit (go-git clones need a HEAD).
if [ ! -d "${APP_REPO}/.git" ]; then
  git init -q "${APP_REPO}"
fi
cd "${APP_REPO}"
if ! git rev-parse --verify HEAD >/dev/null 2>&1; then
  git add -A
  # Set local identity in case the environment has no user.email/user.name.
  git -c user.email="swarm-hpa@example.local" -c user.name="swarm-hpa demo" \
    commit -q -m "swarm-hpa gitops demo app"
fi
cd - >/dev/null

# 2. Resolve the absolute file:// URL and write it into repos.yaml.
APP_REPO_ABS="$(cd "${APP_REPO}" && pwd)"
URL="file://${APP_REPO_ABS}"

if [ ! -f "${REPOS_YAML}" ]; then
  echo "ERROR: ${REPOS_YAML} not found." >&2
  exit 1
fi
# Replace the `url:` line. Portable across macOS BSD sed and GNU sed.
sed -i.bak -E "s|^  url: .*$|  url: ${URL}|" "${REPOS_YAML}" && rm -f "${REPOS_YAML}.bak"

cat <<EOF

Done.
  app-repo git source: ${APP_REPO_ABS}
  repos.yaml url:      ${URL}

Run the daemon (dry-run first — watch the sync intent, touch nothing):
  make run ARGS="--gitops --gitops-configs-path=examples/gitops --gitops-repos-path=/tmp/swarm-hpa-repos --log-level=debug"

Then follow examples/gitops/README.md (enable real sync, carry-forward demo, drift at GET /stacks).
EOF
