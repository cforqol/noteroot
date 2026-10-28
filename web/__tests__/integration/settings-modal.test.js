/**
 * Integration tests for settings modal DOM behavior.
 * Uses the real circuit-breaker and preference functions from lib/vim-mode.js.
 */
import {
  getVimModeState,
  setVimModeState,
  resetVimCircuitBreaker,
  getEditorPreference,
  setEditorPreference,
} from '../../lib/vim-mode.js';

function setupDOM() {
  document.body.innerHTML = `
    <button id="settingsbtn" class="btn icon-btn">⚙</button>
    <div id="settings-modal" class="modal" hidden>
      <div class="modal-backdrop"></div>
      <div class="modal-content">
        <div class="modal-header">
          <h2>Settings</h2>
          <button id="settings-close" class="btn icon-btn">×</button>
        </div>
        <div class="modal-body">
          <section class="settings-section">
            <h3>Editor Preference</h3>
            <div class="radio-group">
              <label>
                <input type="radio" name="editor-mode" value="auto" checked>
                <span>Auto</span>
              </label>
              <label>
                <input type="radio" name="editor-mode" value="vim">
                <span>Always Vim</span>
              </label>
              <label>
                <input type="radio" name="editor-mode" value="textarea">
                <span>Always Plain Text</span>
              </label>
            </div>
          </section>
          <section class="settings-section">
            <h3>Vim Mode Status</h3>
            <div id="vim-status" class="status-display">
              <div class="status-row">
                <span class="status-label">Circuit breaker:</span>
                <span id="cb-status" class="status-value">—</span>
              </div>
              <div class="status-row">
                <span class="status-label">Consecutive failures:</span>
                <span id="cb-failures" class="status-value">—</span>
              </div>
              <div class="status-row">
                <span class="status-label">Last failure:</span>
                <span id="cb-lastfail" class="status-value">—</span>
              </div>
            </div>
            <button id="reset-vim" class="btn">Reset Circuit Breaker</button>
          </section>
        </div>
      </div>
    </div>
  `;
}

// DOM-level helpers mirroring app.js, but using real lib functions.
// app.js wires these to the DOM; here we replicate the wiring for integration.

function openSettingsModal() {
  const modal = document.getElementById('settings-modal');
  modal.hidden = false;
  const pref = getEditorPreference();
  const radio = document.querySelector(`input[name="editor-mode"][value="${pref}"]`);
  if (radio) radio.checked = true;
  updateVimStatus();
}

function closeSettingsModal() {
  document.getElementById('settings-modal').hidden = true;
}

function updateVimStatus() {
  const state = getVimModeState();
  const statusEl = document.getElementById('cb-status');
  statusEl.textContent = state.enabled ? 'Enabled' : 'Disabled';
  statusEl.className = state.enabled ? 'status-value enabled' : 'status-value disabled';
  document.getElementById('cb-failures').textContent = String(state.failures);
  document.getElementById('cb-lastfail').textContent = state.lastFailure
    ? new Date(state.lastFailure).toLocaleString()
    : 'Never';
}

function saveEditorPreference() {
  const selected = document.querySelector('input[name="editor-mode"]:checked');
  if (selected) setEditorPreference(selected.value);
}

function resetCircuitBreaker() {
  resetVimCircuitBreaker();
  updateVimStatus();
}

