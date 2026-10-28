# UI Integration Tests

## Overview

The linking feature has two levels of UI integration tests:

1. **JavaScript unit tests** (`app.test.cjs`) — Test the UI panel building and SPA navigation logic in isolation
2. **Go E2E tests** (`internal/httpapi/notes_test.go`) — Verify the complete backend-to-frontend integration

## Running Tests

### JavaScript Tests

```bash
# Install dependencies (one-time)
cd web && npm install

# Run JS tests
npm test

# Or directly with node
node --test app.test.cjs
```

The JS tests verify:
- `linksPanels()` correctly builds Outgoing/Backlinks panels from JSON data
- Resolved links become `<a class="wikilink" data-path>` anchors
- Unresolved targets become `<span class="wikilink dead">` spans
- SPA click handler respects modifier keys (cmd/ctrl/middle-click)
- Panels are omitted gracefully when index unavailable

### Go E2E Tests

```bash
# Run all httpapi tests (includes UI integration)
go test ./internal/httpapi/

# Run specific UI tests
go test -run TestNote_wikilink ./internal/httpapi/
go test -run TestNote_backlinks ./internal/httpapi/
go test -run TestNote_panel ./internal/httpapi/
```

The Go E2E tests verify:
- **TestNote_linksIntegration** — Complete §7 flow: backlinks/outgoing JSON + HTML rendering
- **TestNote_wikilinkHTMLStructure** — Proper `<a>`/`<span>` structure, CSS classes, data attributes
- **TestNote_backlinksOutgoingJSONShape** — Exact JSON field structure (omitempty, field presence)
- **TestNote_wikilinkSPAAttributes** — data-path extraction, href encoding for paths with special chars
- **TestNote_panelDataConsistency** — HTML and JSON data agree (design §7 requirement)

## Test Coverage

### UI Functions Tested
- ✅ `linksPanels(n)` — Panel fragment building
- ✅ `panel(title, items, makeItem)` — Generic panel structure
- ✅ `noteLink(path, text)` — Anchor creation with proper attributes
- ✅ SPA click handler — Event delegation, modifier key handling

### Integration Points Tested
- ✅ Backend JSON → Frontend panel rendering
- ✅ Wikilink resolution → HTML anchor href
- ✅ Unresolved targets → Dead spans
- ✅ Label/title/target priority in display text
- ✅ Path encoding in hrefs and data-path
- ✅ Graceful degradation when index unavailable
- ✅ Alias resolution in panels
- ✅ Self-links (note linking to itself via alias)

### Design Requirements Verified
- §7: Display and data never disagree ✅
- §8: Middle-click, cmd-click work via real hrefs ✅
- §8: No-JS works (anchors with real hrefs) ✅
- §8: SPA navigation for plain left-click ✅
- Dead links visible, never silent ✅
- Panels omitted when no data ✅

## CI Integration

To add these tests to CI:

```bash
# In Makefile or CI script:
make test-web    # runs web/npm test
make test        # already runs Go tests including UI E2E
```

## Manual Verification

For live browser testing:

```bash
# Start server with test notebook
export NOTEROOT_DATA_DIR=/tmp/nr-test
export NOTEROOT_AUTH_MODE=none
./scripts/new-notebook.sh demo

# Add test notes with wikilinks
curl -X PUT 'http://localhost:8080/n/demo/note?path=alpha.md' \
  -H 'Content-Type: text/markdown' \
  --data-binary $'---\ntitle: Alpha\n---\n[[beta|see beta]]\n'

curl -X PUT 'http://localhost:8080/n/demo/note?path=beta.md' \
  -H 'Content-Type: text/markdown' \
  --data-binary $'---\ntitle: Beta\n---\nBack to [[alpha]].\n'

# Open http://localhost:8080 and verify:
# - Outgoing/Backlinks panels appear below note
# - Clicking wikilink navigates within app (SPA)
# - Middle-click opens in new tab
# - Dead links show as muted dotted text
```

## Test Dependencies

- **jsdom** (^25.0.0) — DOM implementation for Node.js testing
- **node:test** — Built-in Node.js test runner (Node 18+)

## Notes

- JS tests are isolated unit tests (no server required)
- Go E2E tests run full server + index + rendering stack
- Both test suites are independent and can run in parallel
- Manual browser testing still recommended for visual/UX verification


## Workspace UI tests (note/folder CRUD, tree, status bar, themes, layout)

| Layer | Where | Covers |
|---|---|---|
| Unit | `__tests__/unit/{stats,tree,prefs,notes-api,trash}.test.js` | pure logic in `lib/` (100 % line/function coverage enforced by `jest.config.js`); trash includes coercion, wording and expiry helpers |
| jsdom integration | `__tests__/integration/{dialog,tree-view}.test.js` | dialogs, context menu, tree rendering and callbacks |
| App integration | `__tests__/integration/app-ui.test.js` | the real `index.html` + `app.js` against an in-memory fake of the HTTP API: tree, collapse, decorations, CRUD through dialogs, trash restore, status bar, theme, placement, phone drawer, trash retention |
| Browser E2E | `e2e/ui-notes.spec.js` | the built binary in Chromium: CRUD, status bar (plain and Vim), persistence across reload, theme vs OS preference, left/right/hidden placement, 375 px phone drawer, folder API safety and CSRF |
| Vim editor integration | `__tests__/integration/vim-editor.test.js` | ResizeObserver‑based canvas resize, syncSize, watchSize, close, and DPR-aware backing store |

```bash
make build
cd web
node --experimental-vm-modules ../node_modules/.bin/jest --config jest.config.js --coverage
npx playwright test --config playwright.config.js e2e/ui-notes.spec.js
```

Go side: `go test ./internal/vault ./internal/state ./internal/httpapi -run 'Mkdir|RenameDir|IsDir|RenameIdentityPrefix|Folder'`.
