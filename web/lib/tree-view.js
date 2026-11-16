/**
 * Notebook tree renderer: collapsible folders with user-assigned
 * icon/color, recursive note counts in brackets, active-note highlight,
 * and a per-row actions button (also reachable by right-click).
 */
import { annotateCounts } from './tree.js';

const INDENT_PX = 12;
const LONG_PRESS_MS = 450;
const MOVE_THRESHOLD_PX = 10;

// Touch has neither a modifier-key click (desktop's multi-select) nor
// native HTML5 drag-and-drop, so a long press stands in for both:
// held still, it toggles multi-select; dragged, it moves the note,
// exactly like the existing mouse-drag path. Wires touchstart/move/end
// on `main` (the note row's button) without touching any existing
// mouse/click/dragstart listeners, so desktop behaviour is untouched.
function attachLongPress(doc, main, row, node, onDropNote, onLongPress) {
  let timer = null;
  let start = null;
  let longPressed = false;
  let dragging = false;
  let target = null;

  const clearTarget = () => {
    if (target) target.row.classList.remove('drop-target');
    target = null;
  };
  const reset = () => {
    clearTimeout(timer);
    timer = null;
    start = null;
    longPressed = false;
    if (dragging) {
      main.classList.remove('touch-dragging');
      dragging = false;
    }
    clearTarget();
  };
  const findDropTarget = (x, y) => {
    const el = doc.elementFromPoint ? doc.elementFromPoint(x, y) : null;
    const hit = el?.closest?.('.node.dir:not(.locked), .root-node');
    if (!hit) return null;
    const hitRow = hit.closest('.row');
    if (!hitRow) return null;
    return { path: hit.dataset.path, row: hitRow };
  };

  main.addEventListener('touchstart', (e) => {
    if (e.touches.length !== 1) return;
    const t = e.touches[0];
    start = { x: t.clientX, y: t.clientY };
    longPressed = false;
    timer = setTimeout(() => {
      longPressed = true;
      main.classList.add('long-pressed');
    }, LONG_PRESS_MS);
  }, { passive: true });

  main.addEventListener('touchmove', (e) => {
    if (!start) return;
    const t = e.touches[0];
    const dx = t.clientX - start.x;
    const dy = t.clientY - start.y;
    if (!longPressed) {
      // Moved before the long-press fired: an ordinary scroll/swipe,
      // not this gesture. Let the browser handle it.
      if (Math.hypot(dx, dy) > MOVE_THRESHOLD_PX) { clearTimeout(timer); start = null; }
      return;
    }
    // Long press already fired: this is now a drag. Stop the page from
    // scrolling under the finger and track the drop target.
    e.preventDefault();
    if (!dragging) { dragging = true; main.classList.add('touch-dragging'); }
    const hit = findDropTarget(t.clientX, t.clientY);
    if (hit?.row !== target?.row) {
      clearTarget();
      if (hit) { hit.row.classList.add('drop-target'); target = hit; }
    }
  }, { passive: false });

  main.addEventListener('touchend', () => {
    if (dragging) {
      if (target) onDropNote(node.path, target.path);
    } else if (longPressed) {
      onLongPress(node.path);
    }
    main.classList.remove('long-pressed');
    reset();
  });
  main.addEventListener('touchcancel', () => { main.classList.remove('long-pressed'); reset(); });
}

function buildRootDropZone(doc, onDropNote) {
  const row = doc.createElement('div');
  row.className = 'row root-zone';
  const btn = doc.createElement('div');
  btn.className = 'root-node';
  btn.dataset.path = '';
  btn.setAttribute('aria-label', 'Notebook root — drop here to move a note to the root folder');
  const icon = doc.createElement('span');
  icon.className = 'icon';
  icon.textContent = '📂';
  const label = doc.createElement('span');
  label.className = 'label';
  label.textContent = '/ root';
  btn.append(icon, label);
  row.append(btn);
  row.addEventListener('dragover', (e) => {
    if (!e.dataTransfer.types.includes('text/x-noteroot-path')) return;
    e.preventDefault();
    e.dataTransfer.dropEffect = 'move';
    row.classList.add('drop-target');
  });
  row.addEventListener('dragleave', (e) => {
    if (!row.contains(e.relatedTarget)) row.classList.remove('drop-target');
  });
  row.addEventListener('drop', (e) => {
    e.preventDefault();
    row.classList.remove('drop-target');
    const notePath = e.dataTransfer.getData('text/x-noteroot-path');
    if (notePath) onDropNote(notePath, '');
  });
  return row;
}

/**
 * @param {HTMLElement} container
 * @param {object} root         server tree ({children: [...]})
 * @param {object} ctx
 * @param {string|null} [ctx.current]            open note path
 * @param {string|null} [ctx.splitPath]          note open in the second pane
 * @param {Set<string>} [ctx.multi]              notes picked for a bulk action
 * @param {string|null} [ctx.selected]           highlighted folder path
 * @param {Set<string>} [ctx.collapsed]          collapsed folder paths
 * @param {Object<string,{emoji:string,color:string}>} [ctx.decorations]
 * @param {(path:string, el:HTMLElement, ev:MouseEvent)=>void} [ctx.onOpen]
 * @param {(path:string, collapsed:boolean)=>void} [ctx.onToggle]
 * @param {(node:object, x:number, y:number)=>void} [ctx.onMenu]
 * @param {(notePath:string, destPath:string)=>void} [ctx.onDropNote]
 * @param {(notePath:string)=>void} [ctx.onLongPress] touch long-press
 *   held still on a note (toggle multi-select); a long-press that then
 *   moves calls onDropNote instead, same as a mouse drag would.
 * @returns {{notes:number, folders:number}} totals for the status line
 */