describe('Settings Modal Integration', () => {
  beforeEach(() => {
    setupDOM();
    localStorage.clear();
  });

  describe('Modal Open/Close', () => {
    test('opens settings modal', () => {
      const modal = document.getElementById('settings-modal');
      expect(modal.hidden).toBe(true);
      openSettingsModal();
      expect(modal.hidden).toBe(false);
    });

    test('closes settings modal', () => {
      openSettingsModal();
      closeSettingsModal();
      expect(document.getElementById('settings-modal').hidden).toBe(true);
    });

    test('settings button opens modal via click', () => {
      document.getElementById('settingsbtn').addEventListener('click', openSettingsModal);
      document.getElementById('settingsbtn').click();
      expect(document.getElementById('settings-modal').hidden).toBe(false);
    });

    test('close button closes modal via click', () => {
      openSettingsModal();
      document.getElementById('settings-close').addEventListener('click', closeSettingsModal);
      document.getElementById('settings-close').click();
      expect(document.getElementById('settings-modal').hidden).toBe(true);
    });

    test('backdrop click closes modal', () => {
      openSettingsModal();
      document.querySelector('.modal-backdrop').addEventListener('click', closeSettingsModal);
      document.querySelector('.modal-backdrop').click();
      expect(document.getElementById('settings-modal').hidden).toBe(true);
    });
  });

  describe('Editor Preference Selection', () => {
    test('loads stored vim preference on open', () => {
      setEditorPreference('vim');
      openSettingsModal();
      const vimRadio = document.querySelector('input[name="editor-mode"][value="vim"]');
      expect(vimRadio.checked).toBe(true);
    });

    test('loads stored textarea preference on open', () => {
      setEditorPreference('textarea');
      openSettingsModal();
      const textareaRadio = document.querySelector('input[name="editor-mode"][value="textarea"]');
      expect(textareaRadio.checked).toBe(true);
    });

    test('defaults to auto when no preference set', () => {
      openSettingsModal();
      const autoRadio = document.querySelector('input[name="editor-mode"][value="auto"]');
      expect(autoRadio.checked).toBe(true);
    });

    test('saves textarea preference on change', () => {
      openSettingsModal();
      const textareaRadio = document.querySelector('input[name="editor-mode"][value="textarea"]');
      textareaRadio.checked = true;
      textareaRadio.dispatchEvent(new Event('change'));
      saveEditorPreference();
      expect(getEditorPreference()).toBe('textarea');
    });

    test('saves vim preference on change', () => {
      openSettingsModal();
      document.querySelectorAll('input[name="editor-mode"]').forEach((radio) => {
        radio.addEventListener('change', saveEditorPreference);
      });
      const vimRadio = document.querySelector('input[name="editor-mode"][value="vim"]');
      vimRadio.checked = true;
      vimRadio.dispatchEvent(new Event('change'));
      expect(getEditorPreference()).toBe('vim');
    });

    test('saves auto preference on change', () => {
      setEditorPreference('vim');
      openSettingsModal();
      document.querySelectorAll('input[name="editor-mode"]').forEach((radio) => {
        radio.addEventListener('change', saveEditorPreference);
      });
      const autoRadio = document.querySelector('input[name="editor-mode"][value="auto"]');
      autoRadio.checked = true;
      autoRadio.dispatchEvent(new Event('change'));
      expect(getEditorPreference()).toBe('auto');
    });
  });

  describe('Circuit Breaker Status Display', () => {
    test('shows enabled status by default', () => {
      openSettingsModal();
      expect(document.getElementById('cb-status').textContent).toBe('Enabled');
      expect(document.getElementById('cb-failures').textContent).toBe('0');
      expect(document.getElementById('cb-lastfail').textContent).toBe('Never');
    });

    test('shows disabled status after 3 failures', () => {
      setVimModeState({ enabled: false, failures: 3, lastFailure: Date.now() });
      openSettingsModal();
      expect(document.getElementById('cb-status').textContent).toBe('Disabled');
      expect(document.getElementById('cb-failures').textContent).toBe('3');
    });

    test('displays last failure time when set', () => {
      setVimModeState({ enabled: false, failures: 3, lastFailure: Date.now() });
      openSettingsModal();
      expect(document.getElementById('cb-lastfail').textContent).not.toBe('Never');
    });

    test('applies enabled CSS class', () => {
      setVimModeState({ enabled: true, failures: 0, lastFailure: 0 });
      openSettingsModal();
      expect(document.getElementById('cb-status').className).toContain('enabled');
    });

    test('applies disabled CSS class', () => {
      setVimModeState({ enabled: false, failures: 3, lastFailure: Date.now() });
      openSettingsModal();
      expect(document.getElementById('cb-status').className).toContain('disabled');
    });
  });

  describe('Circuit Breaker Reset', () => {
    test('reset button clears failures and re-enables', () => {
      setVimModeState({ enabled: false, failures: 5, lastFailure: Date.now() });
      openSettingsModal();
      document.getElementById('reset-vim').addEventListener('click', resetCircuitBreaker);
      document.getElementById('reset-vim').click();

      const state = getVimModeState();
      expect(state.enabled).toBe(true);
      expect(state.failures).toBe(0);
      expect(state.lastFailure).toBe(0);
    });

    test('status display updates after reset', () => {
      setVimModeState({ enabled: false, failures: 3, lastFailure: Date.now() });
      openSettingsModal();
      resetCircuitBreaker();

      expect(document.getElementById('cb-status').textContent).toBe('Enabled');
      expect(document.getElementById('cb-failures').textContent).toBe('0');
      expect(document.getElementById('cb-lastfail').textContent).toBe('Never');
    });
  });

  describe('Escape Key Handling', () => {
    test('closes modal on Escape key', () => {
      openSettingsModal();
      window.addEventListener('keydown', (e) => {
        if (e.key === 'Escape' && !document.getElementById('settings-modal').hidden) {
          closeSettingsModal();
        }
      });
      window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }));
      expect(document.getElementById('settings-modal').hidden).toBe(true);
    });

    test('does not close when already hidden', () => {
      closeSettingsModal();
      window.addEventListener('keydown', (e) => {
        if (e.key === 'Escape' && !document.getElementById('settings-modal').hidden) {
          closeSettingsModal();
        }
      });
      window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }));
      expect(document.getElementById('settings-modal').hidden).toBe(true);
    });
  });
});
