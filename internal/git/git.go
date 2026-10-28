// Package git coalesces vault mutations into autocommits.
//
// One Flusher per notebook accumulates dirty marks and commits at most once
// per window. A commit failure is reported to the caller but must never fail
// the HTTP write that caused it — the write is truth, git is recovery.
// Identity travels on the command line (-c flags) so autocommits work in
// repos without configured user.email, and operator commits keep their own.
package git

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	defaultWindow  = 60 * time.Second
	commandTimeout = 30 * time.Second
)

// Op is one mutation to record in the next commit message.
type Op struct {
	Action string // write, create, move, delete, restore, import
	Path   string
	Actor  string
}

// Flusher coalesces dirty marks into periodic commits of dir.
type Flusher struct {
	mu      sync.Mutex
	dir     string
	log     *slog.Logger
	pending []Op
	timer   *time.Timer
	window  time.Duration
	closed  bool
}

// New returns a Flusher for the git repo rooted at dir.
func New(dir string, log *slog.Logger) *Flusher {
	return &Flusher{dir: dir, log: log, window: defaultWindow}
}

// Init creates a git repository in dir when one does not exist yet.
// A failure only means autocommit history will be missing — the caller
// logs it and carries on, because the write path never depends on git.
func Init(dir string) error {
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "init", "-q")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git init: %w\n%s", err, out)
	}
	return nil
}

// MarkDirty records a mutation. The first mark after a flush starts the
// coalescing window; a timer commit fires once per window at most.
func (f *Flusher) MarkDirty(op Op) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return
	}
	f.pending = append(f.pending, op)
	if f.timer == nil {
		f.timer = time.AfterFunc(f.window, func() {
			if err := f.Flush(); err != nil {
				f.log.Warn("git autocommit failed", "dir", f.dir, "err", err)
			}
		})
	}
}

// Flush commits pending mutations now. Empty pending is a no-op — git must
// never record empty commits.
func (f *Flusher) Flush() error {
	f.mu.Lock()
	if len(f.pending) == 0 {
		f.mu.Unlock()
		return nil
	}
	ops := f.pending
	f.pending = nil
	if f.timer != nil {
		f.timer.Stop()
		f.timer = nil
	}
	f.mu.Unlock()

	msg := commitMessage(ops)
	if err := f.run("add", "-A"); err != nil {
		f.restore(ops)
		return err
	}
	if clean, err := f.clean(); err != nil {
		f.restore(ops)
		return err
	} else if clean {
		return nil
	}
	if err := f.run("commit", "-q", "-m", msg); err != nil {
		f.restore(ops)
		return err
	}
	f.log.Info("git autocommit", "dir", f.dir, "ops", len(ops))
	return nil
}

// restore puts ops that failed to commit back into pending and re-arms the
// window timer, so the next mark or Flush retries them. Nothing is ever
// silently dropped — the write is truth, the commit must eventually follow.
func (f *Flusher) restore(ops []Op) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pending = append(ops, f.pending...)
	if f.timer == nil && !f.closed {
		f.timer = time.AfterFunc(f.window, func() {
			if err := f.Flush(); err != nil {
				f.log.Warn("git autocommit failed", "dir", f.dir, "err", err)
			}
		})
	}
}

// clean reports whether the tree has nothing to commit. Marks made obsolete
// by later writes (net-zero change) must not produce empty commits.
func (f *Flusher) clean() (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", f.dir, "status", "--porcelain")
	out, err := cmd.Output()
	if err != nil {
		return false, fmt.Errorf("git status: %w", err)
	}
	return len(out) == 0, nil
}

// Close stops the timer and flushes anything pending.
func (f *Flusher) Close() error {
	f.mu.Lock()
	f.closed = true
	f.mu.Unlock()
	return f.Flush()
}

func commitMessage(ops []Op) string {
	first := ops[0]
	actor := first.Actor
	if actor == "" {
		actor = "unknown"
	}
	msg := fmt.Sprintf("noteroot: %s %s %s", actor, first.Action, first.Path)
	if len(ops) > 1 {
		msg += fmt.Sprintf(" [+%d more]", len(ops)-1)
	}
	return msg
}

func (f *Flusher) run(args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	full := append([]string{"-C", f.dir,
		"-c", "user.name=noteroot",
		"-c", "user.email=noteroot@local",
	}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git %v: %w\n%s", args, err, out)
	}
	return nil
}

// Commit represents a git commit.
type Commit struct {
	Hash      string
	ShortHash string
	Date      time.Time
	Author    string
	Message   string
	IsMerge   bool
}

// LogFile returns commits affecting a file, newest first.
func LogFile(dir, path string, limit int, before string) ([]Commit, bool, error) {
	args := []string{"log", "--oneline", "--follow", fmt.Sprintf("-%d", limit), "--pretty=format:%H|%h|%ct|%an|%s|%p"}
	if before != "" {
		args = append(args, before+"..")
	}
	args = append(args, "--", path)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		// If the file doesn't exist in history, return empty
		if strings.Contains(string(out), "does not exist") || strings.Contains(string(out), "bad revision") {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("git log: %w\n%s", err, out)
	}

	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return nil, false, nil
	}

	var commits []Commit
	for _, line := range lines {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "|", 6)
		if len(parts) < 6 {
			continue
		}
		ts, _ := strconv.ParseInt(parts[2], 10, 64)
		parents := strings.Fields(parts[5])
		commits = append(commits, Commit{
			Hash:      parts[0],
			ShortHash: parts[1],
			Date:      time.Unix(ts, 0),
			Author:    parts[3],
			Message:   parts[4],
			IsMerge:   len(parents) > 1,
		})
	}

	hasMore := len(commits) >= limit
	if hasMore {
		commits = commits[:limit]
	}
	return commits, hasMore, nil
}

