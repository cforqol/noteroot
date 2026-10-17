/**
 * Small modal dialog and context-menu helpers (no framework, no
 * window.prompt): promise-based so callers read like synchronous code.
 */

function el(doc, tag, className, text) {
  const e = doc.createElement(tag);
  if (className) e.className = className;
  if (text !== undefined) e.textContent = text;
  return e;
}

function buildInput(doc, f) {
  if (f.type === 'select') {
    const select = doc.createElement('select');
    for (const opt of f.options || []) {
      const o = doc.createElement('option');
      o.value = opt.value;
      o.textContent = opt.label;
      o.disabled = Boolean(opt.disabled);
      select.append(o);
    }
    if (f.value !== undefined) select.value = f.value;
    return select;
  }
  const input = doc.createElement('input');
  input.type = f.type || 'text';
  input.autocomplete = 'off';
  if (f.type === 'checkbox') input.checked = Boolean(f.value);
  else input.value = f.value ?? '';
  if (f.placeholder) input.placeholder = f.placeholder;
  return input;
}

function readValues(inputs) {
  const values = {};
  for (const [name, input] of Object.entries(inputs)) {
    if (input.type === 'checkbox') values[name] = input.checked;
    else values[name] = input.type === 'text' ? input.value.trim() : input.value;
  }
  return values;
}

/**
 * Show a modal form. Resolves with {field: value} on OK, with
 * {__action: name} when an extra action button is pressed, or with null
 * on cancel (button, Escape or backdrop click). Focus moves into the
 * dialog, Tab cycles inside it, and focus returns where it was on close.
 *
 * @param {object} o
 * @param {string} o.title
 * @param {string} [o.message]
 * @param {Node} [o.extra]             custom content under the message
 * @param {{name:string,label:string,type?:string,value?:string|boolean,placeholder?:string,hint?:string,options?:{value:string,label:string,disabled?:boolean}[]}[]} [o.fields]
 *   type "checkbox" yields a boolean; type "select" takes options
 * @param {(values:object)=>string|null} [o.preview]  live line under the fields
 * @param {string} [o.okLabel]
 * @param {string|null} [o.cancelLabel] null drops the Cancel button
 * @param {boolean} [o.danger]          style OK as destructive
 * @param {boolean} [o.wide]            wider dialog (lists, reports)
 * @param {{label:string,name:string}[]} [o.actions]  extra buttons
 * @param {(values:object)=>string|null} [o.validate]  error text keeps the dialog open
 */
export function formDialog(o, doc = document) {
  const {
    title, message = '', extra = null, fields = [], preview = null, okLabel = 'OK', cancelLabel = 'Cancel',
    danger = false, wide = false, actions = [], validate = null,
  } = o;
  return new Promise((resolve) => {
    const opener = doc.activeElement;
    const root = el(doc, 'div', wide ? 'modal dialog dialog-wide' : 'modal dialog');
    root.setAttribute('role', 'dialog');
    root.setAttribute('aria-modal', 'true');
    root.setAttribute('aria-label', title);

    const backdrop = el(doc, 'div', 'modal-backdrop');
    const content = el(doc, 'div', 'modal-content dialog-content');
    const head = el(doc, 'div', 'modal-header');
    head.append(el(doc, 'h2', '', title));

    const form = el(doc, 'form', 'modal-body dialog-form');
    form.noValidate = true;
    if (message) form.append(el(doc, 'p', 'help-text dialog-message', message));
    if (extra) form.append(extra);

    const inputs = {};
    for (const f of fields) {
      const label = el(doc, 'label', 'dialog-field');
      const span = el(doc, 'span', '', f.label);
      const input = buildInput(doc, f);
      input.name = f.name;
      if (f.type === 'checkbox') {
        label.classList.add('dialog-check');
        label.append(input, span);
      } else {
        label.append(span, input);
      }
      if (f.hint) label.append(el(doc, 'small', 'help-text small', f.hint));
      inputs[f.name] = input;
      form.append(label);
    }

    const live = el(doc, 'p', 'dialog-preview');
    live.setAttribute('aria-live', 'polite');
    const refresh = () => {
      const text = preview(readValues(inputs));
      live.textContent = text || '';
      live.hidden = !text;
    };
    if (preview) {
      form.append(live);
      form.addEventListener('input', refresh);
      form.addEventListener('change', refresh);
      refresh();
    }

    const error = el(doc, 'p', 'dialog-error');
    error.setAttribute('role', 'alert');
    error.hidden = true;
    form.append(error);

    const buttons = el(doc, 'div', 'dialog-buttons');
    const finish = (value) => {
      doc.removeEventListener('keydown', onKey, true);
      root.remove();
      if (opener?.focus && opener.isConnected) opener.focus();
      resolve(value);
    };
    for (const a of actions) {
      const b = el(doc, 'button', 'btn', a.label);
      b.type = 'button';
      b.dataset.action = a.name;
      b.addEventListener('click', () => finish({ __action: a.name }));
      buttons.append(b);
    }
    buttons.append(el(doc, 'span', 'spacer'));
    if (cancelLabel !== null) {
      const cancel = el(doc, 'button', 'btn', cancelLabel);
      cancel.type = 'button';
      cancel.addEventListener('click', () => finish(null));
      buttons.append(cancel);
    }
    const ok = el(doc, 'button', danger ? 'btn danger' : 'btn primary', okLabel);
    ok.type = 'submit';
    buttons.append(ok);
    form.append(buttons);

    form.addEventListener('input', () => { error.hidden = true; });
    form.addEventListener('submit', (e) => {
      e.preventDefault();
      const values = readValues(inputs);
      const problem = validate ? validate(values) : null;
      if (problem) {
        error.textContent = problem;
        error.hidden = false;
        return;
      }
      finish(values);
    });
    backdrop.addEventListener('click', () => finish(null));
    function onKey(e) {
      if (e.key === 'Escape') {
        e.stopPropagation();
        finish(null);
      } else if (e.key === 'Tab') {
        // Keep keyboard focus inside the dialog.
        const stops = [...content.querySelectorAll('input, select, button')].filter((x) => !x.disabled);
        const i = stops.indexOf(doc.activeElement);
        const next = e.shiftKey ? (i <= 0 ? stops.length - 1 : i - 1) : (i === stops.length - 1 ? 0 : i + 1);
        e.preventDefault();
        stops[next].focus();
      }
    }
    doc.addEventListener('keydown', onKey, true);

    content.append(head, form);
    root.append(backdrop, content);
    doc.body.append(root);
    const first = Object.values(inputs)[0] || ok;
    first.focus();
    if (first.select) first.select();
  });
}

