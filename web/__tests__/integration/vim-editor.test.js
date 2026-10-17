/**
 * VimEditor (lib/vim-editor.js) against a scripted vim.wasm double and
 * the real circuit breaker. The double reproduces the behaviours the
 * session is built around: exports are the only way content leaves Vim,
 * readiness is the VimEnter export, and `:w` exports via BufWritePost.
 */
import { jest } from '@jest/globals';
import {
  VimEditor, vimFilePath, vimStartArgs, READ_COMMAND, QUIT_GRACE_MS, RESIZE_DEBOUNCE_MS,
} from '../../lib/vim-editor.js';
import { recordVimFailure, getVimModeState, shouldUseVim, resetVimCircuitBreaker } from '../../lib/vim-mode.js';

let instances = [];
const enc = (s) => new TextEncoder().encode(s).buffer;

class FakeVim {
  static mode = 'ready'; // 'ready' | 'silent' | 'throw'

  constructor(opts) {
    this.opts = opts;
    this.cmds = [];
    this.drops = [];
    this.resizes = [];
    this.resizes = [];
    this.worker = { terminate: jest.fn() };
    instances.push(this);
  }

  start(opts) {
    this.startOpts = opts;
    this.file = opts.cmdArgs[opts.cmdArgs.length - 1];
    if (FakeVim.mode === 'throw') throw new Error('cannot start');
    if (FakeVim.mode === 'ready') setTimeout(() => this.export(opts.files[this.file]), 0); // VimEnter
  }

  export(text, path = this.file) {
    this.onFileExport?.(path, enc(text));
  }

  async cmdline(cmd) {
    this.cmds.push(cmd);
    if (this.onCmd) return this.onCmd(cmd);
    return undefined;
  }

  async dropFile(name, bytes) {
    if (this.dropFails) throw new Error('drop failed');
    this.drops.push([name, new TextDecoder().decode(bytes)]);
  }

  focus() {
    this.focused = true;
  }

  resize(pixelWidth, pixelHeight) {
    this.resizes.push([pixelWidth, pixelHeight]);
  }
}

let els;
function dom() {
  document.body.innerHTML = `
    <div id="vim-container" hidden>
      <div id="vim-loading" hidden>Loading</div>
      <canvas id="vim-canvas"></canvas>
      <input id="vim-input">
    </div>`;
  els = {
    container: document.getElementById('vim-container'),
    loading: document.getElementById('vim-loading'),
    canvas: document.getElementById('vim-canvas'),
    input: document.getElementById('vim-input'),
  };
}

function editor(extra = {}) {
  return new VimEditor({ VimWasm: FakeVim, ...els, onFailure: () => recordVimFailure(), readyTimeout: 50, readTimeout: 50, ...extra });
}

const last = () => instances[instances.length - 1];

beforeEach(() => {
  instances = [];
  FakeVim.mode = 'ready';
  dom();
  resetVimCircuitBreaker();
});

afterEach(() => {
  jest.useRealTimers();
});

test('vimFilePath / vimStartArgs', () => {
  expect(vimFilePath('a/b/c.md')).toBe('/tmp/a_b_c.md');
  const args = vimStartArgs('/tmp/x.md');
  expect(args[args.length - 1]).toBe('/tmp/x.md');
  expect(args).toContain('autocmd VimEnter * export');
  expect(args).toContain('autocmd BufWritePost * export');
  expect(args.join(' ')).toMatch(/noswapfile.*hidden/);
});

