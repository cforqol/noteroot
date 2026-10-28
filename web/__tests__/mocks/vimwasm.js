// Mock vim.wasm module for the app tests (mapped from /vim/vimwasm.js).
//
// It keeps the contract lib/vim-editor.js is built on: Vim exports the
// note on VimEnter (readiness), exports it again when asked to read the
// buffer, and quits on `qall!`. `VimWasm.last` is the newest instance;
// set its `buffer` to simulate typing in Vim.

const enc = (s) => new TextEncoder().encode(s).buffer;

export class VimWasm {
  static last = null;

  constructor(opts) {
    this.canvas = opts.canvas;
    this.input = opts.input;
    this.workerScriptPath = opts.workerScriptPath;
    this.onError = null;
    this.onVimInit = null;
    this.onVimExit = null;
    this.onFileExport = null;
    this.running = false;
    this.commands = [];
    this.worker = { terminate() {} };
    VimWasm.last = this;
  }

  async start(opts) {
    this.running = true;
    this.file = opts.cmdArgs[opts.cmdArgs.length - 1];
    this.buffer = opts.files[this.file];
    // Simulate async startup, then the VimEnter export.
    await new Promise((resolve) => setTimeout(resolve, 10));
    this.onVimInit?.();
    this.onFileExport?.(this.file, enc(this.buffer));
  }

  async dropFile() {}

  async cmdline(cmd) {
    this.commands.push(cmd);
    if (!this.running) throw new Error('Vim is not running');
    if (cmd.endsWith('silent export')) {
      setTimeout(() => this.onFileExport?.(this.file, enc(this.buffer)), 0);
    } else if (cmd === 'qall!') {
      this.running = false;
      setTimeout(() => this.onVimExit?.(0), 0);
    }
  }

  focus() {}

  resize(pixelWidth, pixelHeight) {
    this.resized = { pixelWidth, pixelHeight };
  }

  isRunning() {
    return this.running;
  }
}

export function checkBrowserCompatibility() {
  if (typeof SharedArrayBuffer === 'undefined') {
    return 'SharedArrayBuffer is not supported';
  }
  if (typeof Atomics === 'undefined') {
    return 'Atomics API is not supported';
  }
  return undefined;
}

export const VIM_VERSION = '8.2.0055';
