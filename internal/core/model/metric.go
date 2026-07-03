package model

import "errors"

// Metric names recognized by the autoscaler, matched (case-insensitively, with
// "mem" accepted as an alias for memory in providers) against
// ServicePolicy.Metric.
const (
	MetricCPU    = "cpu"
	MetricMemory = "memory"
)

// ErrNoMetricData signals that a metric is currently unavailable for a service
// (for example, no locally-readable container stats). Callers should skip the
// service for this cycle rather than treating it as a failure.
var ErrNoMetricData = errors.New("no metric data")