describe('open', () => {
  test('boots Vim on the note and resolves on the VimEnter export', async () => {
    const ed = editor();
    const opening = ed.open('notes/a.md', 'hello');
    expect(els.container.hidden).toBe(false);
    expect(els.loading.hidden).toBe(false);
    expect(ed.running).toBe(false);
    await opening;
    expect(ed.running).toBe(true);
    expect(els.loading.hidden).toBe(true);
    const v = last();
    expect(v.opts).toEqual({ canvas: els.canvas, input: els.input, workerScriptPath: '/vim/vim-worker.js' });
    expect(v.startOpts.files).toEqual({ '/tmp/notes_a.md': 'hello' });
    expect(v.startOpts.cmdArgs).toEqual(vimStartArgs('/tmp/notes_a.md'));
    expect(v.cmds).toEqual([]); // nothing is sent before Vim is ready
  });

  test('times out when Vim never becomes ready; records a failure and tears down', async () => {
    FakeVim.mode = 'silent';
    const ed = editor();
    await expect(ed.open('a.md', 'x')).rejects.toThrow('did not start');
    expect(getVimModeState().failures).toBe(1);
    expect(els.container.hidden).toBe(true);
    expect(els.loading.hidden).toBe(true);
    expect(last().cmds).toEqual(['qall!']);
  });

  test('start throwing and onError before readiness both reject once', async () => {
    FakeVim.mode = 'throw';
    await expect(editor().open('a.md', 'x')).rejects.toThrow('cannot start');
    FakeVim.mode = 'silent';
    const ed = editor();
    const p = ed.open('b.md', 'y');
    last().onError(new Error('worker crashed'));
    await expect(p).rejects.toThrow('worker crashed');
    expect(getVimModeState().failures).toBe(2);
  });

  test('three failed starts trip the circuit breaker', async () => {
    FakeVim.mode = 'throw';
    for (let i = 0; i < 3; i += 1) await expect(editor().open(`n${i}.md`, '')).rejects.toThrow();
    expect(getVimModeState().enabled).toBe(false);
    expect(shouldUseVim({ checkCompatibility: () => undefined, viewportWidth: 1280 })).toBe(false);
  });

  test('works without container/loading elements or a failure hook', async () => {
    const ed = new VimEditor({ VimWasm: FakeVim, canvas: els.canvas, input: null });
    await ed.open('a.md', 'x');
    expect(ed.running).toBe(true);
    ed.close();
    FakeVim.mode = 'throw';
    await expect(new VimEditor({ VimWasm: FakeVim, canvas: els.canvas, input: null }).open('a.md', 'x')).rejects.toThrow();
    expect(getVimModeState().failures).toBe(0);
  });
});

