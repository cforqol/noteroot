package httpapi

import (
	"context"
	"fmt"
	"regexp"
	"strings"
)

// mediaEmbedSize is the fixed player size for embedded video
// (style.css keeps max-width:100% so small screens shrink it).
const (
	mediaEmbedWidth  = 640
	mediaEmbedHeight = 360
)

// resourceLinkRe matches an anchor whose href points at a notebook
// resource blob. Built per slug so an unrelated /n/other/... link can
// never be rewritten.
func resourceLinkRe(slug string) *regexp.Regexp {
	return regexp.MustCompile(`(?s)<a\b[^>]*\bhref="(/n/` + regexp.QuoteMeta(slug) + `/resources/([a-f0-9]{64}))"[^>]*>(.*?)</a>`)
}

var foreignResourceRe = regexp.MustCompile(`/n/([A-Za-z0-9_-]+)/resources/([a-f0-9]{64})`)

// remapResourceLinks points resource links at this notebook when they name
// a notebook that does not exist (typically the display name or an old
// slug, left behind by a rename). Links to other real notebooks are kept.
func (s *Server) remapResourceLinks(html []byte, slug string) []byte {
	if !strings.Contains(string(html), "/resources/") {
		return html
	}
	return foreignResourceRe.ReplaceAllFunc(html, func(m []byte) []byte {
		sub := foreignResourceRe.FindSubmatch(m)
		if string(sub[1]) == slug {
			return m
		}
		if _, ok := s.reg.Get(string(sub[1])); ok {
			return m
		}
		return []byte("/n/" + slug + "/resources/" + string(sub[2]))
	})
}

// embedMedia rewrites anchors that point at video/audio resource blobs
// into <video>/<audio> players. It runs after sanitization (bluemonday
// strips these elements), so the markup it adds is final; anything not
// in mimeBySHA — images, downloads, unknown blobs — is left untouched.
// The label of the original anchor is kept as the element's fallback
// content for browsers that cannot play the format.
func embedMedia(html []byte, slug string, mimeBySHA map[string]string) []byte {
	if len(mimeBySHA) == 0 || !strings.Contains(string(html), "/n/"+slug+"/resources/") {
		return html
	}
	re := resourceLinkRe(slug)
	return re.ReplaceAllFunc(html, func(m []byte) []byte {
		sub := re.FindSubmatch(m)
		if sub == nil {
			return m
		}
		url := string(sub[1])
		sha := string(sub[2])
		label := string(sub[3])
		mime := mimeBySHA[sha]
		switch {
		case strings.HasPrefix(mime, "video/"):
			return []byte(fmt.Sprintf(
				`<video controls preload="metadata" width="%d" height="%d" poster="%s/poster" src="%s"><a href="%s">%s</a></video>`,
				mediaEmbedWidth, mediaEmbedHeight, url, url, url, label))
		case strings.HasPrefix(mime, "audio/"):
			return []byte(fmt.Sprintf(
				`<audio controls preload="metadata" src="%s"><a href="%s">%s</a></audio>`,
				url, url, label))
		case mime == "application/pdf":
			return []byte(fmt.Sprintf(
				`<iframe class="embed-pdf" src="%s" title="%s"><a href="%s">%s</a></iframe>`,
				url, label, url, label))
		default:
			return m
		}
	})
}

// mediaMimeMap reports which of the resource shas referenced in html
// are committed blobs and what MIME each has. It returns nil (no state
// DB work) when the HTML mentions no resource links at all, so opening
// an ordinary note costs one substring scan.
func (s *Server) mediaMimeMap(ctx context.Context, slug string, html []byte) map[string]string {
	if !strings.Contains(string(html), "/n/"+slug+"/resources/") {
		return nil
	}
	re := resourceLinkRe(slug)
	seen := map[string]bool{}
	for _, sub := range re.FindAllSubmatch(html, -1) {
		seen[string(sub[2])] = true
	}
	if len(seen) == 0 {
		return nil
	}
	nb, ok := s.reg.Get(slug)
	if !ok {
		return nil
	}
	_, journal, cleanup, err := s.openResources(ctx, nb)
	if err != nil {
		s.log.Warn("media embed: resources unavailable", "notebook", slug, "err", err)
		return nil
	}
	defer cleanup()
	shas := make([]string, 0, len(seen))
	for sha := range seen {
		shas = append(shas, sha)
	}
	out, err := journal.MIMEBySHA(shas...)
	if err != nil {
		s.log.Warn("media embed: journal read failed", "notebook", slug, "err", err)
		return nil
	}
	return out
}
