package httpapi

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"noteroot/internal/config"
)

// shaOf is the content address postResource will store under.
func shaOf(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func TestEmbedMedia_replacesVideoAndAudioAnchors(t *testing.T) {
	vidSHA := strings.Repeat("a", 64)
	audSHA := strings.Repeat("b", 64)
	imgSHA := strings.Repeat("c", 64)
	in := []byte(fmt.Sprintf(
		`<p><a href="/n/work/resources/%s" rel="nofollow">clip.mp4</a>`+
			`<a href="/n/work/resources/%s" rel="nofollow">song.mp3</a>`+
			`<a href="/n/work/resources/%s" rel="nofollow">pic.png</a></p>`,
		vidSHA, audSHA, imgSHA))
	mimes := map[string]string{
		vidSHA: "video/mp4",
		audSHA: "audio/mpeg",
		imgSHA: "image/png",
	}

	out := string(embedMedia(in, "work", mimes))

	wantV := fmt.Sprintf(
		`<video controls preload="metadata" width="640" height="360" poster="/n/work/resources/%s/poster" src="/n/work/resources/%s"><a href="/n/work/resources/%s">clip.mp4</a></video>`,
		vidSHA, vidSHA, vidSHA)
	if !strings.Contains(out, wantV) {
		t.Errorf("video player missing; out = %s", out)
	}
	wantA := fmt.Sprintf(
		`<audio controls preload="metadata" src="/n/work/resources/%s"><a href="/n/work/resources/%s">song.mp3</a></audio>`,
		audSHA, audSHA)
	if !strings.Contains(out, wantA) {
		t.Errorf("audio player missing; out = %s", out)
	}
	// Images and downloads stay plain links.
	if !strings.Contains(out, fmt.Sprintf(`<a href="/n/work/resources/%s" rel="nofollow">pic.png</a>`, imgSHA)) {
		t.Errorf("image link should stay an anchor; out = %s", out)
	}
}

func TestEmbedMedia_leavesUnrelatedLinksAlone(t *testing.T) {
	vidSHA := strings.Repeat("a", 64)
	other := fmt.Sprintf(`<a href="/n/other/resources/%s">clip.mp4</a>`, vidSHA)
	sameSlugNoMime := fmt.Sprintf(`<a href="/n/work/resources/%s">clip.mp4</a>`, strings.Repeat("d", 64))

	for name, in := range map[string]string{"other notebook": other, "unknown sha": sameSlugNoMime} {
		out := string(embedMedia([]byte(in), "work", map[string]string{vidSHA: "video/mp4"}))
		if out != in {
			t.Errorf("%s: got %q, want unchanged", name, out)
		}
	}
	if out := embedMedia([]byte(other), "work", nil); string(out) != other {
		t.Errorf("empty mime map must be a no-op, got %q", out)
	}
}

// mediaMimeMap returns nil (no state DB work) for notes without resource
// links; a link to an uncommitted sha resolves to an empty map, which the
// caller treats the same way.
func TestMediaMimeMap_fastPathAndUncommitted(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, resourceFixture(t))
	ctx := t.Context()

	if got := srv.mediaMimeMap(ctx, "work", []byte("<p>hello world</p>")); got != nil {
		t.Errorf("plain note: got %v, want nil (no DB open)", got)
	}

	ghost := fmt.Sprintf(`<p><a href="/n/work/resources/%s">x</a></p>`, strings.Repeat("e", 64))
	if got := srv.mediaMimeMap(ctx, "work", []byte(ghost)); len(got) != 0 {
		t.Errorf("uncommitted sha: got %v, want empty map", got)
	}
	if got := srv.mediaMimeMap(ctx, "missing", []byte(ghost)); got != nil {
		t.Errorf("unknown notebook: got %v, want nil", got)
	}
}

