package markdown

import (
	"errors"
	"io"
	"reflect"
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
	first := New()
	if New() != first {
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

func TestRender_wikilink_resolved(t *testing.T) {
	r := New()
	resolve := func(target string) (string, bool) {
		if target == "Other Note" {
			return "/n/work/note?path=other.md", true
		}
		return "", false
	}
	out, err := r.RenderWithResolver([]byte("See [[Other Note]] for details.\n"), resolve)
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)
	if !strings.Contains(got, `<a href="/n/work/note?path=other.md" class="wikilink" data-path="other.md"`) ||
		!strings.Contains(got, `>Other Note</a>`) {
		t.Errorf("resolved wikilink missing or malformed:\n%s", got)
	}
}

func TestRender_wikilink_dead(t *testing.T) {
	r := New()
	resolve := func(string) (string, bool) { return "", false }
	out, err := r.RenderWithResolver([]byte("See [[Missing]] here.\n"), resolve)
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)
	if !strings.Contains(got, `<span class="wikilink dead">Missing</span>`) {
		t.Errorf("dead wikilink missing:\n%s", got)
	}
}

func TestRender_wikilink_label(t *testing.T) {
	r := New()
	resolve := func(string) (string, bool) { return "/x", true }
	out, err := r.RenderWithResolver([]byte("[[Target|Click me]]\n"), resolve)
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)
	if !strings.Contains(got, `<a href="/x" class="wikilink" data-path=""`) ||
		!strings.Contains(got, `>Click me</a>`) {
		t.Errorf("label wikilink missing:\n%s", got)
	}
}

func TestRender_wikilink_noResolver(t *testing.T) {
	// Without a resolver every wikilink is a visible dead link — never
	// literal [[brackets]], never silently dropped.
	out := render(t, "See [[Other Note]] here.\n")
	if !strings.Contains(out, `<span class="wikilink dead">Other Note</span>`) {
		t.Errorf("without resolver, wikilink should render as a dead link:\n%s", out)
	}
	if strings.Contains(out, "[[Other Note]]") {
		t.Errorf("without resolver, wikilink must not stay literal:\n%s", out)
	}
}

func TestRender_wikilink_bareOpenStaysLiteral(t *testing.T) {
	out := render(t, "broken [[ open\n")
	if !strings.Contains(out, "[[ open") {
		t.Errorf("bare [[ should stay literal:\n%s", out)
	}
}

func TestRender_wikilink_insideCodeSpanIgnored(t *testing.T) {
	out := render(t, "`[[Target]]`\n")
	if strings.Contains(out, "wikilink") {
		t.Errorf("wikilink inside code span must not be parsed:\n%s", out)
	}
	if !strings.Contains(out, "[[Target]]") {
		t.Errorf("code span content should be preserved:\n%s", out)
	}
}

func TestRender_wikilink_insideCodeBlockIgnored(t *testing.T) {
	out := render(t, "```\n[[Target]]\n```\n")
	if strings.Contains(out, "wikilink") {
		t.Errorf("wikilink inside code block must not be parsed:\n%s", out)
	}
}

func TestRender_wikilink_invalidTarget(t *testing.T) {
	r := New()
	resolve := func(string) (string, bool) { return "/x", true }
	for _, src := range []string{"[[a]b]]\n", "[[a|b|c]]\n", "[[line\nbreak]]\n"} {
		out, err := r.RenderWithResolver([]byte(src), resolve)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(out), "wikilink") {
			t.Errorf("invalid target %q must not parse as wikilink:\n%s", src, out)
		}
	}
}

func TestRender_wikilink_multiple(t *testing.T) {
	r := New()
	resolve := func(target string) (string, bool) {
		return "/n/work/note?path=" + strings.ToLower(strings.ReplaceAll(target, " ", "-")) + ".md", true
	}
	out, err := r.RenderWithResolver([]byte("[[Alpha]] and [[Beta]]\n"), resolve)
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)
	if !strings.Contains(got, "Alpha</a>") || !strings.Contains(got, "Beta</a>") {
		t.Errorf("multiple wikilinks not both rendered:\n%s", got)
	}
}

