package markdown

import (
	"bytes"
	"net/url"
	"strings"
	"sync"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// wikilinkNode is an inline node for [[Target]] or [[Target|Label]].
type wikilinkNode struct {
	ast.BaseInline
	Target string
	Label  string
}

var kindWikilink = ast.NewNodeKind("Wikilink")

func (n *wikilinkNode) Kind() ast.NodeKind { return kindWikilink }

// Dump implements Node.Dump.
func (n *wikilinkNode) Dump(source []byte, level int) {
	m := map[string]string{"Target": n.Target}
	if n.Label != "" {
		m["Label"] = n.Label
	}
	ast.DumpHelper(n, source, level, m, nil)
}

func (n *wikilinkNode) display() string {
	if n.Label != "" {
		return n.Label
	}
	return n.Target
}

// wikilinkParser is a goldmark inline parser triggered on '['.
type wikilinkParser struct{}

func (p *wikilinkParser) Trigger() []byte { return []byte{'['} }

func (p *wikilinkParser) Parse(parent ast.Node, block text.Reader, pc parser.Context) ast.Node {
	line, _ := block.PeekLine()
	// Need at least [[x]]
	if len(line) < 5 || line[0] != '[' || line[1] != '[' {
		return nil
	}
	rest := line[2:]
	idx := bytes.Index(rest, []byte("]]"))
	if idx < 0 {
		return nil
	}
	content := string(rest[:idx])
	// Target must not contain ']' (the first ']' is part of the closing ']]').
	if bytes.IndexByte([]byte(content), ']') >= 0 {
		return nil
	}
	var target, label string
	if pipe := bytes.IndexByte([]byte(content), '|'); pipe >= 0 {
		if bytes.IndexByte([]byte(content[pipe+1:]), '|') >= 0 {
			return nil
		}
		target, label = content[:pipe], content[pipe+1:]
	} else {
		target = content
	}
	// Targets and labels are trimmed so the resolver key, the stored
	// links row, and the rendered text always agree. An empty target
	// after trimming stays literal text — it is never a dead link or a
	// silent drop.
	target = strings.TrimSpace(target)
	label = strings.TrimSpace(label)
	if target == "" {
		return nil
	}
	block.Advance(idx + 4)
	return &wikilinkNode{Target: target, Label: label}
}

// wikilinkRenderer renders wikilinkNode via the resolver captured at
// construction time.
type wikilinkRenderer struct {
	resolve func(target string) (string, bool)
}

func (r *wikilinkRenderer) RegisterFuncs(reg renderer.NodeRendererFuncRegisterer) {
	reg.Register(kindWikilink, r.render)
}

func (r *wikilinkRenderer) render(w util.BufWriter, source []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		return ast.WalkContinue, nil
	}
	n, ok := node.(*wikilinkNode)
	if !ok {
		return ast.WalkContinue, nil
	}
	href, resolved := "", false
	if r.resolve != nil {
		href, resolved = r.resolve(n.Target)
	}
	if !resolved {
		_, _ = w.WriteString(`<span class="wikilink dead">`)
		_, _ = w.Write(util.EscapeHTML([]byte(n.display())))
		_, _ = w.WriteString(`</span>`)
		return ast.WalkContinue, nil
	}
	_, _ = w.WriteString(`<a href="`)
	_, _ = w.Write(util.EscapeHTML([]byte(href)))
	_, _ = w.WriteString(`" class="wikilink" data-path="`)
	_, _ = w.Write(util.EscapeHTML([]byte(extractPath(href))))
	_, _ = w.WriteString(`">`)
	_, _ = w.Write(util.EscapeHTML([]byte(n.display())))
	_, _ = w.WriteString(`</a>`)
	return ast.WalkContinue, nil
}

// extractPath pulls the path query parameter from an href for the
// data-path attribute used by the client-side click handler.
func extractPath(href string) string {
	u, err := url.Parse(href)
	if err != nil {
		return ""
	}
	return u.Query().Get("path")
}

// wikilinkExtension registers the inline parser and node renderer.
type wikilinkExtension struct {
	resolve func(target string) (string, bool)
}

func (e *wikilinkExtension) Extend(m goldmark.Markdown) {
	m.Parser().AddOptions(parser.WithInlineParsers(
		util.Prioritized(&wikilinkParser{}, 100),
	))
	m.Renderer().AddOptions(renderer.WithNodeRenderers(
		util.Prioritized(&wikilinkRenderer{resolve: e.resolve}, 100),
	))
}

// Wikilink is one extracted [[target]] or [[target|label]] occurrence.
type Wikilink struct {
	Target string
	Label  string
}

// extractMD mirrors the renderer's parser setup so extraction and
// rendering can never disagree: code spans and code blocks are never
// linkified, targets never span lines, and targets are trimmed the same
// way in both paths.
var extractMD = sync.OnceValue(func() goldmark.Markdown {
	return goldmark.New(
		goldmark.WithExtensions(extension.GFM, extension.Footnote, &wikilinkExtension{}),
		goldmark.WithParserOptions(parser.WithAutoHeadingID()),
	)
})

// ExtractWikilinks returns the wikilinks in src in document order,
// deduplicated by target (the first occurrence's label wins).
func ExtractWikilinks(src []byte) []Wikilink {
	doc := extractMD().Parser().Parse(text.NewReader(src))
	var out []Wikilink
	seen := make(map[string]bool)
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		w, ok := n.(*wikilinkNode)
		if !ok || seen[w.Target] {
			return ast.WalkContinue, nil
		}
		seen[w.Target] = true
		out = append(out, Wikilink{Target: w.Target, Label: w.Label})
		return ast.WalkContinue, nil
	})
	return out
}
