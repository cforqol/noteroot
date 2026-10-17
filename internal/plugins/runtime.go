package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/yuin/gopher-lua"

	"noteroot/internal/platform"
)

// Timeouts bound one invocation. Every plugin call runs inside one of
// these; nothing a plugin does can occupy the server longer.
const (
	DefaultCommandTimeout  = 5 * time.Second
	DefaultHookTimeout     = 5 * time.Second
	DefaultLoadTimeout     = 10 * time.Second
	capabilityTimeout      = 5 * time.Second
	eventQueueSize         = 64
	defaultBreakerStrikes  = 3
	defaultBreakerCooldown = 30 * time.Second
)

// ErrBreakerOpen is returned when the circuit breaker is open: the
// plugin failed repeatedly and is cooling down. It counts as plugin
// failure, never server failure.
var ErrBreakerOpen = errors.New("plugin circuit open")

// ErrNotLoaded is returned when the plugin has no live VM (load
// failed or it was closed).
var ErrNotLoaded = errors.New("plugin not loaded")

// ErrUnknownCommand is returned when a loaded plugin has no command
// by that name — a client mistake, mapped to 400 by the HTTP layer.
var ErrUnknownCommand = errors.New("unknown command")

// WriteRequest is a plugin-originated note write, carrying the hop
// depth of the event chain that caused it so the bus's depth limit
// terminates runaway chains.
type WriteRequest struct {
	Actor   string
	Origin  string
	Depth   int
	Path    string
	Content string
}

// NoteStore is the note capability, provided by the HTTP layer (it
// owns the write-path bookkeeping: identity minting, search index,
// git flusher, audit+bus emission).
type NoteStore interface {
	Read(ctx context.Context, path string) (string, error)
	Write(ctx context.Context, wr WriteRequest) error
}

// ErrNotFound is returned by NoteStore.Read for a missing path.
var ErrNotFound = errors.New("note not found")

// breaker is a consecutive-failure circuit: strikes accumulate, the
// circuit opens at the threshold, half-opens after a cooldown, and
// closes on the first success.
type breaker struct {
	mu       sync.Mutex
	strikes  int
	openedAt time.Time
	cooldown time.Duration
}

func newBreaker() *breaker {
	return &breaker{cooldown: defaultBreakerCooldown}
}

func (b *breaker) allow() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.strikes < defaultBreakerStrikes {
		return true
	}
	return time.Since(b.openedAt) >= b.cooldown
}

func (b *breaker) fail() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.strikes++
	if b.strikes >= defaultBreakerStrikes {
		b.openedAt = time.Now()
	}
}

func (b *breaker) success() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.strikes = 0
	b.openedAt = time.Time{}
}

func (b *breaker) open() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.strikes >= defaultBreakerStrikes && time.Since(b.openedAt) < b.cooldown
}

// Plugin is one loaded (or failed-to-load) plugin: its manifest, its
// single Lua VM guarded by mu, its breaker, and its event queue.
//
// Concurrency contract: mu serializes ALL VM use (entry, hooks,
// commands) — one VM, one runner. The bus never touches the VM: it
// only enqueues into evq (non-blocking, drop-on-full), so a slow or
// wedged plugin can delay itself and nothing else.
//
// invCtx is the context for the current invokeLocked call; capability
// functions (luaNoteWrite, luaKVSet, …) derive their own contexts from
// it so a blocking Go call inherits the outer timeout and is cancelled
// promptly when the hook/command deadline fires — or when stopCtx is
// cancelled by Close(). invCtx is only valid while p.mu is held.
type Plugin struct {
	Meta   Manifest
	Dir    string
	Grants []platform.Grant

	mu       sync.Mutex
	L        *lua.LState
	commands map[string]*lua.LFunction
	depth    int // event depth while a hook runs; capability writes add 1
	br       *breaker
	loadErr  string
	invCtx   context.Context // current invocation ctx; set by invokeLocked

	hooks atomic.Pointer[map[string][]*lua.LFunction]

	sendMu     sync.Mutex
	evq        chan platform.Event
	closed     bool
	stopCtx    context.Context    // cancelled by Close()
	stopCancel context.CancelFunc // cancels stopCtx

	loadTimeout time.Duration
	hookTimeout time.Duration
	cmdTimeout  time.Duration

	log   *slog.Logger
	store NoteStore
	db    *platform.DB
	slug  string
}

func newPlugin(f Found, grants []platform.Grant, slug string, store NoteStore, db *platform.DB, log *slog.Logger) *Plugin {
	stopCtx, stopCancel := context.WithCancel(context.Background())
	return &Plugin{
		Meta:        f.Manifest,
		Dir:         f.Dir,
		Grants:      grants,
		commands:    map[string]*lua.LFunction{},
		br:          newBreaker(),
		evq:         make(chan platform.Event, eventQueueSize),
		loadTimeout: DefaultLoadTimeout,
		hookTimeout: DefaultHookTimeout,
		cmdTimeout:  DefaultCommandTimeout,
		invCtx:      context.Background(), // safe zero: only used while mu is held
		stopCtx:     stopCtx,
		stopCancel:  stopCancel,
		log:         log.With("plugin", f.Manifest.Name, "notebook", slug),
		store:       store,
		db:          db,
		slug:        slug,
	}
}