func TestRender_wikilink_emptyLabel(t *testing.T) {
	r := New()
	resolve := func(string) (string, bool) { return "/x", true }
	out, err := r.RenderWithResolver([]byte("[[Target|]]\n"), resolve)
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)
	// Empty label should fallback to target
	if !strings.Contains(got, ">Target</a>") {
		t.Errorf("empty label should display target:\n%s", got)
	}
}

func TestRender_wikilink_whitespaceOnlyTarget(t *testing.T) {
	r := New()
	resolve := func(target string) (string, bool) { return "/x", true }
	out, err := r.RenderWithResolver([]byte("[[   ]]\n"), resolve)
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)
	// A whitespace-only target trims to nothing: it is not a wikilink at
	// all and must stay literal, exactly as the extractor skips it.
	if strings.Contains(got, "wikilink") {
		t.Errorf("whitespace-only target must not parse as a wikilink:\n%s", got)
	}
	if !strings.Contains(got, "[[   ]]") {
		t.Errorf("whitespace-only target should stay literal:\n%s", got)
	}
}

func TestRender_wikilink_trimsTargetAndLabel(t *testing.T) {
	r := New()
	resolve := func(target string) (string, bool) {
		if target != "Spaced" {
			t.Errorf("resolver got target %q, want trimmed %q", target, "Spaced")
		}
		return "/x", true
	}
	out, err := r.RenderWithResolver([]byte("[[  Spaced  |  My Label  ]]\n"), resolve)
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)
	if !strings.Contains(got, ">My Label</a>") {
		t.Errorf("label should render trimmed:\n%s", got)
	}
}

func TestExtractWikilinks(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want []Wikilink
	}{
		{"empty", "", nil},
		{"plain", "no links here", nil},
		{"target", "See [[Other Note]].", []Wikilink{{Target: "Other Note"}}},
		{"label", "See [[target|Custom Label]].", []Wikilink{{Target: "target", Label: "Custom Label"}}},
		{"multiple", "[[A]] and [[B]]", []Wikilink{{Target: "A"}, {Target: "B"}}},
		{"dedupe first label wins", "[[A|x]] and [[A|y]]", []Wikilink{{Target: "A", Label: "x"}}},
		{"trims target", "[[  spaced  ]]", []Wikilink{{Target: "spaced"}}},
		{"bare open literal", "broken [[ open", nil},
		{"whitespace only literal", "[[   ]]", nil},
		{"nested not supported", "[[outer [[inner]]]]", []Wikilink{{Target: "outer [[inner"}}},
		{"invalid pipe count", "[[a|b|c]]", nil},
		{"unbalanced bracket", "[[a]b]]", nil},
		{"multiline not a link", "[[line\nbreak]]", nil},
		{"code span ignored", "`[[Target]]`", nil},
		{"code block ignored", "```\n[[Target]]\n```\n", nil},
		{"html block ignored", "<div>[[Target]]</div>\n", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractWikilinks([]byte(tt.src))
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ExtractWikilinks() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestExtractWikilinks_matchesRenderer(t *testing.T) {
	// Every wikilink the extractor finds must be one the renderer also
	// parses — display and stored links can never disagree.
	src := []byte("A [[one]] B [[two|2]] C `[[no]]` D\n\n```\n[[nope]]\n```\n")
	extracted := ExtractWikilinks(src)
	r := New()
	resolve := func(target string) (string, bool) {
		for _, w := range extracted {
			if w.Target == target {
				return "/n/work/note?path=" + target + ".md", true
			}
		}
		t.Errorf("renderer resolved unknown target %q (not in extractor output)", target)
		return "", false
	}
	out, err := r.RenderWithResolver(src, resolve)
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)
	for _, w := range extracted {
		if !strings.Contains(got, `data-path="`+w.Target+`.md"`) {
			t.Errorf("renderer did not linkify extracted target %q:\n%s", w.Target, got)
		}
	}
}

func TestRender_wikilink_specialChars(t *testing.T) {
	r := New()
	resolve := func(string) (string, bool) { return "/x?path=a%2Fb.md", true }
	out, err := r.RenderWithResolver([]byte("[[a/b.md]]\n"), resolve)
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)
	// Should properly escape the href
	if !strings.Contains(got, `href="/x?path=a%2Fb.md"`) {
		t.Errorf("special chars should be escaped:\n%s", got)
	}
}
