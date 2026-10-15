package resources

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// Transformer produces derived outputs (thumbnails, poster frames) from
// stored blobs. The interface is deliberately narrow so a vips-backed
// implementation can replace the ffmpeg one without touching callers.
type Transformer interface {
	// ResizeImage produces a JPEG thumbnail of the source image, scaled
	// to fit within maxW × maxH while preserving aspect ratio. Returns
	// the output bytes and the detected source dimensions.
	ResizeImage(ctx context.Context, src string, maxW, maxH int) ([]byte, int, int, error)
	// VideoPoster extracts a representative frame from a video as JPEG.
	// seekSec controls the timestamp (0 = default heuristic).
	VideoPoster(ctx context.Context, src string, seekSec float64) ([]byte, error)
	// ProbeImage returns the pixel dimensions of an image file.
	ProbeImage(ctx context.Context, src string) (int, int, error)
	// ProbeVideo returns the duration in seconds of a video file.
	ProbeVideo(ctx context.Context, src string) (float64, error)
	// Available reports whether the underlying tools are present.
	Available() bool
}

// ErrTransformUnsupported is returned when a transform kind is not
// supported for the given source type.
var ErrTransformUnsupported = errors.New("transform not supported")

// FFmpegTransformer uses ffmpeg/ffprobe for both stills and video.
// vips is preferred for stills (EXIF, ICC, HEIC, AVIF, lower memory)
// but is optional; ffmpeg is the universal fallback.
type FFmpegTransformer struct {
	FFmpeg  string // path to ffmpeg binary
	FFprobe string // path to ffprobe binary
}

// NewFFmpegTransformer locates the binaries on PATH. Missing binaries do
// not error — Available() reports the truth at call time.
func NewFFmpegTransformer() *FFmpegTransformer {
	ff, _ := exec.LookPath("ffmpeg")
	fb, _ := exec.LookPath("ffprobe")
	return &FFmpegTransformer{FFmpeg: ff, FFprobe: fb}
}

func (t *FFmpegTransformer) Available() bool {
	return t.FFmpeg != "" && t.FFprobe != ""
}

// maxResizeDim caps thumbnail dimensions to keep ffmpeg memory bounded.
const maxResizeDim = 4096

// ResizeImage scales src to fit within maxW × maxH (either may be 0 =
// unconstrained on that axis, but never exceeds maxResizeDim). Output is
// JPEG quality 85. Returns the JPEG bytes and source width/height.
func (t *FFmpegTransformer) ResizeImage(ctx context.Context, src string, maxW, maxH int) ([]byte, int, int, error) {
	if !t.Available() {
		return nil, 0, 0, errors.New("ffmpeg not available")
	}
	if maxW < 0 || maxH < 0 || maxW > maxResizeDim || maxH > maxResizeDim {
		return nil, 0, 0, fmt.Errorf("resize dimensions out of range (max %d)", maxResizeDim)
	}

	srcW, srcH, err := t.ProbeImage(ctx, src)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("probe: %w", err)
	}

	// Build the scale filter. Use ffmpeg expressions to never upscale:
	// target = min(source, max), then force_original_aspect_ratio=decrease
	// preserves ratio within those bounds. "-2" means auto (multiple of 2).
	var vf string
	switch {
	case maxW > 0 && maxH > 0:
		vf = fmt.Sprintf("scale='min(iw,%d)':'min(ih,%d)':force_original_aspect_ratio=decrease", maxW, maxH)
	case maxW > 0:
		vf = fmt.Sprintf("scale='min(iw,%d)':-2", maxW)
	case maxH > 0:
		vf = fmt.Sprintf("scale=-2:'min(ih,%d)'", maxH)
	default:
		vf = "scale=iw:ih" // no-op copy
	}

	out, err := t.runFFmpeg(ctx,
		"-i", src,
		"-vf", vf,
		"-frames:v", "1",
		"-q:v", "2", // ~JPEG quality 85
		"-f", "image2pipe",
		"-vcodec", "mjpeg",
		"pipe:1",
	)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("resize: %w", err)
	}
	return out, srcW, srcH, nil
}

