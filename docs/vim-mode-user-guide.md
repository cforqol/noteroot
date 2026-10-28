# Vim Mode User Guide

## Overview

noteroot includes a full Vim editor for desktop browsers, powered by [vim.wasm](https://github.com/rhysd/vim.wasm) - a WebAssembly port of Vim. On mobile devices or narrow viewports, a plain text editor is used instead.

## Features

- **Full Vim Functionality**: Real Vim editor (v8.2.0055) compiled to WebAssembly
- **All Vim Motions**: hjkl, w/b/e, f/t, marks, jumps, text objects, etc.
- **Vim Commands**: `:w` to save, `:q` to quit, `:wq` or `ZZ` to save and quit
- **Modes**: NORMAL, INSERT, VISUAL, COMMAND-LINE modes
- **Registers**: Named registers, clipboard register (`"*`)
- **Settings**: `:set` commands (e.g., `:set number`, `:set expandtab`)
- **Automatic Fallback**: Circuit breaker disables Vim after failures, textarea fallback

## Getting Started

### Opening a Note in Vim

1. Click any note in the sidebar
2. Click the "Edit" button
3. Vim will load automatically on desktop (≥768px width)
4. Wait for the loading indicator to disappear

### Basic Vim Commands

```vim
i          Enter INSERT mode (type text)
ESC        Return to NORMAL mode
:w         Save the file
:q         Close the editor (only if saved)
:wq        Save and close
:q!        Close without saving
```

### Saving Your Work

- **In Vim**: Type `:w` in NORMAL mode
- **Save Button**: Works in textarea mode; shows "Use :w in Vim" in vim mode
- **Close Button**: Type `:q` or `:wq` in Vim; or use the button

### Settings

Click the gear (⚙) button in the header to open settings:

- **Auto** (default): Vim on desktop ≥768px, textarea on mobile
- **Always Vim**: Try Vim even on narrow viewports (if supported)
- **Always Plain Text**: Never use Vim, always use textarea

### Circuit Breaker

If Vim crashes 3 times consecutively, it's automatically disabled:

1. Open Settings (⚙ button)
2. Check "Circuit breaker: Disabled"
3. Click "Reset Circuit Breaker" to re-enable
4. Circuit breaker auto-resets after 24 hours

## Browser Compatibility

| Browser | Support | Notes |
|---------|---------|-------|
| Chrome/Chromium | ✅ Full | Works by default |
| Edge | ✅ Full | Works by default |
| Firefox | ✅ Full (recent) | No flag needed in current releases |
| Safari | ✅ Full (recent) | No flag needed in current releases |

**Why?** Vim mode requires `SharedArrayBuffer` and `Atomics` API for synchronous wait in Web Worker. The server sends `Cross-Origin-Opener-Policy: same-origin` and `Cross-Origin-Embedder-Policy: credentialless` on every response, which enables `SharedArrayBuffer` without feature flags in current browsers. (Older Firefox needed `javascript.options.shared_memory` in about:config; older Safari needed the Develop-menu toggle.)

## Limitations

- **Small Feature Set**: Current build uses vim.wasm 'small' features (no syntax highlighting, limited plugins)
- **Draft Autosave**: Only saves on `:w` command (not real-time like textarea)
- **Attach menu**: File uploads and links go through the 📎 Attach menu in the toolbar — the insert lands in the Vim buffer once Vim is ready; save with `:w` (or Save/Done) to store it on the server
- **Startup Time**: Vim takes 1-2 seconds to load (larger binary than textarea)

## Troubleshooting

### Vim Won't Load

1. **Check Browser**: Use Chrome/Chromium for best experience
2. **Check Viewport**: Vim requires ≥768px width (unless "Always Vim" in settings)
3. **Check Circuit Breaker**: Open settings, reset if disabled
4. **Check Console**: Open DevTools, look for errors in Console tab

### Vim Keeps Crashing

1. **Fallback to Textarea**: Set "Always Plain Text" in settings
2. **Report Issue**: Note the error in console, report to noteroot repo
3. **Browser Update**: Ensure browser is up-to-date

### Keys Don't Work

1. **Click Vim Screen**: Vim may have lost focus
2. **Disable Extensions**: Browser extensions might intercept keys
3. **Try Incognito**: Test in incognito/private mode

## Advanced Usage

### Clipboard Integration

Vim's `"*` register syncs with system clipboard:

```vim
"*yy       Copy current line to clipboard
"*p        Paste from clipboard
:set clipboard=unnamed   " Auto-sync with clipboard
```

### Custom vimrc

Files under `~/.vim/` are persisted in browser's IndexedDB. Create `~/.vim/vimrc`:

```vim
" In Vim, type:
:e ~/.vim/vimrc

" Add your config:
set number
set expandtab
set shiftwidth=2

" Save:
:w
```

Your vimrc will load on every Vim startup.

### Multiple Registers

```vim
"ayy       Yank line to register a
"ap        Paste from register a
"byy       Yank line to register b
```

## FAQ

**Q: Why not syntax highlighting?**
A: Current build uses 'small' feature set (1MB total). Normal feature set (7MB) includes syntax highlighting but is much larger. May add in future.

**Q: Can I install Vim plugins?**
A: Limited support. vim.wasm supports Vim script, but many plugins require shell commands which aren't available. Simple plugins may work.

**Q: Why does `:!ls` not work?**
A: Shell commands aren't available in browser. `:!` only works for JavaScript files (`:!file.js` evaluates JS in browser context).

**Q: Can I use external files?**
A: Yes! Use `:e /path/to/file` to edit files in virtual filesystem. Files in `~/.vim/` are persistent via IndexedDB.

**Q: How do I exit Vim?**
A: Type `:q` (or `:wq` to save first). Or close the browser tab (after saving with `:w`).

## Tips

- **Learn Vim Motions**: Vim's power is in motions (w, b, f, t, %, etc.). Practice with `vimtutor`
- **Use Marks**: `ma` to set mark a, `'a` to jump to it
- **Use Visual Mode**: `v` for character-wise, `V` for line-wise, `Ctrl-v` for block
- **Repeat Commands**: `.` repeats last change, `@@` repeats last macro
- **Search**: `/pattern` to search forward, `n` for next match, `N` for previous

## Resources

- [Vim Documentation](https://www.vim.org/docs.php) - Official Vim docs
- [vim.wasm GitHub](https://github.com/rhysd/vim.wasm) - Source for vim.wasm
- [Vim Cheat Sheet](https://vim.rtorr.com/) - Quick reference
- [Vim Adventures](https://vim-adventures.com/) - Learn Vim through a game
