/**
 * jsdom integration tests for the modal form dialog and context menu.
 */
import { jest } from '@jest/globals';
import { formDialog, confirmDialog, infoDialog, showMenu } from '../../lib/dialog.js';

const $q = (sel) => document.querySelector(sel);
const tick = () => new Promise((r) => setTimeout(r, 0));

beforeEach(() => {
  document.body.innerHTML = '';
});

describe('formDialog', () => {
  test('submits trimmed text values, checkbox booleans and colors', async () => {
    const p = formDialog({
      title: 'T', message: 'msg', okLabel: 'Go',
      fields: [
        { name: 'a', label: 'A', value: '  x ', placeholder: 'ph', hint: 'a hint' },
        { name: 'chk', label: 'C', type: 'checkbox', value: true },
        { name: 'col', label: 'Col', type: 'color', value: '#112233' },
      ],
    });
    expect($q('.dialog h2').textContent).toBe('T');
    expect($q('.dialog-message').textContent).toBe('msg');
    expect($q('[name=a]').placeholder).toBe('ph');
    expect($q('.dialog small').textContent).toBe('a hint');
    expect(document.activeElement).toBe($q('[name=a]'));
    expect($q('button[type=submit]').textContent).toBe('Go');
    $q('[name=chk]').checked = false;
    $q('form').dispatchEvent(new Event('submit', { cancelable: true }));
    expect(await p).toEqual({ a: 'x', chk: false, col: '#112233' });
    expect($q('.dialog')).toBeNull();
  });

  test('validate keeps the dialog open and shows the error', async () => {
    const p = formDialog({
      title: 'V', fields: [{ name: 'n', label: 'N' }],
      validate: (v) => (v.n === 'ok' ? null : 'bad name'),
    });
    $q('form').dispatchEvent(new Event('submit', { cancelable: true }));
    expect($q('.dialog-error').hidden).toBe(false);
    expect($q('.dialog-error').textContent).toBe('bad name');
    expect($q('.dialog')).not.toBeNull();
    $q('[name=n]').value = 'ok';
    $q('form').dispatchEvent(new Event('submit', { cancelable: true }));
    expect(await p).toEqual({ n: 'ok' });
  });

  test('cancel button, backdrop and Escape all resolve null', async () => {
    let p = formDialog({ title: 'x', fields: [{ name: 'n', label: 'N' }] });
    [...document.querySelectorAll('.dialog-buttons .btn')].find((b) => b.textContent === 'Cancel').click();
    expect(await p).toBeNull();

    p = formDialog({ title: 'x' });
    $q('.dialog .modal-backdrop').click();
    expect(await p).toBeNull();

    p = formDialog({ title: 'x' });
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'a' }));
    expect($q('.dialog')).not.toBeNull();
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }));
    expect(await p).toBeNull();
    expect($q('.dialog')).toBeNull();
  });

  test('extra action buttons resolve {__action}; danger styles OK', async () => {
    const p = formDialog({ title: 'x', danger: true, actions: [{ label: 'Reset', name: 'reset' }] });
    expect($q('button[type=submit]').className).toContain('danger');
    $q('button[data-action=reset]').click();
    expect(await p).toEqual({ __action: 'reset' });
  });

  test('without fields the OK button takes focus and submits {}', async () => {
    const p = formDialog({ title: 'x' });
    expect(document.activeElement).toBe($q('button[type=submit]'));
    $q('form').dispatchEvent(new Event('submit', { cancelable: true }));
    expect(await p).toEqual({});
  });

  test('appends an extra element and returns focus to the opener on close', async () => {
    const opener = document.createElement('button');
    document.body.append(opener);
    opener.focus();
    const extra = document.createElement('p');
    extra.textContent = 'extra';
    const p = formDialog({ title: 'x', extra });
    expect(document.body.contains(extra)).toBe(true);
    $q('form').dispatchEvent(new Event('submit', { cancelable: true }));
    await p;
    expect(document.activeElement).toBe(opener);
  });

  test('does not restore focus to an opener that was removed', async () => {
    const opener = document.createElement('button');
    document.body.append(opener);
    opener.focus();
    const spy = jest.spyOn(opener, 'focus');
    const p = formDialog({ title: 'x' });
    opener.remove();
    $q('form').dispatchEvent(new Event('submit', { cancelable: true }));
    await p;
    expect(spy).not.toHaveBeenCalled();
  });

  test('a select with no options and no value is harmless', async () => {
    const p = formDialog({ title: 's', fields: [{ name: 'dep', label: 'D', type: 'select' }] });
    expect($q('[name=dep]').options).toHaveLength(0);
    $q('form').dispatchEvent(new Event('submit', { cancelable: true }));
    expect(await p).toEqual({ dep: '' });
  });

  test('renders a select field with options, a disabled option and a value', async () => {
    const p = formDialog({
      title: 's',
      fields: [{
        name: 'dep', label: 'Dept', type: 'select', value: 'b',
        options: [{ value: 'a', label: 'A', disabled: true }, { value: 'b', label: 'B' }],
      }],
    });
    const sel = $q('[name=dep]');
    expect(sel.tagName).toBe('SELECT');
    expect([...sel.options].map((o) => o.value)).toEqual(['a', 'b']);
    expect(sel.options[0].disabled).toBe(true);
    expect(sel.value).toBe('b');
    sel.value = 'a';
    $q('form').dispatchEvent(new Event('submit', { cancelable: true }));
    expect(await p).toEqual({ dep: 'a' });
  });

  test('preview text updates live and hides when empty', async () => {
    const p = formDialog({
      title: 'p',
      fields: [{ name: 'n', label: 'N', value: 'hi' }],
      preview: (v) => (v.n ? `Hello ${v.n}` : ''),
    });
    const live = $q('.dialog-preview');
    expect(live.textContent).toBe('Hello hi');
    expect(live.hidden).toBe(false);
    $q('[name=n]').value = '';
    $q('[name=n]').dispatchEvent(new Event('input', { bubbles: true }));
    expect($q('.dialog-preview').hidden).toBe(true);
    $q('[name=n]').value = 'yo';
    $q('[name=n]').dispatchEvent(new Event('change', { bubbles: true }));
    expect($q('.dialog-preview').textContent).toBe('Hello yo');
    $q('form').dispatchEvent(new Event('submit', { cancelable: true }));
    await p;
  });

  test('Tab and Shift+Tab trap focus inside the dialog, wrapping at both ends', async () => {
    const p = formDialog({ title: 'x', fields: [{ name: 'a', label: 'A' }, { name: 'b', label: 'B' }] });
    const stops = [...$q('.dialog').querySelectorAll('input, select, button')].filter((x) => !x.disabled);
    const press = (opts) => {
      const e = new KeyboardEvent('keydown', { key: 'Tab', bubbles: true, cancelable: true, ...opts });
      document.dispatchEvent(e);
      return e;
    };

    stops[0].focus();
    expect(press().defaultPrevented).toBe(true);
    expect(document.activeElement).toBe(stops[1]);

    stops[0].focus();
    expect(press({ shiftKey: true }).defaultPrevented).toBe(true);
    expect(document.activeElement).toBe(stops[stops.length - 1]);

    stops[1].focus();
    press({ shiftKey: true });
    expect(document.activeElement).toBe(stops[0]);

    stops[stops.length - 1].focus();
    press();
    expect(document.activeElement).toBe(stops[0]);

    $q('form').dispatchEvent(new Event('submit', { cancelable: true }));
    await p;
  });
});

