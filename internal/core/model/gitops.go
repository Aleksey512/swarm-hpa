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

// ComposeFileSpec is one MERGE GROUP of a stack: a base compose file, its
// optional override files, and an optional per-group image pull policy. One spec
// = one `docker stack deploy`.
//
// Two different multi-file mechanisms meet here; do not confuse them:
//
//   - Several SPECS on a stack (ComposeFiles on StackConfig) are deployed in
//     slice order, one `docker stack deploy` each, with NO merging. Those deploys
//     are ADDITIVE: Swarm does not prune services absent from a later deploy, so
//     the specs accumulate into the single stack namespace. Each spec's base file
//     is deployed AS-IS, so it must be self-contained (declare its own
//     networks/volumes and any top-level secrets/configs it references).
//
//   - Overrides WITHIN one spec are merged into a SINGLE deploy — the Swarm
//     equivalent of compose's unsupported `include:`:
//     `docker stack deploy -c base.yml -c override.yml -c another.override.yml`.
//     docker/cli performs the merge (maps deep-merge, later file wins per key),
//     so an override file need NOT be self-contained: it may carry only the keys
//     it changes (environment, image, replicas, …). Slice order is `-c` order and
//     therefore decides precedence.
//
// A non-empty PullPolicy overrides the stack-level PullPolicy AND the global
// --gitops-pull-policy for THIS group's deploy only (precedence: file → stack →
// global). A merge group is one deploy and `docker stack deploy` accepts exactly
// one --resolve-image, so the policy is per GROUP, not per override file. This is
// what makes a per-file pull split possible — e.g. dev apps pull `always` while a
// postgres file pulls `changed`, via two separate specs.
type ComposeFileSpec struct {
	File string // repo-relative path to the base compose file
	// Overrides are repo-relative paths merged INTO this group's deploy, in
	// order, after File. They need not be self-contained; docker/cli merges them
	// over the base last-wins. Empty for a plain single-file deploy.
	Overrides  []string
	PullPolicy string // "", "always", or "changed"; "" inherits stack→global
}

// AllFiles returns the group's compose files in `-c` order: the base file first,
// then each override. The order is load-bearing — it is what docker/cli's merge
// uses to decide which value wins.
func (s ComposeFileSpec) AllFiles() []string {
	files := make([]string, 0, 1+len(s.Overrides))
	files = append(files, s.File)
	files = append(files, s.Overrides...)
	return files
}

// StackConfig describes one stack to sync from Git and deploy to Swarm. It mirrors
// a swarm-cd stacks.yaml entry (branch / compose_file / values_file). Name is the
// Swarm stack namespace — the argument passed to `docker stack deploy <name>`.
type StackConfig struct {
	Name   string // Swarm stack namespace
	Repo   string // key into the repos map
	Branch string
	// ComposeFiles is the ordered list of merge groups for this stack. Each group
	// is rendered and deployed in order, one `docker stack deploy` per group (see
	// ComposeFileSpec for how a group's base file and overrides relate). At least
	// one entry is required.
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

// LiveService is the read-only projection of one Swarm service from an
// unfiltered, cluster-wide service listing. Unlike StackService (scoped to one
// stack, short name), it carries the full Swarm service name and the raw stack
// namespace label so orphan detection can attribute every live service to a
// stack — or to none.
type LiveService struct {
	ID   string
	Name string // full Swarm service name (e.g. <stack>_<service>)
	// StackNamespace is the value of the com.docker.stack.namespace label —
	// the authoritative stack attribution `docker stack deploy` stamps on
	// every service it creates. Empty for services created outside any stack
	// (e.g. bare `docker service create`).
	StackNamespace string
	Labels         map[string]string
}
