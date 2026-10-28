// UI Integration Tests for linking feature
// Run with: node --test web/app.test.cjs

const { test, describe, beforeEach, mock } = require('node:test');
const assert = require('node:assert');
const { JSDOM } = require('jsdom');

// Test helpers
function setupDOM() {
  const dom = new JSDOM(`
    <!DOCTYPE html>
    <html>
      <body>
        <article id="note"></article>
      </body>
    </html>
  `, { url: 'http://localhost/' });
  
  global.document = dom.window.document;
  global.DocumentFragment = dom.window.DocumentFragment;
  global.Element = dom.window.Element;
  
  // Mock $ function from app.js
  global.$ = (id) => document.getElementById(id);
  global.slug = 'demo';
  
  return dom;
}

// Extract core functions from app.js for testing
// (In production, these would be exported modules)
function panel(title, items, makeItem) {
  if (!items || !items.length) return null;
  const sec = document.createElement("section");
  sec.className = "links-panel";
  const h = document.createElement("h2");
  h.textContent = title;
  sec.append(h);
  const ul = document.createElement("ul");
  for (const item of items) ul.append(makeItem(item));
  sec.append(ul);
  return sec;
}

function noteLink(path, text) {
  const a = document.createElement("a");
  a.href = `/n/${encodeURIComponent(slug)}/note?path=${encodeURIComponent(path)}`;
  a.className = "wikilink";
  a.dataset.path = path;
  a.textContent = text;
  return a;
}

function linksPanels(n) {
  const frag = document.createDocumentFragment();
  const out = panel("Outgoing links", n.outgoing, (l) => {
    const li = document.createElement("li");
    if (l.path) {
      li.append(noteLink(l.path, l.label || l.title || l.target));
      const m = document.createElement("span");
      m.className = "muted";
      m.textContent = ` — ${l.path}`;
      li.append(m);
    } else {
      const s = document.createElement("span");
      s.className = "wikilink dead";
      s.textContent = l.label || l.target;
      s.title = "unresolved";
      li.append(s);
    }
    return li;
  });
  const back = panel("Backlinks", n.backlinks, (l) => {
    const li = document.createElement("li");
    li.append(noteLink(l.path, l.title || l.path));
    return li;
  });
  if (out) frag.append(out);
  if (back) frag.append(back);
  return frag;
}

// Tests
describe('UI Integration: Links Panels', () => {
  beforeEach(() => {
    setupDOM();
  });

  test('linksPanels creates outgoing panel with resolved links', () => {
    const noteData = {
      outgoing: [
        { target: 'beta', path: 'beta.md', title: 'Beta', label: 'the beta note' },
        { target: 'gamma', path: 'gamma.md', title: 'Gamma' }
      ]
    };

    const panels = linksPanels(noteData);
    $('note').append(panels);

    const section = document.querySelector('.links-panel');
    assert.ok(section, 'should create links-panel section');
    
    const h2 = section.querySelector('h2');
    assert.strictEqual(h2.textContent, 'Outgoing links');
    
    const links = section.querySelectorAll('a.wikilink');
    assert.strictEqual(links.length, 2, 'should have 2 resolved links');
    
    // First link uses label
    assert.strictEqual(links[0].textContent, 'the beta note');
    assert.strictEqual(links[0].dataset.path, 'beta.md');
    assert.strictEqual(links[0].href, 'http://localhost/n/demo/note?path=beta.md');
    
    // Second link uses title
    assert.strictEqual(links[1].textContent, 'Gamma');
    assert.strictEqual(links[1].dataset.path, 'gamma.md');
  });

  test('linksPanels creates dead spans for unresolved targets', () => {
    const noteData = {
      outgoing: [
        { target: 'missing', path: '', title: '' }
      ]
    };

    const panels = linksPanels(noteData);
    $('note').append(panels);

    const dead = document.querySelector('.wikilink.dead');
    assert.ok(dead, 'should create dead wikilink span');
    assert.strictEqual(dead.textContent, 'missing');
    assert.strictEqual(dead.title, 'unresolved');
    assert.strictEqual(dead.tagName, 'SPAN');
  });

  test('linksPanels creates backlinks panel', () => {
    const noteData = {
      backlinks: [
        { path: 'index.md', title: 'Home' },
        { path: 'other.md', title: 'Other' }
      ]
    };

    const panels = linksPanels(noteData);
    $('note').append(panels);

    const sections = document.querySelectorAll('.links-panel');
    assert.strictEqual(sections.length, 1, 'should have 1 panel (backlinks only)');
    
    const h2 = sections[0].querySelector('h2');
    assert.strictEqual(h2.textContent, 'Backlinks');
    
    const links = sections[0].querySelectorAll('a.wikilink');
    assert.strictEqual(links.length, 2);
    assert.strictEqual(links[0].textContent, 'Home');
    assert.strictEqual(links[0].dataset.path, 'index.md');
  });

  test('linksPanels creates both panels when data present', () => {
    const noteData = {
      outgoing: [{ target: 'beta', path: 'beta.md', title: 'Beta' }],
      backlinks: [{ path: 'alpha.md', title: 'Alpha' }]
    };

    const panels = linksPanels(noteData);
    $('note').append(panels);

    const sections = document.querySelectorAll('.links-panel');
    assert.strictEqual(sections.length, 2, 'should have 2 panels');
    assert.strictEqual(sections[0].querySelector('h2').textContent, 'Outgoing links');
    assert.strictEqual(sections[1].querySelector('h2').textContent, 'Backlinks');
  });

  test('linksPanels returns empty fragment when no links', () => {
    const noteData = {
      outgoing: [],
      backlinks: []
    };

    const panels = linksPanels(noteData);
    $('note').append(panels);

    const sections = document.querySelectorAll('.links-panel');
    assert.strictEqual(sections.length, 0, 'should create no panels for empty data');
  });

  test('linksPanels handles missing backlinks/outgoing gracefully', () => {
    const noteData = {}; // index unavailable - no links data

    const panels = linksPanels(noteData);
    $('note').append(panels);

    const sections = document.querySelectorAll('.links-panel');
    assert.strictEqual(sections.length, 0, 'should handle missing data gracefully');
  });

  test('noteLink creates proper anchor with data-path', () => {
    const link = noteLink('foo/bar.md', 'Bar');
    
    assert.strictEqual(link.tagName, 'A');
    assert.strictEqual(link.className, 'wikilink');
    assert.strictEqual(link.dataset.path, 'foo/bar.md');
    assert.strictEqual(link.textContent, 'Bar');
    assert.strictEqual(link.href, 'http://localhost/n/demo/note?path=foo%2Fbar.md');
  });

  test('outgoing link includes path suffix when resolved', () => {
    const noteData = {
      outgoing: [{ target: 'test', path: 'deeply/nested/test.md', title: 'Test' }]
    };

    const panels = linksPanels(noteData);
    $('note').append(panels);

    const li = document.querySelector('.links-panel li');
    const muted = li.querySelector('.muted');
    assert.ok(muted, 'should have muted path suffix');
    assert.strictEqual(muted.textContent, ' — deeply/nested/test.md');
  });
});

