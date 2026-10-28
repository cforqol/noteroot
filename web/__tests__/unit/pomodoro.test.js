import { jest } from '@jest/globals';
import { pomodoroPlugin } from '../../lib/plugins/pomodoro.js';
import { getUiPluginPrefs } from '../../lib/ui-plugins.js';

let handle = null;

beforeEach(() => {
  document.body.innerHTML = '';
});

afterEach(() => {
  handle?.destroy();
  handle = null;
  document.body.innerHTML = '';
  jest.useRealTimers();
});

function mount() {
  handle = pomodoroPlugin.init('notes');
  return handle;
}

function $(sel) {
  return document.querySelector(sel);
}

function time() {
  return $('.pomo-time').textContent;
}

function toggleBtn() {
  return $('#pomo-toggle');
}

function setInput(phase, value) {
  const input = document.querySelector(`.pomo-minutes[data-phase="${phase}"]`);
  input.value = value;
  input.dispatchEvent(new Event('change', { bubbles: true }));
  return input;
}

function checkState(setting) {
  return document.querySelector(`.pomo-check[data-setting="${setting}"]`).checked;
}

function setCheck(setting, checked) {
  const input = document.querySelector(`.pomo-check[data-setting="${setting}"]`);
  if (input.checked !== checked) input.click();
  return input;
}

/** Start and run one minute of countdown (a full phase). */
function runPhaseToCompletion() {
  $('.pomo-start').click();
  jest.advanceTimersByTime(60_000);
}

describe('layout', () => {
  test('three rows: phase buttons, countdown, start/reset', () => {
    mount();
    const rows = [...$('#pomo-widget').children].map((el) => el.className.split(' ')[0]);
    expect(rows).toEqual([
      'pomo-header', 'pomo-settings', 'pomo-phases', 'pomo-display', 'pomo-controls', 'pomo-count',
    ]);
    expect([...document.querySelectorAll('.pomo-phase-btn')].map((b) => b.textContent))
      .toEqual(['Work', 'Short', 'Long']);
    expect([...document.querySelectorAll('.pomo-controls button')].map((b) => b.textContent))
      .toEqual(['Start', 'Reset']);
    expect($('.pomo-sound')).toBeNull(); // notification icon moved into settings
    expect($('.pomo-time').textContent).toBe('25:00');
  });

  test('duration panel starts hidden behind the gear', () => {
    mount();
    const settings = $('.pomo-settings');
    const gear = $('.pomo-gear');
    expect(settings.hidden).toBe(true);
    expect(gear.getAttribute('aria-expanded')).toBe('false');

    gear.click();
    expect(settings.hidden).toBe(false);
    expect(gear.getAttribute('aria-expanded')).toBe('true');

    gear.click();
    expect(settings.hidden).toBe(true);
    expect(gear.getAttribute('aria-expanded')).toBe('false');
  });

  test('no preferences stored until something is edited', () => {
    mount();
    expect(getUiPluginPrefs('pomodoro')).toEqual({});
  });
});

describe('configuring durations', () => {
  test('editing the current phase re-arms the display and saves', () => {
    mount();
    const input = setInput('work', '50');

    expect(input.value).toBe('50');
    expect(time()).toBe('50:00');
    expect(getUiPluginPrefs('pomodoro')).toEqual({ work: 50, short: 5, long: 15, sound: false, flash: true });
  });

  test('editing another phase leaves the countdown alone but is saved', () => {
    mount();
    setInput('long', '40');
    expect(time()).toBe('25:00');
    expect(document.querySelector('.pomo-minutes[data-phase="long"]').value).toBe('40');
    expect(getUiPluginPrefs('pomodoro').long).toBe(40);
  });

  test('values are clamped to 1..180 minutes', () => {
    mount();
    expect(setInput('work', '0').value).toBe('1');
    expect(time()).toBe('01:00');
    expect(setInput('work', '999').value).toBe('180');
    expect(time()).toBe('180:00');
    expect(setInput('work', '30.7').value).toBe('31');
    expect(time()).toBe('31:00');
    expect(getUiPluginPrefs('pomodoro').work).toBe(31);
  });

  test('unparseable input is rejected and the current value kept', () => {
    mount();
    const input = document.querySelector('.pomo-minutes[data-phase="work"]');
    input.value = 'not-a-number';
    input.dispatchEvent(new Event('change', { bubbles: true }));
    expect(input.value).toBe('25');
    expect(time()).toBe('25:00');
    expect(getUiPluginPrefs('pomodoro')).toEqual({});
  });

  test('a running countdown keeps ticking but is re-scaled', () => {
    jest.useFakeTimers();
    mount();
    $('.pomo-start').click();          // start 25:00
    jest.advanceTimersByTime(3000);    // 24:57
    expect(time()).toBe('24:57');

    setInput('work', '10');            // shorter than what is left
    expect(time()).toBe('10:00');

    jest.advanceTimersByTime(1000);
    expect(time()).toBe('09:59');

    $('.pomo-start').click();          // pause
    expect($('.pomo-start').textContent).toBe('Resume');
  });

  test('editing before the countdown starts adopts the new length', () => {
    jest.useFakeTimers();
    mount();
    setInput('work', '40');
    $('.pomo-start').click();
    jest.advanceTimersByTime(1000);
    expect(time()).toBe('39:59');
  });

  test('a shortened phase completes on the configured duration', () => {
    jest.useFakeTimers();
    mount();
    setInput('work', '1');
    $('.pomo-start').click();
    jest.advanceTimersByTime(60_000);
    expect($('.pomo-phase-btn:nth-child(2)').classList.contains('active')).toBe(true);
    expect(time()).toBe('05:00');
    expect($('.pomo-count').textContent).toBe('🍅 × 1 today');
  });
});

