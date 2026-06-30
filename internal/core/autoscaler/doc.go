// Package autoscaler holds the pure horizontal-scaling decision logic: given a
// metric value and a service policy it computes a desired replica count and
// clamps it to [min, max]. No I/O, no clock, no Docker — table-test friendly.
package autoscaler
