// Package model holds the daemon's pure domain types (service policy, scale
// decision, task/node views). It has no infrastructure dependencies and must
// never import adapter packages, the Docker SDK, or a Prometheus client.
package model