// The full pipeline: upload video+audio, link them from a note, and the
// rendered HTML must carry players — after sanitization, so these tags
// only exist because the media pass added them.
func TestNote_viewEmbedsUploadedMedia(t *testing.T) {
	vid := append([]byte("\x00\x00\x00\x18ftypmp42"), bytes.Repeat([]byte("f"), 64)...)
	aud := bytes.Repeat([]byte("ID3\x04"), 32)
	txt := []byte("plain download")

	vidSHA, audSHA, txtSHA := shaOf(vid), shaOf(aud), shaOf(txt)
	note := fmt.Sprintf(
		"Watch [clip.mp4](/n/work/resources/%s)\n\nListen [song.mp3](/n/work/resources/%s)\n\nGet [notes.txt](/n/work/resources/%s)\n",
		vidSHA, audSHA, txtSHA)
	srv := newTestServer(t, config.AuthNone, notesFixture(t, map[string]string{"media.md": note}))

	for _, up := range []struct {
		name, mime string
		body       []byte
	}{
		{"clip.mp4", "video/mp4", vid},
		{"song.mp3", "audio/mpeg", aud},
		{"notes.txt", "text/plain", txt},
	} {
		if rec := postResource(t, srv, "work", up.name, up.mime, up.body); rec.Code != http.StatusCreated {
			t.Fatalf("upload %s = %d, body %s", up.name, rec.Code, rec.Body.String())
		}
	}

	rec := doReq(t, srv, http.MethodGet, notePath("media.md"), "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("note status = %d", rec.Code)
	}
	var resp map[string]any
	decodeJSON(t, rec, &resp)
	html, _ := resp["html"].(string)

	for _, want := range []string{
		fmt.Sprintf(`<video controls preload="metadata" width="640" height="360" poster="/n/work/resources/%s/poster"`, vidSHA),
		fmt.Sprintf(`<audio controls preload="metadata" src="/n/work/resources/%s">`, audSHA),
	} {
		if !strings.Contains(html, want) {
			t.Errorf("html missing %q\nhtml = %s", want, html)
		}
	}
	// The text file must stay a plain anchor (a download link).
	if !strings.Contains(html, fmt.Sprintf(`href="/n/work/resources/%s"`, txtSHA)) {
		t.Errorf("text resource link lost; html = %s", html)
	}
	if strings.Contains(html, "<video") && strings.Contains(html, fmt.Sprintf(`poster="/n/work/resources/%s/poster"`, txtSHA)) {
		t.Error("text file must not get a video player")
	}
}

// Seek support: HTML5 players ask for byte ranges. The blob must come
// back 206 with correct Content-Range, and conditional requests honour
// the ETag.
func TestResource_servesRanges(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, resourceFixture(t))
	content := make([]byte, 1000)
	for i := range content {
		content[i] = byte(i % 251)
	}
	rec := postResource(t, srv, "work", "blob.txt", "text/plain", content)
	if rec.Code != http.StatusCreated {
		t.Fatalf("upload = %d", rec.Code)
	}
	var up map[string]any
	decodeJSON(t, rec, &up)
	sha, _ := up["sha"].(string)
	if sha == "" {
		t.Fatal("no sha")
	}
	target := "/n/work/resources/" + sha

	full := doReq(t, srv, http.MethodGet, target, "", nil)
	if full.Code != http.StatusOK {
		t.Fatalf("full GET = %d", full.Code)
	}
	if full.Body.Len() != len(content) {
		t.Errorf("full body len = %d, want %d", full.Body.Len(), len(content))
	}
	if etag := full.Header().Get("ETag"); etag != `"`+sha+`"` {
		t.Errorf("ETag = %q, want %q", etag, `"`+sha+`"`)
	}

	rangeReq := httptest.NewRequest(http.MethodGet, target, nil)
	rangeReq.Header.Set("Range", "bytes=10-19")
	rangeRec := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(rangeRec, rangeReq)
	if rangeRec.Code != http.StatusPartialContent {
		t.Fatalf("range status = %d, want 206", rangeRec.Code)
	}
	if got, want := rangeRec.Header().Get("Content-Range"), fmt.Sprintf("bytes 10-19/%d", len(content)); got != want {
		t.Errorf("Content-Range = %q, want %q", got, want)
	}
	if !bytes.Equal(rangeRec.Body.Bytes(), content[10:20]) {
		t.Errorf("range body = %v, want %v", rangeRec.Body.Bytes(), content[10:20])
	}

	cond := httptest.NewRequest(http.MethodGet, target, nil)
	cond.Header.Set("If-None-Match", `"`+sha+`"`)
	condRec := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(condRec, cond)
	if condRec.Code != http.StatusNotModified {
		t.Errorf("If-None-Match = %d, want 304", condRec.Code)
	}
}

func TestRemapResourceLinks_unknownNotebookNameBecomesSlug(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, notesFixture(t, map[string]string{"a.md": "x"}))
	sha := strings.Repeat("a", 64)
	in := []byte(`<img src="/n/testing/resources/` + sha + `"><a href="/n/work/resources/` + sha + `">k</a>`)
	got := string(srv.remapResourceLinks(in, "work"))
	if want := `<img src="/n/work/resources/` + sha + `"><a href="/n/work/resources/` + sha + `">k</a>`; got != want {
		t.Fatalf("got %s", got)
	}
}