describe('infoDialog', () => {
  test('shows one Done button, honoring wide, and resolves on click', async () => {
    const p = infoDialog({ title: 'Info', message: 'body text', wide: true });
    expect($q('.dialog').className).toContain('dialog-wide');
    expect($q('.dialog-message').textContent).toBe('body text');
    const buttons = [...$q('.dialog-buttons').querySelectorAll('button')];
    expect(buttons).toHaveLength(1);
    expect(buttons[0].textContent).toBe('Done');
    buttons[0].click();
    await expect(p).resolves.toBeUndefined();
    expect($q('.dialog')).toBeNull();
  });

  test('infoDialog defaults its message and extra', async () => {
    const p = infoDialog({ title: 'Info' });
    expect($q('.dialog-message')).toBeNull();
    $q('.dialog-buttons button').click();
    await expect(p).resolves.toBeUndefined();
  });
});

describe('confirmDialog', () => {
  test('true on OK, false on Cancel', async () => {
    let p = confirmDialog({ title: 'Sure?', message: 'm', okLabel: 'Yes', danger: true });
    $q('form').dispatchEvent(new Event('submit', { cancelable: true }));
    expect(await p).toBe(true);
    p = confirmDialog({ title: 'Sure?', message: 'm' });
    $q('.dialog .modal-backdrop').click();
    expect(await p).toBe(false);
  });
});

