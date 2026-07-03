// Package port defines the interfaces (ports) the core needs from the outside
// world — MetricsProvider, SwarmController, and Clock. Adapters under
// internal/adapter implement these; the core depends only on the interfaces.
package port
