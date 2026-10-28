/**
 * Unit tests for lib/vim-editor.js — the canvas snapshot helper that
 * resizeBitmap relies on. jsdom has no rasteriser, so getContext is
 * stubbed per path: rasterisable, null context, and a throwing context.
 */
import { jest } from '@jest/globals';
import { VimEditor } from '../../lib/vim-editor.js';

function makeEditor() {
  const canvas = document.createElement('canvas');
  canvas.width = 40;
  canvas.height = 20;
  return new VimEditor({ VimWasm: class {}, canvas, input: null });
}

describe('VimEditor.snapshot', () => {
  test('copies the bitmap when the browser can rasterise', () => {
    const drawImage = jest.fn();
    const spy = jest.spyOn(HTMLCanvasElement.prototype, 'getContext')
      .mockReturnValue({ drawImage });
    try {
      const ed = makeEditor();
      const copy = ed.snapshot();
      expect(copy).toBeTruthy();
      expect(copy.width).toBe(40);
      expect(copy.height).toBe(20);
      expect(drawImage).toHaveBeenCalledWith(ed.canvas, 0, 0);
    } finally {
      spy.mockRestore();
    }
  });

  test('returns null when there is no 2d context', () => {
    const spy = jest.spyOn(HTMLCanvasElement.prototype, 'getContext')
      .mockReturnValue(null);
    try {
      expect(makeEditor().snapshot()).toBeNull();
    } finally {
      spy.mockRestore();
    }
  });

  test('returns null when drawing blows up', () => {
    const spy = jest.spyOn(HTMLCanvasElement.prototype, 'getContext')
      .mockImplementation(() => { throw new Error('no canvas'); });
    try {
      expect(makeEditor().snapshot()).toBeNull();
    } finally {
      spy.mockRestore();
    }
  });

  test('resizeBitmap keeps the picture when it can, still resizes when it cannot', () => {
    const ed = makeEditor();
    // Same size: early return, no snapshot needed.
    ed.resizeBitmap(40, 20);
    expect(ed.canvas.width).toBe(40);

    // A rasterisable context: snapshot + redraw both run.
    const drawImage = jest.fn();
    const spy = jest.spyOn(HTMLCanvasElement.prototype, 'getContext')
      .mockReturnValue({ drawImage });
    try {
      ed.resizeBitmap(80, 30);
      expect(ed.canvas.width).toBe(80);
      expect(ed.canvas.height).toBe(30);
      expect(drawImage).toHaveBeenCalledWith(ed.canvas, 0, 0); // snapshot
      expect(drawImage).toHaveBeenCalledWith(expect.any(HTMLCanvasElement), 0, 0); // redraw
    } finally {
      spy.mockRestore();
    }

    // No context: the picture cannot be restored, but the bitmap resizes.
    ed.resizeBitmap(60, 15);
    expect(ed.canvas.width).toBe(60);
    expect(ed.canvas.height).toBe(15);
  });
});
