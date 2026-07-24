package model

// RepoConfig describes a single Git repository that backs one or more stacks. It
// mirrors a swarm-cd repos.yaml entry for drop-in compatibility. Public repos
// leave Username and Password empty; private repos authenticate over HTTP basic
// auth via a Password or a PasswordFile (the foundation slice matches swarm-cd:
// no SSH support yet).
type RepoConfig struct {
	URL          string
	Username     string
	Password     string
	PasswordFile string
}

// ComposeFileSpec is one compose file of a stack, with an optional per-file
// image pull policy.
//
// A stack may declare several compose files (ComposeFiles on StackConfig). They
// are deployed in slice order, one `docker stack deploy` each. Deploys are
// ADDITIVE: Swarm does not prune services that are absent from a later file's
// deploy, so the files accumulate into the single stack namespace. Each file is
// deployed AS-IS — there is no merging — so each file must be self-contained
// (declare its own networks/volumes and any top-level secrets/configs it
// references). List order is the deploy order (put shared infrastructure first).
//
// A non-empty PullPolicy overrides the stack-level PullPolicy AND the global
// --gitops-pull-policy for THIS file's deploy only (precedence: file → stack →
// global). This is what makes a per-file pull split possible — e.g. dev apps
// pull `always` while a postgres file pulls `changed`, via two deploys.
type ComposeFileSpec struct {
	File       string // repo-relative path to the compose file
	PullPolicy string // "", "always", or "changed"; "" inherits stack→global
}

// StackConfig describes one stack to sync from Git and deploy to Swarm. It mirrors
// a swarm-cd stacks.yaml entry (branch / compose_file / values_file). Name is the
// Swarm stack namespace — the argument passed to `docker stack deploy <name>`.
type StackConfig struct {
	Name   string // Swarm stack namespace
	Repo   string // key into the repos map
	Branch string
	// ComposeFiles is the ordered list of compose files for this stack. Each is
	// rendered and deployed in order (see ComposeFileSpec). At least one entry is
	// required; the field replaces the former single ComposeFile string.
	ComposeFiles []ComposeFileSpec
	ValuesFile   string // optional; "" disables template rendering
	// SopsFiles are repo-relative paths of sops-encrypted files to decrypt before
	// deploy (swarm-cd sops_files). Ignored when SopsSecretsDiscovery is true.
	SopsFiles []string
	// SopsSecretsDiscovery, when true, auto-discovers sops files from the
	// compose's file-backed secrets (swarm-cd sops_secrets_discovery) and ignores
	// SopsFiles.
	SopsSecretsDiscovery bool
	// PullPolicy overrides the global --gitops-pull-policy for this stack only
	// (swarm-cd has no equivalent; this is a swarm-hpa extension). Valid values:
	// "always" or "changed". Empty means "use the global policy".
	PullPolicy string
}

// StackService is the live, read-only projection of one Swarm service that belongs
// to a stack. The deploy adapter uses it to carry autoscaled replicas forward so a
// `docker stack deploy` never clobbers a count the autoscaler just set. Name is the
// short compose service name (Swarm stores the service as <stack>_<name>).
type StackService struct {
	Name       string
	Replicas   uint64
	Replicated bool
	Labels     map[string]string
}