describe('UI Integration: SPA Click Handler', () => {
  beforeEach(() => {
    setupDOM();
  });

  test('plain left-click on wikilink should trigger SPA navigation', () => {
    const link = noteLink('target.md', 'Target');
    $('note').appendChild(link);

    let navigatedTo = null;
    const mockOpenNote = (path) => { navigatedTo = path; };

    // Simulate click handler
    const event = new document.defaultView.MouseEvent('click', {
      bubbles: true,
      cancelable: true,
      button: 0
    });
    
    link.addEventListener('click', (e) => {
      const a = e.target.closest('a.wikilink[data-path]');
      if (!a) return;
      if (e.defaultPrevented || e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;
      e.preventDefault();
      mockOpenNote(a.dataset.path);
    });

    link.dispatchEvent(event);

    assert.strictEqual(navigatedTo, 'target.md', 'should call openNote with data-path');
    assert.ok(event.defaultPrevented, 'should preventDefault on left-click');
  });

  test('middle-click should not trigger SPA navigation', () => {
    const link = noteLink('target.md', 'Target');
    $('note').appendChild(link);

    let navigated = false;
    const mockOpenNote = () => { navigated = true; };

    const event = new document.defaultView.MouseEvent('click', {
      bubbles: true,
      cancelable: true,
      button: 1 // middle button
    });
    
    link.addEventListener('click', (e) => {
      const a = e.target.closest('a.wikilink[data-path]');
      if (!a) return;
      if (e.defaultPrevented || e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;
      e.preventDefault();
      mockOpenNote(a.dataset.path);
    });

    link.dispatchEvent(event);

    assert.strictEqual(navigated, false, 'should not navigate on middle-click');
    assert.strictEqual(event.defaultPrevented, false, 'should not preventDefault');
  });

  test('cmd/ctrl-click should not trigger SPA navigation', () => {
    const link = noteLink('target.md', 'Target');
    $('note').appendChild(link);

    let navigated = false;
    const mockOpenNote = () => { navigated = true; };

    const eventMeta = new document.defaultView.MouseEvent('click', {
      bubbles: true,
      cancelable: true,
      button: 0,
      metaKey: true
    });
    
    link.addEventListener('click', (e) => {
      const a = e.target.closest('a.wikilink[data-path]');
      if (!a) return;
      if (e.defaultPrevented || e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;
      e.preventDefault();
      mockOpenNote(a.dataset.path);
    });

    link.dispatchEvent(eventMeta);

    assert.strictEqual(navigated, false, 'should not navigate on cmd-click');
  });

  test('click on non-wikilink should not trigger handler', () => {
    const regularLink = document.createElement('a');
    regularLink.href = 'http://example.com';
    regularLink.textContent = 'External';
    $('note').appendChild(regularLink);

    let navigated = false;
    const mockOpenNote = () => { navigated = true; };

    const event = new document.defaultView.MouseEvent('click', {
      bubbles: true,
      cancelable: true,
      button: 0
    });
    
    regularLink.addEventListener('click', (e) => {
      const a = e.target.closest('a.wikilink[data-path]');
      if (!a) return;
      if (e.defaultPrevented || e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;
      e.preventDefault();
      mockOpenNote(a.dataset.path);
    });

    regularLink.dispatchEvent(event);

    assert.strictEqual(navigated, false, 'should not trigger for non-wikilink');
  });
});
