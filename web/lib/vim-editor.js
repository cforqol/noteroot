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

export const READY_TIMEOUT_MS = 20000;
export const READ_TIMEOUT_MS = 4000;
export const QUIT_GRACE_MS = 1000;
export const READ_COMMAND = 'silent noautocmd update | silent export';
/** Settle time before a container resize is pushed to Vim. */
export const RESIZE_DEBOUNCE_MS = 100;
const INSERT_FILE = '/tmp/.nr-insert';

/** Path of a note's buffer in Vim's in-memory filesystem. */
export function vimFilePath(notePath) {
  return `/tmp/${String(notePath).replace(/\//g, '_')}`;
}

/** Vim command-line arguments for editing `file`. */
export function vimStartArgs(file) {
  return [
    '-c', 'set nobackup nowritebackup noswapfile hidden',
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
   */
  constructor({
    VimWasm, canvas, input, container = null, loading = null,
    workerScriptPath = '/vim/vim-worker.js', onFailure = () => {},
    readyTimeout = READY_TIMEOUT_MS, readTimeout = READ_TIMEOUT_MS,
    resizeDebounce = RESIZE_DEBOUNCE_MS,
  }) {
    Object.assign(this, {
      VimWasm, canvas, input, container, loading, workerScriptPath, onFailure,
      readyTimeout, readTimeout, resizeDebounce,
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
      await vim.start({ debug: false, files: { [this.file]: content }, cmdArgs: vimStartArgs(this.file) });
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
   */
  resizeTo(width, height) {
    const w = Math.round(width);
    const h = Math.round(height);
    if (!this.running || w <= 0 || h <= 0) return false;
    if (w === this.sized.width && h === this.sized.height) return false;
    this.sized = { width: w, height: h };
    const dpr = globalThis.devicePixelRatio || 1;
    this.canvas.width = Math.round(w * dpr);
    this.canvas.height = Math.round(h * dpr);
    this.vim.resize(w, h);
    return true;
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

  /** Insert text below the cursor line; resolves false when Vim refused. */
  async insert(text) {
    if (!this.running) return false;
    try {
      // Command lines are capped near 126 characters, so stage the text
      // as a file: dropping opens it over the (hidden) note buffer; come
      // back, read it in below the cursor and wipe the staging buffer.
      await this.vim.dropFile(INSERT_FILE.slice(1), new TextEncoder().encode(text));
      await this.vim.cmdline(`silent buffer # | silent read ${INSERT_FILE} | silent! bwipeout! ${INSERT_FILE}`);
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