export function renderTree(container, root, ctx = {}, doc = document) {
  const {
    current = null, splitPath = null, multi = new Set(), selected = null,
    collapsed = new Set(), decorations = {},
    onOpen = () => {}, onToggle = () => {}, onMenu = () => {},
    onDropNote = null, onLongPress = null,
  } = ctx;
  const totals = annotateCounts(root);

  const menuButton = (node) => {
    const b = doc.createElement('button');
    b.type = 'button';
    b.className = 'row-menu';
    b.textContent = '⋯';
    b.setAttribute('aria-label', `Actions for ${node.name}`);
    b.addEventListener('click', (e) => {
      e.stopPropagation();
      const r = b.getBoundingClientRect();
      onMenu(node, r.left, r.bottom);
    });
    return b;
  };

  const build = (parent, depth) => {
    const frag = doc.createDocumentFragment();
    for (const node of parent.children || []) {
      const row = doc.createElement('div');
      row.className = 'row';
      row.addEventListener('contextmenu', (e) => {
        e.preventDefault();
        onMenu(node, e.clientX, e.clientY);
      });

      const main = doc.createElement('button');
      main.type = 'button';
      main.style.paddingLeft = `${depth * INDENT_PX}px`;
      main.dataset.path = node.path;
      const caret = doc.createElement('span');
      caret.className = 'caret';

      // A locked node is hidden behind a content lock the session has not
      // opened: it stays visible (its existence is not secret) but is
      // clearly marked, and the server omits a locked folder's children.
      if (node.locked) main.classList.add('locked');
      if (node.isDir) {
        const isCollapsed = collapsed.has(node.path);
        const deco = decorations[node.path] || {};
        main.className = 'node dir';
        main.setAttribute('aria-expanded', String(!isCollapsed));
        if (node.path === selected) main.classList.add('selected');
        if (node.locked) main.classList.add('locked');
        caret.textContent = isCollapsed ? '▸' : '▾';
        const icon = doc.createElement('span');
        icon.className = 'icon';
        icon.textContent = node.locked ? '🔒' : (deco.emoji || (isCollapsed ? '📁' : '📂'));
        const label = doc.createElement('span');
        label.className = 'label';
        label.textContent = node.name;
        if (deco.color && !node.locked) label.style.color = deco.color;
        const count = doc.createElement('span');
        count.className = 'count';
        count.textContent = node.locked ? '' : `[${node.noteCount}]`;
        main.append(caret, icon, label, count);
        main.addEventListener('click', () => onToggle(node.path, !isCollapsed));
        if (onDropNote && !node.locked) {
          row.addEventListener('dragover', (e) => {
            if (!e.dataTransfer.types.includes('text/x-noteroot-path')) return;
            e.preventDefault();
            e.dataTransfer.dropEffect = 'move';
            row.classList.add('drop-target');
          });
          row.addEventListener('dragleave', (e) => {
            if (!row.contains(e.relatedTarget)) row.classList.remove('drop-target');
          });
          row.addEventListener('drop', (e) => {
            e.preventDefault();
            row.classList.remove('drop-target');
            const notePath = e.dataTransfer.getData('text/x-noteroot-path');
            if (notePath) onDropNote(notePath, node.path);
          });
        }
        row.append(main, menuButton(node));
        frag.append(row);
        if (!isCollapsed && !node.locked) frag.append(build(node, depth + 1));
      } else {
        main.className = 'node';
        if (node.locked) main.classList.add('locked');
        if (node.path === current) main.classList.add('active');
        if (node.path === splitPath) main.classList.add('split-open');
        if (multi.has(node.path)) main.classList.add('multi');
        main.setAttribute('aria-selected', String(multi.has(node.path)));
        const label = doc.createElement('span');
        label.className = 'label';
        label.textContent = node.name.replace(/\.md$/, '');
        if (node.locked) {
          const lock = doc.createElement('span');
          lock.className = 'icon lock-icon';
          lock.textContent = '🔒';
          main.append(caret, lock, label);
        } else {
          main.append(caret, label);
        }
        main.addEventListener('click', (e) => onOpen(node.path, main, e));
        if (onDropNote && !node.locked) {
          main.draggable = true;
          main.addEventListener('dragstart', (e) => {
            e.dataTransfer.effectAllowed = 'move';
            e.dataTransfer.setData('text/x-noteroot-path', node.path);
            main.classList.add('dragging');
          });
          main.addEventListener('dragend', () => main.classList.remove('dragging'));
          if (onLongPress) attachLongPress(doc, main, row, node, onDropNote, onLongPress);
        }
        row.append(main, menuButton(node));
        frag.append(row);
      }
    }
    return frag;
  };

  const body = build(root, 0);
  if (!(root.children || []).length) {
    const empty = doc.createElement('p');
    empty.className = 'muted tree-empty';
    empty.textContent = 'No notes yet. Use "+ Note" to create one.';
    container.replaceChildren(empty);
  } else if (onDropNote) {
    container.replaceChildren(buildRootDropZone(doc, onDropNote), body);
  } else {
    container.replaceChildren(body);
  }
  return totals;
}