// Bundle creates a git bundle of the entire repo and writes it to w.
// Returns ErrNoHistory when the repo has no commits yet.
func Bundle(dir string, w io.Writer) error {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "bundle", "create", "-", "--all")
	cmd.Stdout = w
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if strings.Contains(stderr.String(), "Refusing to create empty bundle") ||
			strings.Contains(stderr.String(), "nothing to bundle") {
			return ErrNoHistory
		}
		return fmt.Errorf("git bundle: %w\n%s", err, stderr.String())
	}
	return nil
}

// ErrNoHistory is returned by Bundle when the repo contains no commits.
var ErrNoHistory = fmt.Errorf("no git history")

// Unbundle applies a bundle file to the repo, grafting its history onto the
// local branches. The bundle must already exist at bundlePath (a temporary
// file the caller manages).
//
// Only a repo with no commits yet (unborn HEAD) is grafted: the bundle's
// refs are staged under refs/import/* (fetching directly into local branches
// is refused for the current branch of a non-bare repo) and then moved onto
// refs/heads/*, with HEAD repointed at the branch the bundle carried when its
// name differs from the local default (main vs master). A repo that already
// has commits keeps its own history — the bundle is dropped.
func Unbundle(dir, bundlePath string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// A repo that already has commits keeps its own history: nothing to graft.
	headErr := exec.CommandContext(ctx, "git", "-C", dir,
		"rev-parse", "--verify", "HEAD").Run()
	if headErr == nil {
		return nil
	}

	// Stage the bundle's branches under a temporary namespace.
	stage := exec.CommandContext(ctx, "git", "-C", dir,
		"fetch", bundlePath, "+refs/heads/*:refs/import/*")
	if out, err := stage.CombinedOutput(); err != nil {
		return fmt.Errorf("git fetch bundle: %w\n%s", err, out)
	}

	// List the staged refs with their commits.
	out, err := exec.CommandContext(ctx, "git", "-C", dir,
		"for-each-ref", "--format=%(refname) %(objectname)", "refs/import/").Output()
	if err != nil {
		return nil // Nothing staged; nothing to graft.
	}
	type stagedRef struct{ ref, sha string }
	var staged []stagedRef
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) == 2 {
			staged = append(staged, stagedRef{ref: parts[0], sha: parts[1]})
		}
	}
	if len(staged) == 0 {
		return nil
	}

	// Move each staged ref onto its local branch.
	var names []string
	for _, s := range staged {
		name := strings.TrimPrefix(s.ref, "refs/import/")
		names = append(names, name)
		if err := gitRun(ctx, dir, "update-ref", "refs/heads/"+name, s.sha); err != nil {
			return err
		}
		_ = gitRun(ctx, dir, "update-ref", "-d", s.ref) // clean the temp ref
	}

	// Point HEAD at a branch that actually has commits.
	target := pickBranch(names)
	if target == "" {
		return nil
	}
	return gitRun(ctx, dir, "symbolic-ref", "HEAD", "refs/heads/"+target)
}

// pickBranch prefers main, then master, then the first name.
func pickBranch(names []string) string {
	if len(names) == 0 {
		return ""
	}
	for _, want := range []string{"main", "master"} {
		for _, n := range names {
			if n == want {
				return n
			}
		}
	}
	return names[0]
}

// gitRun runs git in dir with the given args, returning a descriptive error.
func gitRun(ctx context.Context, dir string, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git %v: %w\n%s", args, err, out)
	}
	return nil
}

// DiffFile returns the unified diff between two commits for a file.
func DiffFile(dir, path, from, to string) (string, error) {
	args := []string{"diff", from, to, "--", path}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git diff: %w\n%s", err, out)
	}
	return string(out), nil
}

// GetCommit returns commit info for a given hash.
func GetCommit(dir, hash string) (*Commit, error) {
	args := []string{"show", "-s", "--pretty=format:%H|%h|%ct|%an|%s|%p", hash}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("git show: %w\n%s", err, out)
	}
	line := strings.TrimSpace(string(out))
	if line == "" {
		return nil, fmt.Errorf("commit not found")
	}
	parts := strings.SplitN(line, "|", 6)
	if len(parts) < 6 {
		return nil, fmt.Errorf("unexpected git show output")
	}
	ts, _ := strconv.ParseInt(parts[2], 10, 64)
	parents := strings.Fields(parts[5])
	return &Commit{
		Hash:      parts[0],
		ShortHash: parts[1],
		Date:      time.Unix(ts, 0),
		Author:    parts[3],
		Message:   parts[4],
		IsMerge:   len(parents) > 1,
	}, nil
}