describe('resizing the canvas', () => {
  // vim.wasm only listens for *window* resizes, so container-only layout
  // changes must be pushed by hand or Vim keeps drawing at the old size.
  let dpr;
  beforeEach(() => { dpr = globalThis.devicePixelRatio; });
  afterEach(() => {
    Object.defineProperty(globalThis, 'devicePixelRatio', { value: dpr, configurable: true });
    delete globalThis.ResizeObserver;
  });

  function sizeCanvas(width, height) {
    els.canvas.getBoundingClientRect = () => ({ width, height });
  }

  test('resizeTo drives the backing store and tells Vim, once per size', async () => {
    const ed = editor();
    await ed.open('a.md', 'x');
    sizeCanvas(640, 480);

    expect(ed.resizeTo(640.4, 480.5)).toBe(true);
    expect(els.canvas.width).toBe(640);
    expect(els.canvas.height).toBe(481);
    expect(last().resizes).toEqual([[640, 481]]);
    // Same size again is a no-op, so a chatty observer cannot spam the worker.
    expect(ed.resizeTo(640, 481)).toBe(false);
    expect(last().resizes).toEqual([[640, 481]]);
  });

  test('scales the backing store by the device pixel ratio', async () => {
    Object.defineProperty(globalThis, 'devicePixelRatio', { value: 2, configurable: true });
    const ed = editor();
    await ed.open('a.md', 'x');
    expect(ed.resizeTo(300, 200)).toBe(true);
    expect(els.canvas.width).toBe(600);
    expect(els.canvas.height).toBe(400);
    expect(last().resizes).toEqual([[300, 200]]);
  });

  test('ignores degenerate sizes and editors that are not running', async () => {
    const idle = editor();
    expect(idle.resizeTo(100, 100)).toBe(false);
    await idle.open('a.md', 'x');
    expect(idle.resizeTo(0, 100)).toBe(false);
    expect(idle.resizeTo(100, 0)).toBe(false);
    expect(idle.resizeTo(-5, 50)).toBe(false);
    expect(last().resizes).toEqual([]);
  });

  test('syncSize forwards the canvas box', async () => {
    const ed = editor();
    await ed.open('a.md', 'x');
    sizeCanvas(800, 600);
    expect(ed.syncSize()).toBe(true);
    expect(last().resizes).toEqual([[800, 600]]);
  });

  test('open syncs the canvas once layout has settled', async () => {
    sizeCanvas(900, 700);
    const ed = editor();
    await ed.open('a.md', 'x');
    expect(last().resizes).toEqual([[900, 700]]);
  });

  test('watchSize follows the container, debounced', async () => {
    const observers = [];
    globalThis.ResizeObserver = class {
      constructor(cb) { this.cb = cb; observers.push(this); }
      observe(t) { this.target = t; }
      disconnect() { this.disconnected = true; }
    };
    const ed = editor();
    await ed.open('a.md', 'x');
    expect(observers).toHaveLength(1);
    expect(observers[0].target).toBe(els.container);

    sizeCanvas(500, 400);
    jest.useFakeTimers();
    observers[0].cb();
    observers[0].cb(); // a second nudge restarts the debounce
    expect(last().resizes).toEqual([]);
    jest.advanceTimersByTime(RESIZE_DEBOUNCE_MS);
    expect(last().resizes).toEqual([[500, 400]]);
    // Watching twice does not stack observers.
    ed.watchSize();
    expect(observers).toHaveLength(1);
  });

  test('degrades safely without a device pixel ratio or a measurable box', async () => {
    Object.defineProperty(globalThis, 'devicePixelRatio', { value: 0, configurable: true });
    const ed = editor();
    await ed.open('a.md', 'x');
    expect(ed.resizeTo(200, 100)).toBe(true); // falls back to a ratio of 1
    expect(els.canvas.width).toBe(200);
    expect(els.canvas.height).toBe(100);
    els.canvas.getBoundingClientRect = () => undefined; // canvas is not laid out
    expect(ed.syncSize()).toBe(false);
  });

  test('works without a ResizeObserver (older browsers) or a container', async () => {
    delete globalThis.ResizeObserver;
    const ed = editor();
    await ed.open('a.md', 'x');
    expect(ed.observer).toBeNull();
  });

  test('close stops watching and cancels a pending resize', async () => {
    const observers = [];
    globalThis.ResizeObserver = class {
      constructor(cb) { this.cb = cb; observers.push(this); }
      observe() {}
      disconnect() { this.disconnected = true; }
    };
    const ed = editor();
    await ed.open('a.md', 'x');
    jest.useFakeTimers();
    sizeCanvas(400, 300);
    observers[0].cb();
    ed.close();
    expect(observers[0].disconnected).toBe(true);
    jest.advanceTimersByTime(RESIZE_DEBOUNCE_MS * 5);
    expect(last().resizes).toEqual([]); // the cancelled timer never fires
  });
});

describe('reading the buffer', () => {
  test('a read sends one update+export command and resolves with the exported bytes', async () => {
    const ed = editor();
    await ed.open('a.md', 'v1');
    const v = last();
    v.onCmd = (cmd) => { if (cmd === READ_COMMAND) setTimeout(() => v.export('v2 edited\n'), 0); };
    const [a, b] = await Promise.all([ed.getContent(), ed.getContent()]); // coalesced
    expect(a).toBe('v2 edited\n');
    expect(b).toBe('v2 edited\n');
    expect(v.cmds).toEqual([READ_COMMAND]);
    expect(ed.content).toBe('v2 edited\n');
  });

  test('a read times out when no export arrives, and a rejected command fails it', async () => {
    const ed = editor();
    await ed.open('a.md', 'v1');
    await expect(ed.getContent()).rejects.toThrow('did not hand back');
    last().onCmd = () => Promise.reject(new Error('invalid command'));
    await expect(ed.getContent()).rejects.toThrow('invalid command');
  });

  test('when Vim is not running the last seen content is returned', async () => {
    const ed = editor();
    await expect(ed.getContent()).resolves.toBe('');
    await ed.open('a.md', 'seed');
    ed.close();
    await expect(ed.getContent()).resolves.toBe('seed');
  });
});

