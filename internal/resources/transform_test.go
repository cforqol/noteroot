package resources

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// hasFFmpeg reports whether ffmpeg is on PATH (skips video tests if not).
func hasFFmpeg(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not available")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe not available")
	}
}

// makeTestImage generates a solid-color PNG at the given dimensions.
func makeTestImage(t *testing.T, dir string, w, h int) string {
	t.Helper()
	out := filepath.Join(dir, "test.png")
	cmd := exec.Command("ffmpeg", "-y",
		"-f", "lavfi", "-i", "color=c=blue:s=640x480",
		"-frames:v", "1",
		out,
	)
	// Override dimensions via size
	if w != 640 || h != 480 {
		cmd = exec.Command("ffmpeg", "-y",
			"-f", "lavfi", "-i", "color=c=blue",
			"-vf", "scale="+itoa(w)+":"+itoa(h),
			"-frames:v", "1",
			out,
		)
	}
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg make image: %v\n%s", err, output)
	}
	return out
}

// makeTestVideo generates a short test video.
func makeTestVideo(t *testing.T, dir string) string {
	t.Helper()
	out := filepath.Join(dir, "test.mp4")
	cmd := exec.Command("ffmpeg", "-y",
		"-f", "lavfi", "-i", "testsrc=duration=2:size=320x240:rate=10",
		"-pix_fmt", "yuv420p",
		out,
	)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg make video: %v\n%s", err, output)
	}
	return out
}

func itoa(n int) string {
	return fmtInt(n)
}

func fmtInt(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	if neg {
		return "-" + string(b)
	}
	return string(b)
}

func TestFFmpegTransformer_availability(t *testing.T) {
	tr := NewFFmpegTransformer()
	// On CI without ffmpeg this returns false; the test still runs.
	t.Logf("available = %v (ffmpeg=%q ffprobe=%q)", tr.Available(), tr.FFmpeg, tr.FFprobe)
}

func TestFFmpegTransformer_resizeImage(t *testing.T) {
	hasFFmpeg(t)
	tr := NewFFmpegTransformer()
	dir := t.TempDir()
	src := makeTestImage(t, dir, 640, 480)

	ctx := context.Background()
	out, w, h, err := tr.ResizeImage(ctx, src, 320, 240)
	if err != nil {
		t.Fatalf("ResizeImage: %v", err)
	}
	if w != 640 || h != 480 {
		t.Errorf("probe dims = %dx%d, want 640x480", w, h)
	}
	if len(out) == 0 {
		t.Fatal("empty output")
	}
	// Output should be a JPEG (SOI marker).
	if len(out) < 3 || out[0] != 0xFF || out[1] != 0xD8 {
		t.Errorf("output is not JPEG (first bytes: %x)", out[:min(4, len(out))])
	}
}

func TestFFmpegTransformer_resizePreservesAspectRatio(t *testing.T) {
	hasFFmpeg(t)
	tr := NewFFmpegTransformer()
	dir := t.TempDir()
	src := makeTestImage(t, dir, 800, 600) // 4:3

	ctx := context.Background()
	// Fit within 400x400 → should become 400x300 (4:3 preserved).
	out, _, _, err := tr.ResizeImage(ctx, src, 400, 400)
	if err != nil {
		t.Fatal(err)
	}
	// Write to temp and probe the output dimensions.
	outPath := filepath.Join(dir, "out.jpg")
	if err := os.WriteFile(outPath, out, 0o644); err != nil {
		t.Fatal(err)
	}
	ow, oh, err := tr.ProbeImage(ctx, outPath)
	if err != nil {
		t.Fatal(err)
	}
	if ow != 400 || oh != 300 {
		t.Errorf("output dims = %dx%d, want 400x300", ow, oh)
	}
}

func TestFFmpegTransformer_resizeNoUpscale(t *testing.T) {
	hasFFmpeg(t)
	tr := NewFFmpegTransformer()
	dir := t.TempDir()
	src := makeTestImage(t, dir, 100, 80) // small

	ctx := context.Background()
	// Request 400x400 — should NOT upscale beyond 100x80.
	out, _, _, err := tr.ResizeImage(ctx, src, 400, 400)
	if err != nil {
		t.Fatal(err)
	}
	outPath := filepath.Join(dir, "out.jpg")
	if err := os.WriteFile(outPath, out, 0o644); err != nil {
		t.Fatal(err)
	}
	ow, oh, err := tr.ProbeImage(ctx, outPath)
	if err != nil {
		t.Fatal(err)
	}
	if ow > 100 || oh > 80 {
		t.Errorf("upscaled: got %dx%d, source 100x80", ow, oh)
	}
}