// pluginOrigin is the event origin/actor attribution for this plugin:
// the bus, the event table, and self-suppression all key off it.
func pluginOrigin(name string) string { return "plugin:" + name }

// Load builds the VM and runs the entry script. Failures leave the
// plugin inert (loadErr set) — never fatal to the notebook.
func (p *Plugin) Load() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.resetLocked()
}

// resetLocked (re)builds the VM and re-runs the entry script. Caller
// holds p.mu. Used at load and after a timeout/panic tears the VM down.
func (p *Plugin) resetLocked() error {
	if p.L != nil {
		p.L.Close()
		p.L = nil
	}
	p.commands = map[string]*lua.LFunction{}
	p.hooks.Store(nil) // stale hooks must not survive a rebuild

	L, err := newSandbox(p)
	if err != nil {
		p.loadErr = err.Error()
		return fmt.Errorf("sandbox: %w", err)
	}
	p.L = L

	ctx, cancel := context.WithTimeout(p.stopCtx, p.loadTimeout)
	defer cancel()
	prev := p.invCtx
	p.invCtx = ctx // entry-script capabilities inherit the load deadline
	L.SetContext(ctx)
	err = L.DoFile(p.entryPath())
	L.SetContext(context.Background())
	p.invCtx = prev
	if err != nil {
		p.loadErr = err.Error()
		p.L.Close()
		p.L = nil
		p.hooks.Store(nil)
		return fmt.Errorf("entry: %w", err)
	}
	p.loadErr = ""
	return nil
}

func (p *Plugin) entryPath() string {
	entry, err := p.Meta.Validate(p.Dir)
	if err != nil { // validated at Scan; belt and braces
		return ""
	}
	return entry
}

// invokeLocked is the single path every VM execution takes: breaker
// check, context deadline, panic recovery, teardown-and-rebuild on
// timeout or panic, breaker accounting. Caller holds p.mu.
//
// The invocation context is derived from p.stopCtx so that Close()
// cancels it immediately, and from the per-call timeout so that Lua's
// context-poll and capability Go calls both respect it. The live
// context is published to p.invCtx while the call runs so capability
// functions can derive their own child contexts from it.
func (p *Plugin) invokeLocked(timeout time.Duration, what string, fn func(L *lua.LState) (lua.LValue, error)) (lua.LValue, error) {
	if p.L == nil {
		return lua.LNil, ErrNotLoaded
	}
	if !p.br.allow() {
		return lua.LNil, ErrBreakerOpen
	}
	// Derive from stopCtx so Close() unblocks a Lua call that is stuck
	// inside a blocking Go capability (note.write, kv.set, …).
	ctx, cancel := context.WithTimeout(p.stopCtx, timeout)
	defer cancel()

	p.invCtx = ctx
	defer func() { p.invCtx = context.Background() }()

	var (
		val      = lua.LNil
		err      error
		panicked bool
	)
	p.L.SetContext(ctx)
	func() {
		defer func() {
			if r := recover(); r != nil {
				panicked = true
				err = fmt.Errorf("panic: %v", r)
			}
		}()
		val, err = fn(p.L)
	}()
	if p.L != nil {
		p.L.SetContext(context.Background())
	}

	timedOut := ctx.Err() != nil
	// If the plugin is shutting down (stopCtx cancelled), do not tear
	// the VM down — it is about to be closed cleanly by Close().
	stopping := p.stopCtx.Err() != nil
	if (timedOut || panicked) && !stopping {
		p.br.fail()
		p.log.Warn("plugin invocation torn down",
			"what", what, "timeout", timedOut, "panic", panicked, "err", err)
		if rErr := p.resetLocked(); rErr != nil {
			p.log.Error("plugin reload failed", "err", rErr, "load_error", p.loadErr)
		}
	}
	if timedOut {
		if stopping {
			return lua.LNil, fmt.Errorf("%s: %w", what, context.Canceled)
		}
		return lua.LNil, fmt.Errorf("%s: %w", what, context.DeadlineExceeded)
	}
	if panicked {
		return lua.LNil, fmt.Errorf("%s: %v", what, err)
	}
	if err != nil {
		if !stopping {
			p.br.fail()
		}
		return lua.LNil, fmt.Errorf("%s: %w", what, err)
	}
	if !stopping {
		p.br.success()
	}
	return val, nil
}

