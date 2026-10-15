# Stage 4: Vim Mode Design

**Status**: In progress  
**Dependencies**: Stage 3 (write path, locks, crash pad) ✓  
**Scope**: Desktop vim mode with wasi-vim in sandboxed iframe, circuit-breaker fallback, buffer model

---

## 1. Overview

Stage 4 adds **real Vim** to the desktop editing experience via WebAssembly, while maintaining the existing textarea editor as a fallback for phones and failure cases. The key insight: typing latency stays local (no subprocess, no RPC), and the sandbox boundary prevents a Vim crash from taking down the app.

### Design Goals

1. **Real Vim behavior** — motions, text objects, registers, macros, `.` repeat work correctly
2. **Sandboxed isolation** — Vim runs in an iframe with postMessage; crashes degrade gracefully
3. **Zero server changes** — all vim logic lives client-side; backend sees only content saves
4. **Automatic fallback** — circuit breaker detects failures, switches to textarea, persists choice
5. **Desktop-optimized UX** — buffer model with drafts and swap, hidden on narrow viewports
6. **Progressive enhancement** — phones/tablets always use textarea; desktop gets vim when capable

### Non-Goals for Stage 4

- Neovim (planned fallback; wasi-vim ships first, nvim integration deferred to later patch)
- Plugin system (`.vimrc` support comes with stage 9 extensibility)
- Split windows or tabs (single buffer per iframe)
- Clipboard integration beyond browser APIs (no external clipboard daemon)
- Remote editing over SSH (noteroot is the server)

---

## 2. Architecture

### 2.1 Component Diagram

```
┌─────────────────────────────────────────────────────────────┐
│ Main Window (web/app.js)                                    │
│  ┌──────────────────────────────────────────────────────┐   │
│  │ Editor Controller                                    │   │
│  │  - Mode detection (desktop vs mobile)               │   │
│  │  - Circuit breaker state (vim-ok / vim-failed)      │   │
│  │  - Editor lifecycle (open/save/close)               │   │
│  │  - Buffer management (drafts, swap, crash pad)      │   │
│  └───┬──────────────────────────────────────────────┬───┘   │
│      │                                              │       │
│      ▼ (desktop + vim-ok)                          ▼ (fallback) │
│  ┌───────────────────────┐              ┌──────────────────┐│
│  │ VimEditor             │              │ TextareaEditor   ││
│  │  - Iframe sandbox     │              │ (stage 3)        ││
│  │  - postMessage bridge │              │  - Direct DOM    ││
│  │  - Health monitor     │              │  - Autosave      ││
│  └───┬───────────────────┘              └──────────────────┘│
│      │                                                       │
│  ┌───▼────────────────────────────────────────────┐         │
│  │ Sandboxed Iframe (web/vim-frame.html)          │         │
│  │  ┌──────────────────────────────────────────┐  │         │
│  │  │ wasi-vim.wasm                            │  │         │
│  │  │  - Compiled Vim 9.x (minimal config)    │  │         │
│  │  │  - WASI runtime (filesystem shim)       │  │         │
│  │  │  - Terminal backend (xterm.js adapter)  │  │         │
│  │  └──────────────────────────────────────────┘  │         │
│  │  ┌──────────────────────────────────────────┐  │         │
│  │  │ xterm.js                                 │  │         │
│  │  │  - Terminal rendering                    │  │         │
│  │  │  - Keyboard input capture                │  │         │
│  │  │  - ANSI/VT sequences                     │  │         │
│  │  └──────────────────────────────────────────┘  │         │
│  │  ┌──────────────────────────────────────────┐  │         │
│  │  │ Bridge (vim-bridge.js)                   │  │         │
│  │  │  - postMessage protocol                  │  │         │
│  │  │  - Content sync (parent ↔ vim)           │  │         │
│  │  │  - Command relay (save, close)           │  │         │
│  │  └──────────────────────────────────────────┘  │         │
│  └─────────────────────────────────────────────────┘         │
└─────────────────────────────────────────────────────────────┘
```

### 2.2 Mode Detection

