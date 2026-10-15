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
	"log/slog"
	"os/exec"
	"sync"
	"time"
)

const (
	defaultWindow  = 60 * time.Second
	commandTimeout = 30 * time.Second
)

// Op is one mutation to record in the next commit message.
type Op struct {
	Action string // write, delete, restore
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
