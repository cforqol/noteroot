/**
 * A Vim editing session on vim.wasm.
 *
 * vim.wasm takes Ex command lines in and hands file bytes out only
 * through `onFileExport`, so the session is built around exports:
 *
 *  - Vim starts with autocmds that export the note file on VimEnter
 *    and after every write. `onVimInit` fires before Vim accepts
 *    commands (a command sent that early never completes), so the
 *    VimEnter export is the readiness signal; the write export makes
 *    `:w`, `:wq` and `:x` reach the app, which saves to the server.
 *  - Reads run `silent noautocmd update | silent export`: the buffer is
 *    written only when modified (an untouched buffer comes back byte for
 *    byte, without Vim's end-of-line fix-up) and exported.
 *  - Exports are routed by intent: the first is readiness, one arriving
 *    while a read is pending answers it, any other is a user write.
 *
 * Command completion is never trusted for data: vim.wasm keeps one
 * waiter per response kind, so a late response can resolve the wrong
 * call. Reads wait for the export event itself, under a timeout.
 */

import { splitFrontmatter } from './frontmatter.js';

export const READY_TIMEOUT_MS = 20000;
export const READ_TIMEOUT_MS = 4000;
export const QUIT_GRACE_MS = 1000;
export const READ_COMMAND = 'silent noautocmd update | silent export';
/** Settle time before a container resize is pushed to Vim. */
export const RESIZE_DEBOUNCE_MS = 100;
const INSERT_FILE = '/tmp/.nr-insert';
/** Lines taken by a leading frontmatter block, closing `---` included; 0 without one. */
function frontmatterLines(text) {
  const sp = splitFrontmatter(text);
  if (!sp.ok) return 0;
  const head = sp.head + sp.block + sp.close;
  return head.split('\n').length - (head.endsWith('\n') ? 1 : 0);
}

/** Path of a note's buffer in Vim's in-memory filesystem. */
export function vimFilePath(notePath) {
  return `/tmp/${String(notePath).replace(/\//g, '_')}`;
}

/** Where the user's vimrc is placed in Vim's virtual file system. */
export const VIMRC_PATH = '/tmp/noteroot.vimrc';

/**
 * Vim command-line arguments for editing `file`. With `rc`, the user's
 * vimrc is sourced after the built-in settings (so it can override
 * them); `silent!` keeps a broken line from blocking the editor.
 */
export function vimStartArgs(file, rc = false) {
  return [
    '-c', 'set nobackup nowritebackup noswapfile hidden',
    ...(rc ? ['-c', `silent! source ${VIMRC_PATH}`] : []),
    '-c', 'autocmd BufWritePost * export',
    '-c', 'autocmd VimEnter * export',
    file,
  ];
}

export class VimEditor {
  /**
   * @param {object} o
   * @param {function} o.VimWasm         vim.wasm constructor
   * @param {HTMLCanvasElement} o.canvas
   * @param {HTMLInputElement} o.input   receives keystrokes (dirty hint)
   * @param {HTMLElement} [o.container]  shown while the session lives
   * @param {HTMLElement} [o.loading]    shown while Vim boots
   * @param {(err:Error)=>void} [o.onFailure]  start failure or crash
   * @param {()=>string} [o.getVimrc]  the user's vimrc text, read at each start
   */
  constructor({
    VimWasm, canvas, input, container = null, loading = null,
    workerScriptPath = '/vim/vim-worker.js', onFailure = () => {},
    readyTimeout = READY_TIMEOUT_MS, readTimeout = READ_TIMEOUT_MS,
    resizeDebounce = RESIZE_DEBOUNCE_MS, getVimrc = () => '',
  }) {
    Object.assign(this, {
      VimWasm, canvas, input, container, loading, workerScriptPath, onFailure,
      readyTimeout, readTimeout, resizeDebounce, getVimrc,
    });
    this.vim = null;
    this.file = null;
    this.ready = false;
    this.closed = false;
    this.content = ''; // last buffer contents seen (open, read or :w)
    this.read = null; // pending read
    this.starting = null;
    this.dirtyCb = null;
    this.saveCb = null;
    this.closeCb = null;
    this.onKey = () => this.dirtyCb?.();
    // A bare modifier keydown must never reach Vim. vim.wasm only drops
    // Shift when its own shiftKey flag is set; a synthetic Shift event (or
    // any modifier delivered with that flag cleared) forwards keyCode 16,
    // which Vim decodes as Ctrl-P and the cursor jumps up a line. Swallow
    // modifier-only keys before vim.wasm's capture listener sees them.
    this.MODIFIER_KEYS = new Set([
      'Shift', 'Control', 'Alt', 'Meta', 'CapsLock', 'NumLock', 'ScrollLock',
      'AltGraph', 'Fn', 'FnLock', 'Hyper', 'Super', 'OS', 'FnLock',
      'Dead', 'Unidentified', 'Process',
    ]);
    this.onModifierOnly = (e) => {
      if (this.MODIFIER_KEYS.has(e.key)) e.stopImmediatePropagation();
    };
    this.observer = null;
    this.resizeTimer = 0;
    this.sized = { width: 0, height: 0 };
  }