// callFn pushes fn(args...) and pops one result. Caller holds p.mu.
func callFn(L *lua.LState, fn *lua.LFunction, args ...lua.LValue) (lua.LValue, error) {
	if err := L.CallByParam(lua.P{Fn: fn, NRet: 1, Protect: true}, args...); err != nil {
		return lua.LNil, err
	}
	v := L.Get(-1)
	L.Pop(1)
	return v, nil
}

// RunCommand executes a registered command with JSON args and returns
// a JSON-marshalable result.
func (p *Plugin) RunCommand(ctx context.Context, cmd string, argsJSON []byte) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.L == nil {
		return nil, ErrNotLoaded
	}
	fn, ok := p.commands[cmd]
	if !ok {
		return nil, fmt.Errorf("%w %q", ErrUnknownCommand, cmd)
	}
	var args any
	if len(argsJSON) > 0 {
		if err := json.Unmarshal(argsJSON, &args); err != nil {
			return nil, fmt.Errorf("bad args: %w", err)
		}
	}
	p.depth = 0
	defer func() { p.depth = 0 }()
	v, err := p.invokeLocked(p.cmdTimeout, "command "+cmd, func(L *lua.LState) (lua.LValue, error) {
		return callFn(L, fn, valueToLua(L, args))
	})
	if err != nil {
		return nil, err
	}
	return luaToValue(p.L, v)
}

// Enqueue hands an event to the plugin without ever blocking the bus.
// A full queue drops the event (logged): delivery is best-effort
// extension, never a constraint on writes.
func (p *Plugin) Enqueue(ev platform.Event) {
	hooks := p.hooks.Load()
	if hooks == nil {
		return
	}
	if _, ok := (*hooks)[ev.Type]; !ok {
		return
	}
	p.sendMu.Lock()
	defer p.sendMu.Unlock()
	if p.closed {
		return
	}
	select {
	case p.evq <- ev:
	default:
		p.log.Warn("plugin event queue full, dropping", "type", ev.Type, "path", ev.Path)
	}
}

// eventLoop serializes hook delivery for this plugin. Started once at
// load; exits when evq is closed.
func (p *Plugin) eventLoop() {
	for ev := range p.evq {
		p.handleEvent(ev)
	}
}

func (p *Plugin) handleEvent(ev platform.Event) {
	// Self-suppression: a plugin never receives events it caused. The
	// bus also suppresses by subscription, but one subscription fans
	// out to every plugin here, so the check is per plugin.
	if ev.Origin == pluginOrigin(p.Meta.Name) || ev.Actor == pluginOrigin(p.Meta.Name) {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.L == nil {
		return
	}
	hooks := p.hooks.Load()
	if hooks == nil {
		return
	}
	fns := (*hooks)[ev.Type]
	p.depth = ev.Depth
	defer func() { p.depth = 0 }()
	for _, fn := range fns {
		_, err := p.invokeLocked(p.hookTimeout, "hook "+ev.Type, func(L *lua.LState) (lua.LValue, error) {
			return callFn(L, fn, eventTable(L, ev))
		})
		if err != nil {
			p.log.Warn("plugin hook failed", "event", ev.Type, "path", ev.Path, "err", err)
		}
	}
}

// Info describes a plugin for the management API.
type Info struct {
	Name        string   `json:"name"`
	Version     string   `json:"version"`
	Description string   `json:"description,omitempty"`
	Permissions []string `json:"permissions"`
	Risk        string   `json:"risk"`
	Enabled     bool     `json:"enabled"`
	Loaded      bool     `json:"loaded"`
	Problem     string   `json:"problem,omitempty"`
	Commands    []string `json:"commands,omitempty"`
	Hooks       []string `json:"hooks,omitempty"`
}

// Info snapshots plugin state. Caller holds p.mu.
func (p *Plugin) info() Info {
	info := Info{
		Name:        p.Meta.Name,
		Version:     p.Meta.Version,
		Description: p.Meta.Description,
		Permissions: append([]string{}, p.Meta.Permissions...),
		Risk:        riskOf(p.Grants),
		Enabled:     p.Meta.IsEnabled(),
		Loaded:      p.L != nil,
		Problem:     p.loadErr,
	}
	for cmd := range p.commands {
		info.Commands = append(info.Commands, cmd)
	}
	if hooks := p.hooks.Load(); hooks != nil {
		for typ := range *hooks {
			info.Hooks = append(info.Hooks, typ)
		}
	}
	return info
}

// Close stops the event loop and the VM. Safe to call twice.
func (p *Plugin) Close() {
	p.sendMu.Lock()
	if p.closed {
		p.sendMu.Unlock()
		return
	}
	p.closed = true
	close(p.evq)
	p.sendMu.Unlock()

	// Cancel first: an in-flight invocation blocked inside a capability
	// Go call sees its derived context die and returns, releasing p.mu
	// promptly instead of making Close wait out capabilityTimeout.
	if p.stopCancel != nil {
		p.stopCancel()
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if p.L != nil {
		p.L.Close()
		p.L = nil
	}
	p.hooks.Store(nil)
}