Desktop vim mode is **opt-in with automatic fallback**:

1. **Viewport width** — vim mode requires ≥768px width (desktop/laptop)
2. **Capability check** — WebAssembly support, iframe sandbox, postMessage
3. **Circuit breaker state** — `localStorage["nr.vimMode"]`:
   - `"enabled"` (default on desktop) → try vim, fallback on error
   - `"disabled"` (user choice or repeated failures) → always use textarea
   - `"failed:N"` (N = failure count) → disabled after 3 consecutive failures
4. **User preference** — settings toggle to force vim on/off

**Narrow viewports** (phone/tablet portrait) always use textarea, regardless of preferences.

### 2.3 Editor Lifecycle

```javascript
// Unified interface
class Editor {
  async open(path, content, baseSha) { }
  async save() { }  // returns {content, changed}
  async close() { } // cleanup
  getContent() { }  // current state
  onDirty(cb) { }   // content changed callback
  focus() { }
}

// Implementations
class VimEditor extends Editor { /* iframe bridge */ }
class TextareaEditor extends Editor { /* direct DOM, stage 3 code */ }

// Controller picks implementation
async function openEditor() {
  const EditorImpl = shouldUseVim() ? VimEditor : TextareaEditor;
  editor = new EditorImpl();
  try {
    await editor.open(path, content, baseSha);
  } catch (e) {
    if (EditorImpl === VimEditor) {
      recordVimFailure();
      return openEditor(); // retry with textarea
    }
    throw e;
  }
}
```

---

## 3. Wasi-Vim Integration

### 3.1 WASM Build

**Source**: Official Vim repository, minimal build  
**Target**: `wasi` (WebAssembly System Interface)  
**Size budget**: ≤2 MB gzipped  
**Features**: Normal/visual/insert modes, motions, registers, macros; no GUI, no plugins initially

Build configuration (deferred to implementation; mockup for now):
```makefile
# Minimal wasi-vim build
./configure \
  --with-features=normal \
  --disable-gui \
  --disable-netbeans \
  --disable-channel \
  --enable-wasm \
  --prefix=/opt/vim-wasm

make LDFLAGS="-lwasi-emulated-mman"
```

**Delivery**: Static asset `web/vim/wasi-vim.wasm`, served with CORS headers, cached aggressively.

### 3.2 WASI Runtime

Vim expects a POSIX-like environment; WASI provides syscalls. The browser shim:

