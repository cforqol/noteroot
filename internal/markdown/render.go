// Package markdown renders markdown to sanitized HTML.
//
// Output is safe for innerHTML: goldmark produces the HTML, bluemonday
// enforces a strict allowlist. Raw HTML in a note never reaches the browser
// unfiltered.
package markdown

import (
	"bytes"
	"sync"

	"github.com/microcosm-cc/bluemonday"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer/html"
)

type Renderer struct {
	md     goldmark.Markdown
	policy *bluemonday.Policy
}

var get = sync.OnceValue(func() *Renderer {
	md := goldmark.New(
		goldmark.WithExtensions(extension.GFM, extension.Footnote, &wikilinkExtension{}),
		goldmark.WithParserOptions(parser.WithAutoHeadingID()),
		goldmark.WithRendererOptions(html.WithHardWraps()),
	)
	p := bluemonday.UGCPolicy()
	p.AllowAttrs("class").OnElements("code", "pre", "span", "a")
	p.AllowAttrs("data-path").OnElements("a")
	p.AllowAttrs("id").OnElements("h1", "h2", "h3", "h4", "h5", "h6")
	return &Renderer{md: md, policy: p}
})

func New() *Renderer { return get() }

// Render renders markdown with wikilinks parsed but unresolved: every
// wikilink becomes a visible dead link, never literal [[brackets]] and
// never a silent drop.
func (r *Renderer) Render(src []byte) ([]byte, error) {
	var buf bytes.Buffer
	if err := r.md.Convert(src, &buf); err != nil {
		return nil, err
	}
	return r.policy.SanitizeBytes(buf.Bytes()), nil
}

// RenderWithResolver renders markdown, resolving [[wikilinks]] via the
// provided function. When resolve is nil or returns ok=false the link is
// rendered as a dead span.
func (r *Renderer) RenderWithResolver(src []byte, resolve func(target string) (string, bool)) ([]byte, error) {
	md := goldmark.New(
		goldmark.WithExtensions(extension.GFM, extension.Footnote, &wikilinkExtension{resolve: resolve}),
		goldmark.WithParserOptions(parser.WithAutoHeadingID()),
		goldmark.WithRendererOptions(html.WithHardWraps()),
	)
	var buf bytes.Buffer
	if err := md.Convert(src, &buf); err != nil {
		return nil, err
	}
	return r.policy.SanitizeBytes(buf.Bytes()), nil
}
