package resources

import (
	"errors"
	"strings"
	"testing"
)

func TestDefaultPolicy_allowsTypicalUploads(t *testing.T) {
	p := DefaultPolicy()
	cases := []struct {
		name string
		size int64
		mime string
	}{
		{"photo.jpg", 1 << 20, "image/jpeg"},
		{"doc.pdf", 5 << 20, "application/pdf"},
		{"clip.mp4", 20 << 20, "video/mp4"},
		{"song.mp3", 8 << 20, "audio/mpeg"},
		{"note.txt", 100, "text/plain"},
	}
	for _, c := range cases {
		got, err := p.Validate(c.name, c.size, c.mime)
		if err != nil {
			t.Errorf("Validate(%q, %d, %q) = err %v, want nil", c.name, c.size, c.mime, err)
			continue
		}
		if got != c.name {
			t.Errorf("Validate name = %q, want %q", got, c.name)
		}
	}
}

func TestDefaultPolicy_rejectsExecutable(t *testing.T) {
	p := DefaultPolicy()
	if _, err := p.Validate("evil.exe", 1000, "application/x-msdownload"); !errors.Is(err, ErrBadMIME) {
		t.Errorf("err = %v, want ErrBadMIME", err)
	}
}

func TestPolicy_sizeLimit(t *testing.T) {
	p := Policy{MaxSize: 1024}
	if _, err := p.Validate("a.jpg", 1025, "image/jpeg"); !errors.Is(err, ErrTooLarge) {
		t.Errorf("err = %v, want ErrTooLarge", err)
	}
	if _, err := p.Validate("a.jpg", 1024, "image/jpeg"); err != nil {
		t.Errorf("exact limit should pass, got %v", err)
	}
}

func TestPolicy_zeroMaxSizeUsesDefault(t *testing.T) {
	p := Policy{} // MaxSize=0 → DefaultMaxSize
	if _, err := p.Validate("a.jpg", DefaultMaxSize+1, "image/jpeg"); !errors.Is(err, ErrTooLarge) {
		t.Errorf("err = %v, want ErrTooLarge", err)
	}
}

func TestPolicy_mimePrefixWildcard(t *testing.T) {
	p := Policy{AllowedMIME: []string{"image/*"}}
	if _, err := p.Validate("a.png", 100, "image/png"); err != nil {
		t.Errorf("image/png should pass image/*, got %v", err)
	}
	if _, err := p.Validate("a.mp4", 100, "video/mp4"); !errors.Is(err, ErrBadMIME) {
		t.Errorf("video/mp4 should fail image/*, got %v", err)
	}
}

func TestPolicy_emptyMIMEListAllowsAll(t *testing.T) {
	p := Policy{AllowedMIME: nil}
	if _, err := p.Validate("anything.xyz", 100, "application/x-foo"); err != nil {
		t.Errorf("empty allowlist should accept all, got %v", err)
	}
}

func TestPolicy_emptyMIMEStringRejected(t *testing.T) {
	p := DefaultPolicy()
	if _, err := p.Validate("a.jpg", 100, ""); !errors.Is(err, ErrBadMIME) {
		t.Errorf("err = %v, want ErrBadMIME", err)
	}
}

func TestPolicy_mimeCaseInsensitive(t *testing.T) {
	p := Policy{AllowedMIME: []string{"IMAGE/JPEG"}}
	if _, err := p.Validate("a.jpg", 100, "image/jpeg"); err != nil {
		t.Errorf("case mismatch should pass, got %v", err)
	}
}

func TestSanitizeName_stripsPath(t *testing.T) {
	cases := map[string]string{
		"../../etc/passwd":  "passwd",
		"/absolute/path.png": "path.png",
		`C:\windows\evil.exe`: "evil.exe",
		"normal.txt":         "normal.txt",
		"nested/dir/file.md": "file.md",
	}
	for in, want := range cases {
		if got := SanitizeName(in, 0); got != want {
			t.Errorf("SanitizeName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSanitizeName_stripsLeadingDots(t *testing.T) {
	if got := SanitizeName(".hidden", 0); got != "hidden" {
		t.Errorf("got %q, want %q", got, "hidden")
	}
	if got := SanitizeName("..", 0); got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

func TestSanitizeName_replacesControlChars(t *testing.T) {
	if got := SanitizeName("a\x00b\x1fc.txt", 0); got != "a_b_c.txt" {
		t.Errorf("got %q", got)
	}
}

func TestSanitizeName_truncatesToMax(t *testing.T) {
	long := strings.Repeat("a", 300) + ".jpg"
	got := SanitizeName(long, 10)
	if len(got) > 10 {
		t.Errorf("len(%q) = %d, want <= 10", got, len(got))
	}
	if got != strings.Repeat("a", 10) {
		t.Errorf("got %q, want %q", got, strings.Repeat("a", 10))
	}
}

func TestSanitizeName_multibyteSafe(t *testing.T) {
	// "héllo" is 6 bytes (é = 2 bytes). max=3 should give "hé" (3 bytes).
	got := SanitizeName("héllo.txt", 4)
	// 4 bytes: h(1) é(2) l(1) = "hél"
	if got != "hél" {
		t.Errorf("got %q (%d bytes), want %q", got, len(got), "hél")
	}
}

func TestSanitizeName_emptyInput(t *testing.T) {
	if got := SanitizeName("", 0); got != "" {
		t.Errorf("got %q, want empty", got)
	}
	if got := SanitizeName("...", 0); got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

func TestSanitizeName_trailingDotSpace(t *testing.T) {
	if got := SanitizeName("file. ", 0); got != "file" {
		t.Errorf("got %q, want %q", got, "file")
	}
}

func TestValidate_emptyNameAfterSanitize(t *testing.T) {
	p := DefaultPolicy()
	if _, err := p.Validate("...", 100, "image/jpeg"); !errors.Is(err, ErrBadName) {
		t.Errorf("err = %v, want ErrBadName", err)
	}
}

func TestSniffMIME(t *testing.T) {
	cases := map[string]string{
		"photo.jpg":  "image/jpeg",
		"photo.jpeg": "image/jpeg",
		"doc.pdf":    "application/pdf",
		"clip.mp4":   "video/mp4",
		"song.mp3":   "audio/mpeg",
		"note.txt":   "text/plain; charset=utf-8",
		"unknown.xyz": "application/octet-stream",
		"noext":      "application/octet-stream",
	}
	for name, want := range cases {
		if got := SniffMIME(name); got != want {
			t.Errorf("SniffMIME(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestExt(t *testing.T) {
	if got := Ext("Photo.JPG"); got != ".jpg" {
		t.Errorf("got %q, want %q", got, ".jpg")
	}
	if got := Ext("noext"); got != "" {
		t.Errorf("got %q, want empty", got)
	}
}