func TestFFmpegTransformer_resizeOnlyWidth(t *testing.T) {
	hasFFmpeg(t)
	tr := NewFFmpegTransformer()
	dir := t.TempDir()
	src := makeTestImage(t, dir, 640, 480)

	ctx := context.Background()
	out, _, _, err := tr.ResizeImage(ctx, src, 320, 0)
	if err != nil {
		t.Fatal(err)
	}
	outPath := filepath.Join(dir, "out.jpg")
	if err := os.WriteFile(outPath, out, 0o644); err != nil {
		t.Fatal(err)
	}
	ow, _, err := tr.ProbeImage(ctx, outPath)
	if err != nil {
		t.Fatal(err)
	}
	if ow != 320 {
		t.Errorf("width = %d, want 320", ow)
	}
}

func TestFFmpegTransformer_resizeRejectsOversize(t *testing.T) {
	tr := NewFFmpegTransformer()
	dir := t.TempDir()
	src := filepath.Join(dir, "x.jpg")
	_ = os.WriteFile(src, []byte("x"), 0o644)

	_, _, _, err := tr.ResizeImage(context.Background(), src, 9999, 9999)
	if err == nil {
		t.Error("want error for dimensions > maxResizeDim")
	}
}

func TestFFmpegTransformer_videoPoster(t *testing.T) {
	hasFFmpeg(t)
	tr := NewFFmpegTransformer()
	dir := t.TempDir()
	src := makeTestVideo(t, dir)

	ctx := context.Background()
	out, err := tr.VideoPoster(ctx, src, 0) // auto-seek
	if err != nil {
		t.Fatalf("VideoPoster: %v", err)
	}
	if len(out) == 0 {
		t.Fatal("empty poster")
	}
	if out[0] != 0xFF || out[1] != 0xD8 {
		t.Errorf("poster is not JPEG")
	}
}

func TestFFmpegTransformer_videoPosterExplicitSeek(t *testing.T) {
	hasFFmpeg(t)
	tr := NewFFmpegTransformer()
	dir := t.TempDir()
	src := makeTestVideo(t, dir)

	ctx := context.Background()
	out, err := tr.VideoPoster(ctx, src, 1.0)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) == 0 {
		t.Fatal("empty poster")
	}
}

func TestFFmpegTransformer_probeVideo(t *testing.T) {
	hasFFmpeg(t)
	tr := NewFFmpegTransformer()
	dir := t.TempDir()
	src := makeTestVideo(t, dir)

	dur, err := tr.ProbeVideo(context.Background(), src)
	if err != nil {
		t.Fatal(err)
	}
	// testsrc duration=2 → ~2 seconds (allow some tolerance).
	if dur < 1.5 || dur > 2.5 {
		t.Errorf("duration = %f, want ~2.0", dur)
	}
}

func TestFFmpegTransformer_probeImage(t *testing.T) {
	hasFFmpeg(t)
	tr := NewFFmpegTransformer()
	dir := t.TempDir()
	src := makeTestImage(t, dir, 300, 200)

	w, h, err := tr.ProbeImage(context.Background(), src)
	if err != nil {
		t.Fatal(err)
	}
	if w != 300 || h != 200 {
		t.Errorf("dims = %dx%d, want 300x200", w, h)
	}
}

func TestFFmpegTransformer_resizeNonExistentFile(t *testing.T) {
	hasFFmpeg(t)
	tr := NewFFmpegTransformer()
	_, _, _, err := tr.ResizeImage(context.Background(), "/nonexistent.jpg", 100, 100)
	if err == nil {
		t.Error("want error for missing file")
	}
}

func TestDetectKind(t *testing.T) {
	cases := map[string]string{
		"image/png":      "image",
		"image/jpeg":     "image",
		"video/mp4":      "video",
		"video/webm":     "video",
		"application/pdf": "other",
		"text/plain":     "other",
		"":               "other",
	}
	for mime, want := range cases {
		if got := DetectKind(mime); got != want {
			t.Errorf("DetectKind(%q) = %q, want %q", mime, got, want)
		}
	}
}

func TestIsImageExt(t *testing.T) {
	for _, name := range []string{"a.jpg", "a.jpeg", "a.PNG", "a.webp", "a.heic"} {
		if !IsImageExt(name) {
			t.Errorf("IsImageExt(%q) = false", name)
		}
	}
	for _, name := range []string{"a.mp4", "a.pdf", "a.txt", "noext"} {
		if IsImageExt(name) {
			t.Errorf("IsImageExt(%q) = true", name)
		}
	}
}

func TestIsVideoExt(t *testing.T) {
	for _, name := range []string{"a.mp4", "a.webm", "a.MOV", "a.mkv"} {
		if !IsVideoExt(name) {
			t.Errorf("IsVideoExt(%q) = false", name)
		}
	}
	for _, name := range []string{"a.jpg", "a.pdf", "noext"} {
		if IsVideoExt(name) {
			t.Errorf("IsVideoExt(%q) = true", name)
		}
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