describe('events from Vim', () => {
  test(':w exports reach onSave; other files are ignored', async () => {
    const ed = editor();
    const saved = [];
    ed.onSave((t) => saved.push(t));
    await ed.open('a.md', 'x');
    last().export('typed\n');
    last().export('other', '/tmp/.nr-insert');
    expect(saved).toEqual(['typed\n']);
  });

  test('keystrokes mark the session dirty until it closes', async () => {
    const ed = editor();
    const dirty = jest.fn();
    ed.onDirty(dirty);
    await ed.open('a.md', 'x');
    els.input.dispatchEvent(new KeyboardEvent('keydown', { key: 'i' }));
    expect(dirty).toHaveBeenCalledTimes(1);
    ed.close();
    els.input.dispatchEvent(new KeyboardEvent('keydown', { key: 'x' }));
    expect(dirty).toHaveBeenCalledTimes(1);
  });

  test('a quit closes the session and fails a pending read', async () => {
    const ed = editor();
    const closes = [];
    ed.onClose((e) => closes.push(e));
    await ed.open('a.md', 'x');
    const read = ed.getContent();
    last().onVimExit(0);
    await expect(read).rejects.toThrow('quit');
    expect(closes).toEqual([{ crashed: false }]);
    expect(ed.running).toBe(false);
  });

  test('a crash after start records a failure and reports crashed', async () => {
    const ed = editor();
    const closes = [];
    ed.onClose((e) => closes.push(e));
    await ed.open('a.md', 'x');
    const v = last();
    const read = ed.getContent();
    const err = new Error('boom');
    v.onError(err);
    await expect(read).rejects.toBe(err);
    expect(closes).toEqual([{ crashed: true, error: err }]);
    expect(getVimModeState().failures).toBe(1);
    expect(v.cmds).toContain('qall!');
  });
});

describe('replace, insert, focus, close', () => {
  test('replace restarts Vim on new content; the old instance is ignored', async () => {
    const ed = editor();
    const saved = [];
    ed.onSave((t) => saved.push(t));
    await ed.open('a.md', 'old');
    const first = last();
    await ed.replace('new text');
    const second = last();
    expect(second).not.toBe(first);
    expect(first.cmds).toContain('qall!');
    expect(second.startOpts.files).toEqual({ '/tmp/a.md': 'new text' });
    first.export('stale');
    expect(saved).toEqual([]);
    expect(ed.content).toBe('new text');
    expect(ed.running).toBe(true);
  });

  test('a failed replace closes the session', async () => {
    const ed = editor();
    await ed.open('a.md', 'old');
    FakeVim.mode = 'throw';
    await expect(ed.replace('x')).rejects.toThrow('cannot start');
    expect(els.container.hidden).toBe(true);
  });

  test('insert stages the text as a file and reads it below the cursor', async () => {
    const ed = editor();
    expect(await ed.insert('nope')).toBe(false); // not running
    await ed.open('a.md', 'x');
    const v = last();
    expect(await ed.insert('![img](/r/1)')).toBe(true);
    expect(v.drops).toEqual([['tmp/.nr-insert', '![img](/r/1)']]);
    expect(v.cmds[v.cmds.length - 1]).toMatch(/^silent buffer # \| silent read \/tmp\/\.nr-insert/);
    v.dropFails = true;
    expect(await ed.insert('again')).toBe(false);
  });

  test('focus forwards to Vim and is a no-op without it', async () => {
    const ed = editor();
    ed.focus();
    await ed.open('a.md', 'x');
    ed.focus();
    expect(last().focused).toBe(true);
  });

  test('close quits Vim, forces the worker down after a grace period, and is idempotent', async () => {
    const ed = editor();
    await ed.open('a.md', 'x');
    const v = last();
    jest.useFakeTimers();
    ed.close();
    ed.close();
    await Promise.resolve();
    expect(v.cmds).toEqual(['qall!']);
    expect(v.worker.terminate).not.toHaveBeenCalled();
    jest.advanceTimersByTime(QUIT_GRACE_MS);
    expect(v.worker.terminate).toHaveBeenCalledTimes(1);
    expect(els.container.hidden).toBe(true);
  });

  test('a worker that is already gone does not throw on teardown', async () => {
    const ed = editor();
    await ed.open('a.md', 'x');
    last().worker.terminate.mockImplementation(() => { throw new Error('gone'); });
    last().onCmd = () => Promise.reject(new Error('Vim is wedged'));
    jest.useFakeTimers();
    ed.close();
    await Promise.resolve();
    await Promise.resolve();
    expect(() => jest.advanceTimersByTime(QUIT_GRACE_MS)).not.toThrow();
  });
});
