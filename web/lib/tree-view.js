/**
 * Notebook tree renderer: collapsible folders with user-assigned
 * icon/color, recursive note counts in brackets, active-note highlight,
 * and a per-row actions button (also reachable by right-click).
 */
import { annotateCounts } from './tree.js';

const INDENT_PX = 12;

/**
 * @param {HTMLElement} container
 * @param {object} root         server tree ({children: [...]})
 * @param {object} ctx
 * @param {string|null} [ctx.current]            open note path
 * @param {string|null} [ctx.selected]           highlighted folder path
 * @param {Set<string>} [ctx.collapsed]          collapsed folder paths
 * @param {Object<string,{emoji:string,color:string}>} [ctx.decorations]
 * @param {(path:string, el:HTMLElement)=>void} [ctx.onOpen]
 * @param {(path:string, collapsed:boolean)=>void} [ctx.onToggle]
 * @param {(node:object, x:number, y:number)=>void} [ctx.onMenu]
 * @returns {{notes:number, folders:number}} totals for the status line
 */
export function renderTree(container, root, ctx = {}, doc = document) {
  const {
    current = null, selected = null, collapsed = new Set(), decorations = {},
    onOpen = () => {}, onToggle = () => {}, onMenu = () => {},
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

      if (node.isDir) {
        const isCollapsed = collapsed.has(node.path);
        const deco = decorations[node.path] || {};
        main.className = 'node dir';
        main.setAttribute('aria-expanded', String(!isCollapsed));
        if (node.path === selected) main.classList.add('selected');
        caret.textContent = isCollapsed ? '▸' : '▾';
        const icon = doc.createElement('span');
        icon.className = 'icon';
        icon.textContent = deco.emoji || (isCollapsed ? '📁' : '📂');
        const label = doc.createElement('span');
        label.className = 'label';
        label.textContent = node.name;
        if (deco.color) label.style.color = deco.color;
        const count = doc.createElement('span');
        count.className = 'count';
        count.textContent = `[${node.noteCount}]`;
        main.append(caret, icon, label, count);
        main.addEventListener('click', () => onToggle(node.path, !isCollapsed));
        row.append(main, menuButton(node));
        frag.append(row);
        if (!isCollapsed) frag.append(build(node, depth + 1));
      } else {
        main.className = 'node';
        if (node.path === current) main.classList.add('active');
        const label = doc.createElement('span');
        label.className = 'label';
        label.textContent = node.name.replace(/\.md$/, '');
        main.append(caret, label);
        main.addEventListener('click', () => onOpen(node.path, main));
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
    empty.textContent = 'No notes yet. Use “+ Note” to create one.';
    container.replaceChildren(empty);
  } else {
    container.replaceChildren(body);
  }
  return totals;
}
