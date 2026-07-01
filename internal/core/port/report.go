package port

import "github.com/Aleksey512/swarm-hpa/internal/core/model"

// ReportSink receives agent load reports at the manager. It is implemented by
// app/registry.Registry and consumed by the inbound ingest HTTP adapter, so that
// adapter depends on this core port rather than on the app package directly.
type ReportSink interface {
	// Ingest records a report from an agent. source is the opaque identity of the
	// sender (e.g. its network address), used only to detect a second live agent
	// reporting for the same node. It returns an error only for a malformed
	// report (for example an empty node ID).
	Ingest(report model.AgentReport, source string) error
}
