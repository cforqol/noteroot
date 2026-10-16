// Unit tests for the plugin status panel renderer (stage 9).
// Coverage must stay complete: lib/ is held to 100% by jest.config.

import { fetchPanels, applyTheme, renderPanelChips } from '/lib/panels.js';

function jsonRes(status, body) {
  return {
    ok: status >= 200 && status < 300,
    status,
    json: async () => body,
  };
}

describe('fetchPanels', () => {
  test('returns the panels map on success', async () => {
    const panels = await fetchPanels('work', async () =>
      jsonRes(200, { 'word-count': { text: '3 words', level: 'info' } }));
    expect(panels).toEqual({ 'word-count': { text: '3 words', level: 'info' } });
  });

  test('encodes the slug in the request path', async () => {
    let url = null;
    await fetchPanels('a/b', async (u) => {
      url = u;
      return jsonRes(200, {});
    });
    expect(url).toBe('/n/a%2Fb/plugins/panels');
  });

  test('returns {} on a non-OK response', async () => {
    const panels = await fetchPanels('work', async () => jsonRes(500, { error: 'x' }));
    expect(panels).toEqual({});
  });

  test('returns {} when fetch throws', async () => {
    const panels = await fetchPanels('work', async () => {
      throw new Error('offline');
    });
    expect(panels).toEqual({});
  });

  test('uses globalThis.fetch when no impl is passed', async () => {
    const orig = globalThis.fetch;
    globalThis.fetch = async () => jsonRes(200, { ok: { text: 'x' } });
    try {
      expect(await fetchPanels('work')).toEqual({ ok: { text: 'x' } });
    } finally {
      globalThis.fetch = orig;
    }
  });
});

describe('applyTheme', () => {
  test('applies a theme panel name to the document root', () => {
    expect(applyTheme({ theme: { type: 'theme', name: 'dark' } })).toBe('dark');
    expect(document.documentElement.dataset.theme).toBe('dark');
    delete document.documentElement.dataset.theme;
  });

  test('returns null for a missing panel', () => {
    expect(applyTheme({})).toBeNull();
  });

  test('returns null for a non-string or empty name', () => {
    expect(applyTheme({ theme: { name: 42 } })).toBeNull();
    expect(applyTheme({ theme: { name: '' } })).toBeNull();
    expect(applyTheme({ theme: null })).toBeNull();
    expect(applyTheme(null)).toBeNull();
  });
});

describe('renderPanelChips', () => {
  test('renders a chip per text panel and applies the theme', () => {
    const container = document.createElement('div');
    renderPanelChips(container, {
      'word-count': { text: '12 words', level: 'info' },
      theme: { type: 'theme', name: 'light' },
    });
    expect(document.documentElement.dataset.theme).toBe('light');
    delete document.documentElement.dataset.theme;
    expect(container.children).toHaveLength(1);
    const chip = container.children[0];
    expect(chip.className).toBe('panel-chip panel-info');
    expect(chip.dataset.panel).toBe('word-count');
    expect(chip.textContent).toBe('12 words');
  });

  test('skips theme panels, textless specs, and null specs', () => {
    const container = document.createElement('div');
    renderPanelChips(container, {
      // theme panels are applied to the document, never chipped —
      // give it text so the type check is what skips it
      theme: { type: 'theme', name: 'dark', text: 'dark' },
      empty: {},
      broken: { text: 7 },
      nothing: null,
    });
    expect(container.children).toHaveLength(0);
    delete document.documentElement.dataset.theme;
  });

  test('omits the level class when the spec has none', () => {
    const container = document.createElement('div');
    renderPanelChips(container, { plain: { text: 'hi' } });
    expect(container.children[0].className).toBe('panel-chip');
  });

  test('handles a null panels map', () => {
    const container = document.createElement('div');
    renderPanelChips(container, null);
    expect(container.children).toHaveLength(0);
  });
});
