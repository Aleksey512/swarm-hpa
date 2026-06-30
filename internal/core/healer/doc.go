// Package healer holds the pure detection logic for tasks stuck in pending
// under a placement constraint after node recovery (moby/moby#42215). It
// decides whether a service warrants a force-update; it performs no I/O.
package healer
