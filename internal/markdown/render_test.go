package markdown

import (
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/parser"
)

func render(t *testing.T, src string) string {
	t.Helper()
	out, err := New().Render([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

func TestNew_singleton(t *testing.T) {
	if New() != New() {
		t.Error("New should return the shared renderer")
	}
}

func TestRender_basics(t *testing.T) {
	out := render(t, "# Title\n\n**bold** and `code`\n\n- a\n- b\n")
	for _, want := range []string{`<h1 id="title">Title</h1>`, "<strong>bold</strong>", "<code>code</code>", "<li>a</li>"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

func TestRender_GFM(t *testing.T) {
	out := render(t, "| a | b |\n|---|---|\n| 1 | 2 |\n\n~~gone~~\n")
	if !strings.Contains(out, "<table>") || !strings.Contains(out, "<del>gone</del>") {
		t.Errorf("GFM not rendered:\n%s", out)
	}
}

func TestRender_sanitizes(t *testing.T) {
	tests := map[string]string{
		"script tag":      "<script>alert(1)</script>",
		"onerror handler": `<img src=x onerror="alert(1)">`,
		"javascript url":  "[x](javascript:alert(1))",
		"iframe":          `<iframe src="https://evil"></iframe>`,
		"style tag":       "<style>body{display:none}</style>",
	}
	for name, src := range tests {
		t.Run(name, func(t *testing.T) {
			out := strings.ToLower(render(t, src))
			for _, bad := range []string{"<script", "onerror", "javascript:", "<iframe", "<style"} {
				if strings.Contains(out, bad) {
					t.Errorf("output contains %q:\n%s", bad, out)
				}
			}
		})
	}
}

func TestRender_empty(t *testing.T) {
	if out := render(t, ""); strings.TrimSpace(out) != "" {
		t.Errorf("out = %q, want empty", out)
	}
}

type brokenMarkdown struct{ goldmark.Markdown }

func (brokenMarkdown) Convert([]byte, io.Writer, ...parser.ParseOption) error {
	return errors.New("convert failed")
}

func TestRender_convertError(t *testing.T) {
	r := &Renderer{md: brokenMarkdown{}}
	out, err := r.Render([]byte("# x"))
	if err == nil || out != nil {
		t.Fatalf("Render = %v, %v; want nil output and error", out, err)
	}
}