  /** Keystroke in Vim: the buffer may have changed. */
  onDirty(cb) { this.dirtyCb = cb; }

  /** `:w` in Vim: cb(content). */
  onSave(cb) { this.saveCb = cb; }

  /** Vim quit (`{crashed:false}`) or died (`{crashed:true, error}`). */
  onClose(cb) { this.closeCb = cb; }

  /** True from readiness until Vim quits, crashes or the session closes. */
  get running() {
    return this.ready && !this.closed;
  }

  /** Start Vim on `content`; resolves once it accepts commands. */
  async open(path, content) {
    this.file = vimFilePath(path);
    this.content = content;
    if (this.container) this.container.hidden = false;
    this.input?.addEventListener('keydown', this.onKey);
    // Capture phase, registered before vim.wasm attaches its own handler
    // (in onVimInit during boot), so a bare modifier is swallowed first.
    this.input?.addEventListener('keydown', this.onModifierOnly, { capture: true });
    try {
      await this.boot(content);
    } catch (err) {
      this.close();
      throw err;
    }
  }

  async boot(content) {
    this.setLoading(true);
    const vim = new this.VimWasm({ canvas: this.canvas, input: this.input, workerScriptPath: this.workerScriptPath });
    this.vim = vim;
    this.ready = false;
    const started = new Promise((resolve, reject) => { this.starting = { resolve, reject }; });
    // Callbacks from a replaced or closed instance are ignored.
    const mine = (fn) => (...args) => { if (this.vim === vim) fn(...args); };
    vim.onFileExport = mine((path, bytes) => this.exported(path, bytes));
    vim.onVimExit = mine(() => this.exited());
    vim.onError = mine((err) => this.failed(err));
    const timer = setTimeout(() => this.starting?.reject(new Error('Vim did not start in time')), this.readyTimeout);
    try {
      const rc = String(this.getVimrc() || '');
      const hasRc = rc.trim() !== '';
      const files = { [this.file]: content };
      if (hasRc) files[VIMRC_PATH] = rc;
      await vim.start({ debug: false, files, cmdArgs: vimStartArgs(this.file, hasRc) });
      await started;
    } catch (err) {
      this.onFailure(err);
      this.stop();
      throw err;
    } finally {
      clearTimeout(timer);
      this.starting = null;
      this.setLoading(false);
    }
    // vim.wasm measures the canvas once, in its constructor, and then
    // only listens for *window* resizes. Anything else that changes the
    // editor's box — the toolbar wrapping once the lock state lands, a
    // draft banner, the tree being toggled, the status bar — would leave
    // Vim drawing at the old size inside a differently sized canvas. Sync
    // once now that layout has settled, then watch the container.
    this.syncSize();
    this.watchSize();
  }

  /**
   * Resize the canvas backing store and tell Vim its new pixel size.
   * Returns false when nothing had to change (or Vim is not running).
   * Sizes truncate the way vim.wasm's own measurement does, so the
   * first sync after start finds nothing to change.
   */
  resizeTo(width, height) {
    const w = Math.floor(width);
    const h = Math.floor(height);
    if (!this.running || w <= 0 || h <= 0) return false;
    if (w === this.sized.width && h === this.sized.height) return false;
    this.sized = { width: w, height: h };
    const dpr = globalThis.devicePixelRatio || 1;
    this.resizeBitmap(Math.floor(width * dpr), Math.floor(height * dpr));
    this.vim.resize(w, h);
    return true;
  }

  /**
   * Size the canvas bitmap without losing the picture. Assigning a
   * canvas's width or height wipes it, even to the same value, and Vim
   * repaints only when its grid of rows and columns changes: a nudge
   * smaller than a character cell would otherwise leave it blank.
   */
  resizeBitmap(width, height) {
    const { canvas } = this;
    if (canvas.width === width && canvas.height === height) return;
    const copy = this.snapshot();
    canvas.width = width;
    canvas.height = height;
    if (copy) canvas.getContext('2d')?.drawImage(copy, 0, 0);
  }

  /** A copy of the canvas, or null when the browser cannot make one. */
  snapshot() {
    try {
      const copy = this.canvas.ownerDocument.createElement('canvas');
      copy.width = this.canvas.width;
      copy.height = this.canvas.height;
      const ctx = copy.getContext('2d');
      if (!ctx) return null;
      ctx.drawImage(this.canvas, 0, 0);
      return copy;
    } catch {
      return null;
    }
  }

  /** Push the canvas's current on-screen size to Vim. */
  syncSize() {
    const rect = this.canvas?.getBoundingClientRect?.();
    if (!rect) return false;
    return this.resizeTo(rect.width, rect.height);
  }

