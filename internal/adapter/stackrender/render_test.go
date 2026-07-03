package stackrender

import (
	"fmt"
	"io"
	"strings"
	"testing"

	"log/slog"
)

func newRenderer() *Renderer { return New(slog.New(slog.NewTextHandler(io.Discard, nil))) }

// str navigates a nested map[string]any and stringifies the leaf (handles YAML
// ints/floats, not just strings).
func str(v any, path ...string) string {
	cur := v
	for _, p := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			return "!notmap"
		}
		cur = m[p]
	}
	return fmt.Sprint(cur)
}

func TestRender_Plain(t *testing.T) {
	out, err := newRenderer().Render([]byte("services:\n  web:\n    image: nginx\n"), nil)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if got := str(out, "services", "web", "image"); got != "nginx" {
		t.Fatalf("image = %q, want nginx", got)
	}
}

func TestRender_TemplateValues(t *testing.T) {
	compose := []byte("services:\n  web:\n    image: {{.Values.image}}\n    replicas: {{.Values.replicas}}\n")
	values := []byte("image: nginx:alpine\nreplicas: 3\n")
	out, err := newRenderer().Render(compose, values)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if got := str(out, "services", "web", "image"); got != "nginx:alpine" {
		t.Fatalf("image = %q, want nginx:alpine", got)
	}
	if got := str(out, "services", "web", "replicas"); got != "3" {
		// yaml renders the integer into the map; numbers come back as int/float.
		t.Fatalf("replicas = %q, want 3", got)
	}
}

func TestRender_InvalidTemplateSyntax(t *testing.T) {
	_, err := newRenderer().Render([]byte("services:\n  web:\n    image: {{ .Values.image }"), []byte("image: x\n"))
	if err == nil || !strings.Contains(err.Error(), "parse compose template") {
		t.Fatalf("want template parse error, got %v", err)
	}
}

func TestRender_InvalidComposeYAML(t *testing.T) {
	_, err := newRenderer().Render([]byte("services: [unterminated\n"), nil)
	if err == nil || !strings.Contains(err.Error(), "parse compose yaml") {
		t.Fatalf("want compose yaml parse error, got %v", err)
	}
}

func TestRender_InvalidValuesYAML(t *testing.T) {
	_, err := newRenderer().Render([]byte("services:\n  web:\n    image: x\n"), []byte("image: [unterminated\n"))
	if err == nil || !strings.Contains(err.Error(), "parse values") {
		t.Fatalf("want values parse error, got %v", err)
	}
}
