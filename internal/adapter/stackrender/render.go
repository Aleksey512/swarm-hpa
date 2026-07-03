// Package stackrender implements port.StackRenderer. It mirrors swarm-cd's
// compose rendering: the compose file is an optional Go text/template evaluated
// against a values file exposed as {{.Values.*}}, then parsed as YAML into a
// generic map for the deploy adapter. Pure — no I/O; both inputs are bytes.
package stackrender

import (
	"bytes"
	"fmt"
	"log/slog"
	"text/template"

	"github.com/goccy/go-yaml"

	"github.com/Aleksey512/swarm-hpa/internal/core/port"
)

// Renderer implements port.StackRenderer.
type Renderer struct {
	logger *slog.Logger
}

// compile-time proof the renderer satisfies the core port.
var _ port.StackRenderer = (*Renderer)(nil)

// New builds a Renderer. A nil logger falls back to slog.Default.
func New(logger *slog.Logger) *Renderer {
	if logger == nil {
		logger = slog.Default()
	}
	return &Renderer{logger: logger}
}

// Render parses compose (optionally templated against values) into a generic
// compose map. When values is non-empty, compose is treated as a Go text/template
// executed with {"Values": <values>}, matching swarm-cd so existing stacks port
// unchanged. Missing value keys render as "<no value>" (swarm-cd's lenient
// default); a malformed template or invalid YAML is an error.
func (r *Renderer) Render(compose, values []byte) (map[string]any, error) {
	rendered := compose
	if len(values) > 0 {
		var valuesMap map[string]any
		if err := yaml.Unmarshal(values, &valuesMap); err != nil {
			return nil, fmt.Errorf("stackrender: parse values: %w", err)
		}
		tmpl, err := template.New("compose").Parse(string(compose))
		if err != nil {
			return nil, fmt.Errorf("stackrender: parse compose template: %w", err)
		}
		var buf bytes.Buffer
		if err := tmpl.Execute(&buf, map[string]map[string]any{"Values": valuesMap}); err != nil {
			return nil, fmt.Errorf("stackrender: execute compose template: %w", err)
		}
		rendered = buf.Bytes()
		r.logger.Debug("stackrender: rendered template", "values_keys", len(valuesMap))
	} else {
		r.logger.Debug("stackrender: plain compose (no values)")
	}

	var out map[string]any
	if err := yaml.Unmarshal(rendered, &out); err != nil {
		return nil, fmt.Errorf("stackrender: parse compose yaml: %w", err)
	}
	return out, nil
}