// VideoPoster extracts a frame at seekSec. If seekSec <= 0 it picks 10%
// into the video (a common heuristic that avoids black intros).
func (t *FFmpegTransformer) VideoPoster(ctx context.Context, src string, seekSec float64) ([]byte, error) {
	if !t.Available() {
		return nil, errors.New("ffmpeg not available")
	}

	if seekSec <= 0 {
		dur, err := t.ProbeVideo(ctx, src)
		if err == nil && dur > 0 {
			seekSec = dur * 0.1
		} else {
			seekSec = 1.0
		}
	}

	out, err := t.runFFmpeg(ctx,
		"-ss", strconv.FormatFloat(seekSec, 'f', 3, 64),
		"-i", src,
		"-frames:v", "1",
		"-q:v", "2",
		"-f", "image2pipe",
		"-vcodec", "mjpeg",
		"pipe:1",
	)
	if err != nil {
		return nil, fmt.Errorf("poster: %w", err)
	}
	if len(out) == 0 {
		return nil, errors.New("poster: empty output")
	}
	return out, nil
}

// ProbeImage returns pixel dimensions using ffprobe.
func (t *FFmpegTransformer) ProbeImage(ctx context.Context, src string) (int, int, error) {
	w, h, err := t.probeStream(ctx, src, []string{"width", "height"})
	if err != nil {
		return 0, 0, err
	}
	return w, h, nil
}

// ProbeVideo returns duration in seconds using ffprobe.
func (t *FFmpegTransformer) ProbeVideo(ctx context.Context, src string) (float64, error) {
	out, err := t.runFFprobe(ctx,
		"-v", "error",
		"-show_entries", "format=duration",
		"-of", "default=noprint_wrappers=1:nokey=1",
		src,
	)
	if err != nil {
		return 0, err
	}
	s := strings.TrimSpace(string(out))
	if s == "" || s == "N/A" {
		return 0, errors.New("no duration")
	}
	return strconv.ParseFloat(s, 64)
}

func (t *FFmpegTransformer) probeStream(ctx context.Context, src string, keys []string) (int, int, error) {
	showEntries := "stream=" + strings.Join(keys, ",")
	out, err := t.runFFprobe(ctx,
		"-v", "error",
		"-select_streams", "v:0",
		"-show_entries", showEntries,
		"-of", "csv=p=0",
		src,
	)
	if err != nil {
		return 0, 0, err
	}
	parts := strings.Split(strings.TrimSpace(string(out)), ",")
	if len(parts) < 2 {
		return 0, 0, fmt.Errorf("unexpected probe output: %q", out)
	}
	w, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil {
		return 0, 0, fmt.Errorf("parse width: %w", err)
	}
	h, err := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err != nil {
		return 0, 0, fmt.Errorf("parse height: %w", err)
	}
	return w, h, nil
}

// runFFmpeg executes ffmpeg with the given args and returns stdout.
func (t *FFmpegTransformer) runFFmpeg(ctx context.Context, args ...string) ([]byte, error) {
	return t.run(ctx, t.FFmpeg, args...)
}

// runFFprobe executes ffprobe with the given args and returns stdout.
func (t *FFmpegTransformer) runFFprobe(ctx context.Context, args ...string) ([]byte, error) {
	return t.run(ctx, t.FFprobe, args...)
}

// run executes a binary and returns stdout. stderr is captured into the
// error on failure.
func (t *FFmpegTransformer) run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if name == "" {
		return nil, errors.New("binary path empty")
	}
	cmd := exec.CommandContext(ctx, name, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if len(msg) > 200 {
			msg = msg[:200]
		}
		return nil, fmt.Errorf("%s: %w: %s", filepath.Base(name), err, msg)
	}
	return stdout.Bytes(), nil
}

// DetectKind classifies a blob as "image", "video", or "other" based on
// its MIME type. The HTTP layer uses this to pick the right transform.
func DetectKind(mime string) string {
	switch {
	case strings.HasPrefix(mime, "image/"):
		return "image"
	case strings.HasPrefix(mime, "video/"):
		return "video"
	default:
		return "other"
	}
}

// IsImageExt / IsVideoExt classify by file extension when MIME is unknown.
func IsImageExt(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".jpg", ".jpeg", ".png", ".gif", ".webp", ".bmp", ".avif", ".heic", ".tiff", ".tif":
		return true
	}
	return false
}

func IsVideoExt(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".mp4", ".webm", ".mov", ".avi", ".mkv", ".m4v", ".mpg", ".mpeg":
		return true
	}
	return false
}

// TempDirFor returns a temp directory suitable for transform scratch
// files. The caller must remove it.
func TempDirFor() (string, func(), error) {
	dir, err := os.MkdirTemp("", "nr-transform-*")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	return dir, cleanup, nil
}