describe('notifications', () => {
  test('sound is off by default, flash is on, both are configurable', () => {
    mount();
    expect(checkState('sound')).toBe(false);
    expect(checkState('flash')).toBe(true);

    setCheck('sound', true);
    expect(getUiPluginPrefs('pomodoro'))
      .toEqual({ work: 25, short: 5, long: 15, sound: true, flash: true });

    setCheck('flash', false);
    expect(getUiPluginPrefs('pomodoro').flash).toBe(false);

    handle.destroy(); handle = null; document.body.innerHTML = '';
    mount();
    expect(checkState('sound')).toBe(true);
    expect(checkState('flash')).toBe(false);
  });

  test('a visible widget flashes when a phase ends', () => {
    jest.useFakeTimers();
    mount();
    setInput('work', '1');
    runPhaseToCompletion();

    expect($('#pomo-widget').classList.contains('pomo-flash')).toBe(true);
    jest.advanceTimersByTime(3000);
    expect($('#pomo-widget').classList.contains('pomo-flash')).toBe(false);
  });

  test('a hidden widget pops a "Time is up" dialog instead', () => {
    jest.useFakeTimers();
    mount();
    setInput('work', '1');
    toggleBtn().click();               // hide the widget
    expect($('#pomo-widget').hidden).toBe(true);

    runPhaseToCompletion();

    expect($('.pomo-dialog-overlay')).not.toBeNull();
    expect($('.pomo-dialog-title').textContent).toContain('Time is up');
    expect($('.pomo-dialog-msg').textContent).toBe('Work finished.');

    $('.pomo-dialog button').click();
    expect($('.pomo-dialog-overlay')).toBeNull();
  });

  test('no dialog when flashing is disabled', () => {
    jest.useFakeTimers();
    mount();
    setCheck('flash', false);
    setInput('work', '1');
    toggleBtn().click();               // hide the widget
    runPhaseToCompletion();

    expect($('.pomo-dialog-overlay')).toBeNull();
    expect(time()).toBe('05:00');      // the cycle still advanced
  });
});

describe('running in the background', () => {
  test('hiding the widget keeps the countdown going and marks the icon', () => {
    jest.useFakeTimers();
    mount();
    expect(toggleBtn().classList.contains('running')).toBe(false);

    $('.pomo-start').click();
    expect(toggleBtn().classList.contains('running')).toBe(true);
    expect(toggleBtn().title).toContain('running');

    toggleBtn().click();               // hide — the timer must not stop
    expect($('#pomo-widget').hidden).toBe(true);
    jest.advanceTimersByTime(3000);
    expect(time()).toBe('24:57');
    expect(toggleBtn().classList.contains('running')).toBe(true);

    toggleBtn().click();               // show again
    expect(time()).toBe('24:57');
    expect($('#pomo-widget').hidden).toBe(false);
  });

  test('the indicator clears when the countdown stops', () => {
    jest.useFakeTimers();
    mount();
    $('.pomo-start').click();
    expect(toggleBtn().classList.contains('running')).toBe(true);

    $('.pomo-start').click();          // pause
    expect(toggleBtn().classList.contains('running')).toBe(false);

    $('.pomo-start').click();          // resume
    expect(toggleBtn().classList.contains('running')).toBe(true);

    runPhaseToCompletion();            // finished
    expect(toggleBtn().classList.contains('running')).toBe(false);
    expect(toggleBtn().title).toBe('Pomodoro timer');
  });

  test('reset stops the countdown and the indicator', () => {
    jest.useFakeTimers();
    mount();
    $('.pomo-start').click();
    $('.pomo-reset').click();
    expect(toggleBtn().classList.contains('running')).toBe(false);
    expect(time()).toBe('25:00');
  });
});

describe('preferences survive a restart', () => {
  test('a remounted widget picks up saved durations', () => {
    mount();
    setInput('work', '50');
    setInput('short', '10');
    handle.destroy();
    handle = null;
    document.body.innerHTML = '';

    mount();
    expect(time()).toBe('50:00');
    expect([...document.querySelectorAll('.pomo-phase-btn')].map((b) => b.textContent))
      .toEqual(['Work', 'Short', 'Long']);
    expect(document.querySelector('.pomo-minutes[data-phase="work"]').value).toBe('50');
    expect(document.querySelector('.pomo-minutes[data-phase="short"]').value).toBe('10');
    expect(document.querySelector('.pomo-minutes[data-phase="long"]').value).toBe('15');
  });

  test('stored values are validated on load', () => {
    localStorage.setItem(
      'noteroot_uipluginprefs',
      JSON.stringify({ pomodoro: { work: 500, short: 'x', long: -3, sound: 'yes', flash: 1 } }),
    );
    mount();
    expect(time()).toBe('180:00');
    expect(document.querySelector('.pomo-minutes[data-phase="short"]').value).toBe('5');
    expect(document.querySelector('.pomo-minutes[data-phase="long"]').value).toBe('1');
    expect(checkState('sound')).toBe(false); // only a real boolean counts
    expect(checkState('flash')).toBe(true);  // fallback to the default
  });
});