  /**
   * Follow the container's size. Debounced: a drag-resize would
   * otherwise send a message per frame to the Vim worker.
   */
  watchSize() {
    const target = this.container || this.canvas;
    if (!target || typeof ResizeObserver !== 'function' || this.observer) return;
    this.observer = new ResizeObserver(() => {
      clearTimeout(this.resizeTimer);
      this.resizeTimer = setTimeout(() => this.syncSize(), this.resizeDebounce);
    });
    this.observer.observe(target);
  }

  unwatchSize() {
    clearTimeout(this.resizeTimer);
    this.resizeTimer = 0;
    this.observer?.disconnect();
    this.observer = null;
  }

  exported(path, bytes) {
    if (path !== this.file) return;
    const text = new TextDecoder().decode(bytes);
    this.content = text;
    if (!this.ready) {
      this.ready = true;
      this.starting?.resolve();
      return;
    }
    if (this.read) {
      const r = this.read;
      this.read = null;
      clearTimeout(r.timer);
      r.resolve(text);
      return;
    }
    this.saveCb?.(text);
  }

  exited() {
    this.vim = null;
    this.ready = false;
    this.abortRead(new Error('Vim has quit'));
    this.closeCb?.({ crashed: false });
  }

  failed(err) {
    if (!this.ready) {
      this.starting?.reject(err);
      return;
    }
    this.onFailure(err);
    this.ready = false;
    this.abortRead(err);
    this.stop();
    this.closeCb?.({ crashed: true, error: err });
  }

  /** The live buffer; the last one seen when Vim is not running. */
  getContent() {
    if (!this.running) return Promise.resolve(this.content);
    if (this.read) return this.read.promise;
    let resolve;
    let reject;
    const promise = new Promise((res, rej) => { resolve = res; reject = rej; });
    const timer = setTimeout(() => this.abortRead(new Error('Vim did not hand back the buffer')), this.readTimeout);
    this.read = { promise, resolve, reject, timer };
    const vim = this.vim;
    Promise.resolve().then(() => vim.cmdline(READ_COMMAND)).catch((err) => this.abortRead(err));
    return promise;
  }

  abortRead(err) {
    const r = this.read;
    if (!r) return;
    this.read = null;
    clearTimeout(r.timer);
    r.reject(err);
  }

  /** Replace the whole buffer (draft restore, conflict reload) by restarting Vim on it. */
  async replace(content) {
    this.abortRead(new Error('buffer replaced'));
    this.stop();
    this.content = content;
    try {
      await this.boot(content);
    } catch (err) {
      this.close();
      throw err;
    }
  }

  /**
   * Insert text below the cursor line, or below the closing `---` of a
   * leading frontmatter block while the cursor is still inside it.
   * Resolves false when Vim refused.
   *
   * vim.wasm has no +eval (no exe, line(), getline()), so the block's
   * last line is worked out here and Vim only runs plain Ex: `:global`
   * with the cursor-position atom `\%#` picks the one range, body or
   * block, that holds the cursor.
   */
  async insert(text) {
    if (!this.running) return false;
    try {
      const block = frontmatterLines(await this.getContent());
      // Command lines are capped near 126 characters, so stage the text
      // as a file: dropping opens it over the (hidden) note buffer; come
      // back, read it in and wipe the staging buffer.
      await this.vim.dropFile(INSERT_FILE.slice(1), new TextEncoder().encode(text));
      if (block === 0) {
        await this.vim.cmdline(`silent buffer # | silent read ${INSERT_FILE}`);
      } else {
        await this.vim.cmdline('silent buffer #');
        // The body first: a cursor there reads below its own line, and
        // the second range then no longer holds the cursor.
        await this.vim.cmdline(`silent! ${block + 1},$g/\\%#/read ${INSERT_FILE}`);
        await this.vim.cmdline(`silent! 1,${block}g/\\%#/${block}read ${INSERT_FILE}`);
      }
      await this.vim.cmdline(`silent! bwipeout! ${INSERT_FILE}`);
      return true;
    } catch {
      return false;
    }
  }

  focus() {
    this.vim?.focus();
  }

  /** End the session: quit Vim and hide the editor. Idempotent. */
  close() {
    if (this.closed) return;
    this.closed = true;
    this.input?.removeEventListener('keydown', this.onKey);
    this.input?.removeEventListener('keydown', this.onModifierOnly, { capture: true });
    this.unwatchSize();
    this.abortRead(new Error('editor closed'));
    this.stop();
    if (this.container) this.container.hidden = true;
    this.setLoading(false);
  }

  stop() {
    const vim = this.vim;
    this.vim = null;
    this.ready = false;
    if (!vim) return;
    // Ask Vim to quit (vim.wasm then releases its worker and listeners);
    // force the worker down if Vim is wedged.
    Promise.resolve().then(() => vim.cmdline('qall!')).catch(() => {});
    setTimeout(() => {
      try {
        vim.worker?.terminate();
      } catch {
        /* already gone */
      }
    }, QUIT_GRACE_MS);
  }

  setLoading(on) {
    if (this.loading) this.loading.hidden = !on;
  }
}