- **Filesystem**: In-memory VFS (single buffer file `/tmp/note.md`)
- **Terminal**: xterm.js adapter (stdin/stdout/stderr mapped to terminal I/O)
- **Clock**: `performance.now()` for timeouts
- **No network**: All network syscalls stubbed (Vim doesn't need them for local editing)

**Runtime library**: `@wasmer/wasi` or `@bjorn3/browser_wasi_shim` (evaluate both; pick lighter one).

### 3.3 Initialization Sequence

```javascript
// In iframe (vim-frame.html)
import { WASI } from '@wasmer/wasi';
import { Terminal } from 'xterm';

const term = new Terminal({ cursorBlink: true, fontSize: 14 });
term.open(document.getElementById('terminal'));

const wasi = new WASI({
  args: ['vim', '/tmp/note.md'],
  env: { TERM: 'xterm-256color' },
  preopens: { '/tmp': memfs }
});

const wasmModule = await WebAssembly.instantiateStreaming(
  fetch('/vim/wasi-vim.wasm'),
  { wasi_snapshot_preview1: wasi.wasiImport }
);

// Wire terminal I/O to WASI stdin/stdout
term.onData(data => wasi.stdin.write(data));
wasi.stdout.onData(data => term.write(data));

// Start Vim
wasi.start(wasmModule.instance);
```

### 3.4 Content Synchronization

**Parent → Iframe** (initial load):
```javascript
// Main window
vimFrame.contentWindow.postMessage({
  type: 'vim:load',
  path: 'projects/plan.md',
  content: '# Plan\n\nGoals...',
  baseSha: 'abc123'
}, '*');
```

**Iframe → Parent** (save request):
```javascript
// Iframe
window.parent.postMessage({
  type: 'vim:save',
  content: getCurrentBufferContent()
}, '*');
```

**Vim buffer extraction**: Hook `:w` command or poll Vim's buffer via `vimCommand('getline(1, "$")')` (if wasi-vim exposes a JS bridge; otherwise intercept writes to `/tmp/note.md` in the WASI filesystem).

---

## 4. Iframe Sandbox

### 4.1 Isolation Policy

```html
<iframe
  id="vim-frame"
  src="/vim-frame.html"
  sandbox="allow-scripts allow-same-origin"
  allow="clipboard-read; clipboard-write"
  style="width:100%; height:100%; border:0;"
></iframe>
```

**Sandbox attributes**:
- `allow-scripts` — run wasi-vim.wasm
- `allow-same-origin` — postMessage to parent (needed for `window.parent`)
- **No** `allow-top-navigation`, `allow-forms`, `allow-popups`

**CSP (Content Security Policy)** for iframe document:
```
script-src 'self' 'wasm-unsafe-eval';
style-src 'self' 'unsafe-inline';
connect-src 'self';
default-src 'none';
```

### 4.2 postMessage Protocol

**Commands** (parent → iframe):

| Type | Payload | Purpose |
|------|---------|---------|
| `vim:load` | `{path, content, baseSha}` | Initialize buffer |
| `vim:reload` | `{content, baseSha}` | External change detected |
| `vim:close` | `{}` | Cleanup and teardown |
| `vim:focus` | `{}` | Focus terminal |

**Events** (iframe → parent):

| Type | Payload | Purpose |
|------|---------|---------|
| `vim:ready` | `{}` | WASM loaded, Vim started |
| `vim:dirty` | `{}` | Buffer modified |
| `vim:save` | `{content}` | User triggered `:w` |
| `vim:error` | `{message, fatal}` | Runtime error (fatal triggers fallback) |
| `vim:heartbeat` | `{timestamp}` | Alive check (every 10s) |

**Security**: All messages validate origin (`event.origin === window.location.origin`).

### 4.3 Error Handling

Failure modes:

1. **WASM load failure** → immediate fallback to textarea, log to console
2. **Vim crash** (unhandled exception) → detect via missing heartbeat (30s timeout), fallback
3. **Sandbox escape attempt** → CSP violation, parent kills iframe, fallback
4. **Infinite loop** (Vim hangs) → no heartbeat, parent kills iframe after 30s, fallback

Circuit breaker increments failure counter on fatal errors; after 3 consecutive failures, vim mode disabled for this session (cleared on next app load or manual reset).

---

## 5. Circuit Breaker

### 5.1 State Machine

```
         ┌─────────────┐
    ┌────│   CLOSED    │ (vim enabled)
    │    │ (try vim)   │
    │    └──────┬──────┘
    │           │ error
    │           ▼
    │    ┌─────────────┐
    │    │    OPEN     │ (vim disabled)
    │    │ (use textarea)│
    │    └──────┬──────┘
    │           │ manual reset
    │           │ or new session
    └───────────┘
```

**Failure threshold**: 3 consecutive errors within 5 minutes  
**Reset conditions**: Manual user action, new browser session, or 24h timeout

**Storage**: `localStorage["nr.vimMode"]`
```json
{
  "state": "closed",
  "failures": 0,
  "lastFailure": 1698765432000,
  "disabledUntil": null
}
```

### 5.2 User Controls

Settings panel (new, stage 4):

```
Editor Mode
  ○ Automatic (vim on desktop, fallback on error)
  ○ Always use vim
  ○ Always use plain text editor

[✓] Show vim mode indicator

Status: Vim mode active, 0 recent failures
[Reset failure counter]
```

Indicator in editor toolbar: `<span class="editor-mode">vim</span>` or `<span class="editor-mode">plain</span>`.

---

## 6. Buffer and Swap Management

### 6.1 Draft Autosave

Desktop vim mode gains **draft files** (stage 3's crash pad remains for textarea mode).

**Storage**: `localStorage["nr.draft.{slug}.{path}"]`
```json
{
  "baseSha": "abc123",
  "content": "# Plan\n\nDraft content...",
  "savedAt": 1698765432000,
  "mode": "vim"
}
```

**Write frequency**: On Vim buffer change (debounced 2s), before closing iframe.

**Restore flow**:
1. User opens note
2. Check for draft newer than server version
3. If exists: banner with "Draft from [time]. [Restore] [Discard] [View diff]"
4. Restore → load into Vim, diff → side-by-side modal (future enhancement)

### 6.2 Swap Files (Vim `.swp`)

Vim's swap file mechanism works within the WASI in-memory filesystem (`/tmp/`). On close:

- **Clean exit** → swap deleted by Vim
- **Crash** → swap persists in WASI FS until iframe reload (then lost)

**Note**: WASI FS is ephemeral per iframe instance. Swap files do NOT persist across app reloads; draft autosave (above) is the durable recovery mechanism.

Rationale: Vim's swap is for within-session recovery (e.g., undo after a typo). Drafts handle cross-session recovery (browser crash, accidental close).

### 6.3 Conflict Handling

Same `If-Match` + force flow as stage 3:

1. Save attempt with `baseSha`
2. 412 response → external change detected
3. Banner: "Changed on server. [Reload] [Force save] [View diff]"
4. Reload → fetch server content, reload Vim buffer (`:e! /tmp/note.md` equivalent)
5. Force → `PUT?force=1`, update `baseSha`

Vim-specific: on reload, parent sends `vim:reload` message; iframe updates buffer without restarting Vim (preserves undo history, cursor position).

---

## 7. xterm.js Integration

### 7.1 Terminal Configuration

```javascript
const term = new Terminal({
  cursorBlink: true,
  cursorStyle: 'block',
  fontSize: 14,
  fontFamily: 'JetBrains Mono, Menlo, monospace',
  theme: {
    background: '#1e1e1e',
    foreground: '#d4d4d4',
    cursor: '#ffffff',
    // ... full theme in implementation
  },
  rows: 40,
  cols: 120,
  scrollback: 1000
});
```

**Addons**:
- `xterm-addon-fit` — auto-resize on window/iframe resize
- `xterm-addon-web-links` — clickable URLs (if Vim renders any)

### 7.2 Keyboard Handling

All keyboard input goes to xterm.js, which forwards to Vim via WASI stdin:

```javascript
term.onData(data => {
  wasi.stdin.write(new TextEncoder().encode(data));
});
```

**Special keys**: xterm.js handles escape sequences (arrows, F-keys, etc.) → Vim sees correct codes.

**Clipboard**:
- `Ctrl+C` / `Ctrl+V` in insert mode → browser clipboard
- `yy` / `p` in Vim → Vim's internal registers (WASI FS-backed if needed)

### 7.3 Rendering

Vim outputs ANSI/VT sequences → xterm.js renders → canvas/WebGL.

**Performance**: xterm.js uses canvas by default; WebGL renderer optional (evaluate if scrolling large files is slow).

---

## 8. Implementation Phases

### Phase 1: Foundation (MVP) — 2-3 days

**Goal**: Vim loads, edits a buffer, saves back to parent.

Tasks:
1. Create `web/vim-frame.html` (iframe document, xterm.js, wasi runtime)
2. Stub wasi-vim.wasm (mock; later build real Vim)
3. Implement `VimEditor` class in `app.js` (postMessage bridge)
4. Mode detection (desktop width check)
5. Simple save flow (`:w` → `vim:save` message → PUT)
6. Basic error handling (load failure → fallback to textarea)

**Acceptance**:
- Can open a note in vim mode (iframe with xterm)
- Can edit text (via WASI Vim or mock)
- `:w` saves to server
- Fallback to textarea on error

### Phase 2: Circuit Breaker & Swap — 1-2 days

**Goal**: Resilient fallback, draft recovery.

Tasks:
1. Circuit breaker state machine
2. Failure counter, localStorage persistence
3. Draft autosave (on dirty, debounced)
4. Draft restore banner
5. Settings UI (mode toggle)

**Acceptance**:
- 3 consecutive vim failures → automatic disable
- Draft saved on buffer change
- Restore draft on reopen

### Phase 3: Production Readiness — 2-3 days

**Goal**: Real wasi-vim.wasm, polished UX.

Tasks:
1. Build actual wasi-vim (or integrate pre-built binary)
2. Proper WASI FS setup (buffer file)
3. Heartbeat monitoring
4. Conflict detection UI
5. Terminal theming (dark/light mode match)
6. Keyboard shortcuts guide (help panel)
7. Performance testing (large files, rapid typing)

**Acceptance**:
- Real Vim motions work (hjkl, dd, ., macros)
- Handles 10k+ line files smoothly
- No memory leaks on open/close cycles

### Phase 4: Polish & Testing — 1-2 days

**Goal**: Edge cases, documentation, tests.

Tasks:
1. Unit tests (circuit breaker, mode detection)
2. E2E tests (playwright: open vim, edit, save, fallback)
3. Error message polish
4. Accessibility (screen reader announce mode switches)
5. Documentation (user guide, vim tips)

**Acceptance**:
- Test suite green
- User docs complete
- No console errors on happy path

---

## 9. UX Considerations

### 9.1 First-Time User Experience

**Desktop first visit**:
1. Open note → vim mode attempts
2. If WASM loads slow: "Loading editor..." spinner (max 3s)
3. Vim appears with brief tooltip: "Vim mode active. Press `i` to insert, `Esc` to command mode. [Learn more]"
4. Link to `/help/vim-guide` (new page, stage 4)

**Mobile first visit**:
1. Open note → plain textarea (no vim attempt)
2. No mention of vim mode (irrelevant on mobile)

### 9.2 Mode Indicator

Toolbar shows current mode:

```
┌─────────────────────────────────────────────────────┐
│ [Vim: NORMAL] ⎯  Locked by this tab  Saved 2m ago   │
│ [Save] [Close]                                       │
└─────────────────────────────────────────────────────┘
```

Modes:
- `[Vim: NORMAL]` — green badge
- `[Vim: INSERT]` — blue badge
- `[Plain text]` — gray badge (textarea mode)

Clicking badge → mode tooltip with help link.

### 9.3 Help and Onboarding

**Vim basics panel** (collapsible, in editor):

```
Vim Quick Reference
──────────────────
i     Enter insert mode
Esc   Return to normal mode
:w    Save
:q    Close (if saved)
dd    Delete line
yy    Copy line
p     Paste
u     Undo
Ctrl+R Redo

[Full vim cheatsheet →]
```

Link opens `/help/vim` (new static page with comprehensive guide).

### 9.4 Graceful Degradation

Failure scenarios:

| Condition | Behavior |
|-----------|----------|
| No WASM support | Textarea, no vim option shown |
| Vim load timeout (>5s) | Fallback to textarea, log error |
| Vim crash mid-edit | Auto-save draft, reload with textarea, banner: "Vim crashed, recovered draft" |
| Network offline | Vim works (fully client-side), saves queue until online |

---

## 10. Performance Targets

| Metric | Target | Method |
|--------|--------|--------|
| WASM load time (cold) | <2s @ 3G | Gzip, aggressive cache, HTTP/2 push |
| WASM load time (warm) | <100ms | Service worker cache |
| Input latency (typing) | <16ms (60 FPS) | Local rendering, no server roundtrip |
| Autosave latency | <500ms (debounced) | localStorage write |
| Memory footprint | <50 MB (Vim idle) | WASM heap, xterm buffer |
| Open note (vim mode) | <1s (with warm cache) | Parallel WASM load + content fetch |

**Large file handling**:
- Files >1 MB: warn user, offer plain text mode
- Files >5 MB: force plain text (Vim in WASM may struggle)

---

## 11. Security

### 11.1 Threat Model

**In-scope threats**:
1. Malicious note content → XSS in terminal (mitigated: xterm.js sanitizes ANSI)
2. WASM exploit → sandbox escape (mitigated: iframe sandbox, CSP)
3. Prototype pollution via postMessage (mitigated: message validation)

**Out-of-scope** (broader app security):
- CSRF (covered by stage 3)
- Auth bypass (covered by forward-auth)

### 11.2 Mitigations

1. **Message origin validation**: Every `postMessage` handler checks `event.origin`
2. **Strict CSP**: `script-src 'self' 'wasm-unsafe-eval'` (no `unsafe-inline`, no CDN)
3. **Input sanitization**: Content loaded into Vim never interpreted as HTML (rendered as plain text in terminal)
4. **Iframe sandbox**: Minimal permissions (`allow-scripts allow-same-origin` only)
5. **WASM integrity**: Sub-resource integrity (SRI) hash on wasi-vim.wasm

### 11.3 Audit Recommendations

- **Code review**: PostMessage handlers, WASI shim, circuit breaker logic
- **Fuzzing**: Malformed ANSI sequences, large files, rapid save/close cycles
- **Dependency audit**: `npm audit` for xterm.js, wasi runtime

---

## 12. Monitoring and Observability

### 12.1 Client-Side Metrics

Track in `localStorage["nr.vimMetrics"]` (aggregated, sent on app close or daily):

```json
{
  "vimLoads": 42,
  "vimLoadFailures": 2,
  "fallbackActivations": 2,
  "averageLoadTimeMs": 450,
  "totalEditingTimeMs": 1234567,
  "saveCount": 58,
  "crashCount": 1
}
```

Optional: Send to server endpoint `/api/v1/telemetry` (stage 9; privacy-respecting, opt-in only).

### 12.2 Error Logging

Errors logged to console with structured context:

```javascript
console.error('[VimEditor] Load failed', {
  reason: 'WASM instantiation error',
  wasmUrl: '/vim/wasi-vim.wasm',
  userAgent: navigator.userAgent,
  timestamp: Date.now()
});
```

No PII (paths, content) logged. Only metadata.

### 12.3 Debug Mode

Developer flag `localStorage["nr.debug"] = "1"`:
- Verbose console logs (message passing, buffer syncs)
- Vim frame visible even when hidden (for inspection)
- No circuit breaker (always retry vim)

---

## 13. Dependencies

### 13.1 New NPM Packages

| Package | Version | Purpose | Size |
|---------|---------|---------|------|
| `xterm` | ^5.3.0 | Terminal emulator | ~200 KB |
| `xterm-addon-fit` | ^0.8.0 | Auto-resize | ~5 KB |
| `@wasmer/wasi` | ^1.2.2 | WASI runtime | ~50 KB |

Total: ~255 KB (minified, pre-gzip). After gzip: ~80 KB.

### 13.2 Static Assets

| Asset | Size (gzipped) | Cache |
|-------|----------------|-------|
| `wasi-vim.wasm` | ~1.5 MB | 1 year |
| `xterm.css` | ~5 KB | 1 year |
| `vim-frame.html` | ~2 KB | 1 day |
| `vim-bridge.js` | ~10 KB | 1 day |

Total first load: ~1.6 MB. Subsequent loads: ~0 (cached).

### 13.3 Browser Requirements

**Minimum**:
- WebAssembly support (all modern browsers since 2017)
- ES6 (async/await, modules)
- postMessage API
- localStorage
- iframe sandbox

**Tested browsers**:
- Chrome 90+
- Firefox 88+
- Safari 14+
- Edge 90+

**Not supported**:
- IE11 (no WASM)
- Opera Mini (no WASM)

---

## 14. Testing Strategy

### 14.1 Unit Tests

**Scope**: Editor controller, circuit breaker, mode detection

Files: `web/vim-editor.test.js`, `web/circuit-breaker.test.js`

Sample tests:
- Circuit breaker transitions correctly on failures
- Mode detection returns vim on desktop, textarea on mobile
- postMessage protocol serialization/deserialization
- Draft autosave debouncing

**Runner**: Node.js native test runner (like linking tests)

### 14.2 Integration Tests

**Scope**: Iframe bridge, WASM load, save flow

Approach: Playwright headless browser

Sample scenarios:
- Load vim frame, verify terminal renders
- Type text in vim, verify `:w` triggers save
- Simulate WASM load failure, verify fallback
- Open note with draft, verify restore banner

### 14.3 E2E Tests

**Scope**: Full user flows

Scenarios:
1. Desktop user opens note → vim mode → edits → saves → closes
2. Desktop user encounters vim error → fallback → continues editing
3. Mobile user opens note → plain text mode (no vim attempt)
4. Draft recovery: edit, crash browser (close without save), reopen → restore

**Tool**: Playwright with fixtures (test notebook, pre-seeded notes)

### 14.4 Manual Testing Checklist

Before ship:
- [ ] Vim motions work (hjkl, w, b, $, ^, gg, G)
- [ ] Insert mode typing smooth (<16ms latency)
- [ ] Undo/redo work
- [ ] Copy/paste within Vim
- [ ] Browser clipboard (Cmd+C/V in insert mode)
- [ ] Save via `:w` and toolbar button
- [ ] Close via `:q` and toolbar button
- [ ] Conflict detection (edit same note in two tabs)
- [ ] Draft restore after browser crash
- [ ] Circuit breaker triggers after 3 failures
- [ ] Settings toggle vim mode on/off
- [ ] Narrow viewport → textarea mode
- [ ] Dark/light mode themes

---

## 15. Documentation Deliverables

### 15.1 User Documentation

**File**: `docs/user-guide/vim-mode.md`

Sections:
- What is Vim mode?
- When is it used? (desktop vs mobile)
- Basic Vim commands
- How to switch modes (settings)
- Troubleshooting (vim not loading, etc.)

### 15.2 Developer Documentation

**File**: `docs/dev/vim-architecture.md`

Sections:
- Component overview
- postMessage protocol spec
- Adding vim features
- Debugging vim issues
- Building wasi-vim from source

### 15.3 ADR (Architecture Decision Record)

**File**: `docs/adr/0004-vim-mode.md`

Decision: Use wasi-vim over embedded Neovim

Context: Need real Vim on desktop without server-side process

Alternatives considered:
- Embedded Neovim via subprocess → rejected (RPC latency, session management complexity)
- CodeMirror with vim bindings → rejected (not real Vim, motions incomplete)
- Pure JS Vim (vim.js) → rejected (unmaintained, incomplete)

Chosen: wasi-vim (Vim compiled to WASM)

Consequences:
- (+) Real Vim behavior, local latency
- (+) Sandboxed, no server deps
- (−) Larger bundle size (~1.5 MB)
- (−) WASM debugging harder than JS

---

## 16. Risks and Mitigations

| Risk | Impact | Probability | Mitigation |
|------|--------|-------------|------------|
| wasi-vim build complexity | High | Medium | Use pre-built binary first, defer custom build |
| WASM load time too slow | Medium | Low | Aggressive caching, lazy load, HTTP/2 push |
| Vim crashes frequently | High | Medium | Circuit breaker, automatic fallback, draft recovery |
| Browser incompatibility | Medium | Low | Feature detection, textarea fallback |
| Large bundle size | Medium | High | Code splitting, serve vim.wasm only on desktop |
| Users dislike vim | Low | Medium | Prominent settings toggle, clear help docs |

**Showstopper criteria**: If >20% of vim mode sessions fail, revert to textarea-only and defer vim to later stage.

---

## 17. Success Metrics

**Launch criteria** (all must pass):
- [ ] Vim mode works on 3 major browsers (Chrome, Firefox, Safari)
- [ ] Circuit breaker prevents infinite failure loops
- [ ] Draft recovery tested with simulated crashes
- [ ] E2E test suite green
- [ ] User documentation complete
- [ ] No P0 bugs in manual testing

**Post-launch KPIs** (measure after 2 weeks):
- Vim mode adoption rate (desktop sessions using vim vs textarea)
- Failure rate (vim errors / vim attempts)
- Average editing session duration (vim vs textarea)
- User feedback sentiment (via issue tracker)

**Success targets**:
- >60% of desktop users opt into vim mode
- <5% failure rate
- No increase in edit abandonment (suggests vim too hard)

---

## 18. Future Enhancements (Post-Stage 4)

Deferred to later stages:

1. **Neovim fallback** — alternative WASM backend for users who prefer nvim
2. **Plugin support** — load `.vimrc`, basic plugins (via WASI FS)
3. **Collaborative editing** — show other users' cursors in vim (requires CRDT, stage 9)
4. **Advanced clipboard** — system clipboard beyond browser APIs (via browser extension?)
5. **Split windows** — multiple buffers side-by-side (complex WASM UI change)
6. **Fuzzy finder** — `:Files`, `:Buffers` commands (integrate with notebook tree)
7. **Syntax highlighting** — beyond Vim's built-in (ship custom themes)
8. **Performance mode** — disable animations, reduce scrollback for low-end devices

---

## 19. Open Questions

1. **WASM binary source**: Build our own wasi-vim, or use existing project (e.g., `rhysd/vim.wasm`)? → **Decision**: Start with `vim.wasm` (pre-built), evaluate custom build if needed.

2. **Vim version**: Vim 9.x or 8.x? → **Decision**: 9.x (better performance, modern features).

3. **Offline support**: Should vim work fully offline (service worker cache)? → **Decision**: Yes, vim.wasm and xterm.js cached; only save requires network.

4. **Mobile landscape**: Enable vim on tablets in landscape mode (>768px)? → **Decision**: Yes, same desktop rules.

5. **Undo persistence**: Save Vim undo tree across sessions? → **Decision**: No (complex, low ROI); draft recovery is sufficient.

---

## 20. Implementation Checklist

### Phase 1: Foundation
- [ ] Create `web/vim-frame.html` with xterm.js
- [ ] Integrate WASI runtime (`@wasmer/wasi`)
- [ ] Load stub wasi-vim.wasm (or rhysd/vim.wasm)
- [ ] Implement `VimEditor` class in `app.js`
- [ ] postMessage bridge (load, save, close)
- [ ] Mode detection (viewport width)
- [ ] Basic error handling (fallback to textarea)

### Phase 2: Circuit Breaker
- [ ] Circuit breaker state machine
- [ ] localStorage persistence (`nr.vimMode`)
- [ ] Failure counter, threshold logic
- [ ] Draft autosave (debounced, localStorage)
- [ ] Draft restore banner
- [ ] Settings UI (mode toggle)

### Phase 3: Production Polish
- [ ] Real wasi-vim integration (build or pre-built)
- [ ] WASI filesystem setup (`/tmp/note.md`)
- [ ] Heartbeat monitoring (30s timeout)
- [ ] Conflict detection UI (reload, force save)
- [ ] Terminal theming (dark/light)
- [ ] Help panel (vim basics)
- [ ] Performance testing (large files)

### Phase 4: Testing & Docs
- [ ] Unit tests (circuit breaker, mode detection)
- [ ] Integration tests (playwright: vim load, save)
- [ ] E2E tests (full edit flow)
- [ ] User guide (`docs/user-guide/vim-mode.md`)
- [ ] Developer docs (`docs/dev/vim-architecture.md`)
- [ ] ADR (`docs/adr/0004-vim-mode.md`)

### Final
- [ ] Update README status table (stage 4 done)
- [ ] Update IMPLEMENTATION_SUMMARY (stage 4 summary)
- [ ] Commit with detailed message
- [ ] Deploy to staging, smoke test
- [ ] Monitor metrics, iterate

---

## 21. Timeline Estimate

**Total**: 8-10 working days

| Phase | Duration | Parallelizable? |
|-------|----------|-----------------|
| Phase 1 (Foundation) | 2-3 days | No (sequential) |
| Phase 2 (Circuit Breaker) | 1-2 days | After Phase 1 |
| Phase 3 (Polish) | 2-3 days | Some parallel (theme + perf) |
| Phase 4 (Test & Docs) | 2 days | After Phase 3 |
| Contingency | 1 day | — |

**Critical path**: WASM integration → postMessage bridge → Circuit breaker → Testing

---

**End of Stage 4 Design Document**