/** OK/Cancel confirmation. Resolves true or false. */
export async function confirmDialog({ title, message, extra = null, okLabel = 'OK', danger = false }, doc = document) {
  const r = await formDialog({ title, message, extra, okLabel, danger }, doc);
  return r !== null;
}

/** A message with a single button (reports, results). */
export async function infoDialog({ title, message = '', extra = null, okLabel = 'Done', wide = false }, doc = document) {
  await formDialog({ title, message, extra, okLabel, cancelLabel: null, wide }, doc);
}

let closeOpenMenu = null;

/**
 * Pop a context menu at viewport coordinates. Items:
 * {label, onSelect, danger?} or {separator: true}. Closes on selection,
 * outside press or Escape. Returns the close function.
 */
export function showMenu(x, y, items, doc = document) {
  if (closeOpenMenu) closeOpenMenu();
  const menu = doc.createElement('div');
  menu.className = 'ctx-menu';
  menu.setAttribute('role', 'menu');
  const close = () => {
    doc.removeEventListener('pointerdown', onOutside, true);
    doc.removeEventListener('keydown', onKey, true);
    menu.remove();
    if (closeOpenMenu === close) closeOpenMenu = null;
  };
  for (const item of items) {
    if (item.separator) {
      const sep = doc.createElement('div');
      sep.className = 'ctx-sep';
      sep.setAttribute('role', 'separator');
      menu.append(sep);
      continue;
    }
    const b = doc.createElement('button');
    b.type = 'button';
    b.className = item.danger ? 'ctx-item danger' : 'ctx-item';
    b.setAttribute('role', 'menuitem');
    b.textContent = item.label;
    b.addEventListener('click', () => {
      close();
      item.onSelect();
    });
    menu.append(b);
  }
  function onOutside(e) {
    if (!menu.contains(e.target)) close();
  }
  function onKey(e) {
    if (e.key === 'Escape') {
      e.stopPropagation();
      close();
    }
  }
  doc.addEventListener('pointerdown', onOutside, true);
  doc.addEventListener('keydown', onKey, true);
  menu.style.left = `${x}px`;
  menu.style.top = `${y}px`;
  doc.body.append(menu);
  // Keep the menu on screen: shift back by any overflow.
  const r = menu.getBoundingClientRect();
  const win = doc.defaultView;
  if (r.right > win.innerWidth) menu.style.left = `${Math.max(0, win.innerWidth - r.width - 4)}px`;
  if (r.bottom > win.innerHeight) menu.style.top = `${Math.max(0, win.innerHeight - r.height - 4)}px`;
  closeOpenMenu = close;
  menu.querySelector('button')?.focus();
  return close;
}
