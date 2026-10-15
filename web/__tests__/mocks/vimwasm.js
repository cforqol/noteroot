// Mock VimWasm module for testing

export class VimWasm {
  constructor(opts) {
    this.canvas = opts.canvas;
    this.input = opts.input;
    this.workerScriptPath = opts.workerScriptPath;
    this.onError = null;
    this.onVimInit = null;
    this.onVimExit = null;
    this.onFileExport = null;
    this.running = false;
  }

  async start(opts) {
    this.running = true;
    // Simulate async startup
    await new Promise(resolve => setTimeout(resolve, 10));
    if (this.onVimInit) {
      this.onVimInit();
    }
  }

  async dropFile(name, contents) {
    // Mock file drop
  }

  async cmdline(cmd) {
    // Mock command execution
  }

  focus() {
    // Mock focus
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