describe('showMenu', () => {
  test('renders items and runs the selected handler once, then closes', () => {
    const a = jest.fn();
    const b = jest.fn();
    showMenu(10, 20, [
      { label: 'A', onSelect: a },
      { separator: true },
      { label: 'B', danger: true, onSelect: b },
    ]);
    const menu = $q('.ctx-menu');
    expect(menu.style.left).toBe('10px');
    expect(menu.style.top).toBe('20px');
    expect(menu.querySelectorAll('[role=menuitem]')).toHaveLength(2);
    expect(menu.querySelector('.ctx-sep')).not.toBeNull();
    expect(menu.querySelector('.danger').textContent).toBe('B');
    expect(document.activeElement.textContent).toBe('A');
    menu.querySelector('.danger').click();
    expect(b).toHaveBeenCalledTimes(1);
    expect(a).not.toHaveBeenCalled();
    expect($q('.ctx-menu')).toBeNull();
  });

  test('outside press and Escape close it; inside press does not', () => {
    showMenu(0, 0, [{ label: 'A', onSelect() {} }]);
    $q('.ctx-menu button').dispatchEvent(new Event('pointerdown', { bubbles: true }));
    expect($q('.ctx-menu')).not.toBeNull();
    document.body.dispatchEvent(new Event('pointerdown', { bubbles: true }));
    expect($q('.ctx-menu')).toBeNull();

    showMenu(0, 0, [{ label: 'A', onSelect() {} }]);
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'x' }));
    expect($q('.ctx-menu')).not.toBeNull();
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }));
    expect($q('.ctx-menu')).toBeNull();
  });

  test('opening a second menu closes the first; returned close() works twice', () => {
    showMenu(0, 0, [{ label: 'one', onSelect() {} }]);
    const close = showMenu(0, 0, [{ label: 'two', onSelect() {} }]);
    expect(document.querySelectorAll('.ctx-menu')).toHaveLength(1);
    expect($q('.ctx-menu').textContent).toBe('two');
    close();
    close();
    expect($q('.ctx-menu')).toBeNull();
  });

  test('an empty menu is harmless', () => {
    showMenu(1, 1, []);
    expect($q('.ctx-menu')).not.toBeNull();
  });

  test('is nudged back on screen when it overflows', () => {
    Object.defineProperty(window, 'innerWidth', { value: 300, configurable: true });
    Object.defineProperty(window, 'innerHeight', { value: 200, configurable: true });
    const spy = jest.spyOn(HTMLElement.prototype, 'getBoundingClientRect').mockReturnValue({
      left: 0, top: 0, right: 500, bottom: 400, width: 120, height: 80,
    });
    try {
      showMenu(280, 190, [{ label: 'A', onSelect() {} }]);
      const m = $q('.ctx-menu');
      expect(m.style.left).toBe('176px');
      expect(m.style.top).toBe('116px');
    } finally {
      spy.mockRestore();
    }
  });
});

afterAll(tick);
