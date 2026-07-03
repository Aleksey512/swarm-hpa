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

// StackConfig describes one stack to sync from Git and deploy to Swarm. It mirrors
// a swarm-cd stacks.yaml entry (branch / compose_file / values_file). Name is the
// Swarm stack namespace — the argument passed to `docker stack deploy <name>`.
type StackConfig struct {
	Name        string // Swarm stack namespace
	Repo        string // key into the repos map
	Branch      string
	ComposeFile string
	ValuesFile  string // optional; "" disables template rendering
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
