/**
 * Pomodoro timer plugin.
 *
 * Three-row floating widget: the phase buttons (Work / Short / Long) on
 * top, the countdown in the middle, Start / Reset underneath.  Phase
 * lengths and the completion notifications (sound, widget flash) are
 * edited in the ⚙ panel and saved to the plugin preference store
 * (localStorage, user-level), so they survive a reload.
 *
 * Hiding the widget (top-bar 🍅) never stops a running countdown — the
 * button then carries a running indicator.  If a phase ends while the
 * widget is hidden and flashing is enabled, a small "Time is up"
 * dialog is shown instead of the flash.  Completed pomodoros in the
 * current session are counted.
 */
import { getUiPluginPrefs, setUiPluginPrefs } from "../ui-plugins.js";

export const pomodoroPlugin = {
  id: "pomodoro",
  name: "Pomodoro timer",
  description: "A floating Pomodoro timer; phase lengths and sound/flash notifications are configurable (⚙) and remembered.",

  init(_slug) {
    return mountPomodoro();
  },
};

const PREF_ID = "pomodoro";
const MIN_MINUTES = 1;
const MAX_MINUTES = 180;
const DEFAULT_SOUND = false;
const DEFAULT_FLASH = true;
const FLASH_MS = 3000;

const DEFAULT_PHASES = [
  { key: "work",  label: "Work",        short: "Work",  minutes: 25, cls: "phase-work"  },
  { key: "short", label: "Short break", short: "Short", minutes: 5,  cls: "phase-short" },
  { key: "long",  label: "Long break",  short: "Long",  minutes: 15, cls: "phase-long"  },
];

/** Whole minutes within 1..180. */
function clampMinutes(n) {
  return Math.min(MAX_MINUTES, Math.max(MIN_MINUTES, Math.round(n)));
}

/** Stored value → minutes; anything that is not a finite number falls back. */
function loadMinutes(value, fallback) {
  return typeof value === "number" && Number.isFinite(value) ? clampMinutes(value) : fallback;
}

/** Stored value → boolean; only a real boolean is accepted. */
function loadBool(value, fallback) {
  return typeof value === "boolean" ? value : fallback;
}

function beep(ctx) {
  if (!ctx) return;
  const osc = ctx.createOscillator();
  const gain = ctx.createGain();
  osc.connect(gain); gain.connect(ctx.destination);
  osc.frequency.value = 660;
  gain.gain.setValueAtTime(0.3, ctx.currentTime);
  gain.gain.exponentialRampToValueAtTime(0.001, ctx.currentTime + 0.5);
  osc.start(ctx.currentTime);
  osc.stop(ctx.currentTime + 0.5);
}

function mountPomodoro() {
  const prefs = getUiPluginPrefs(PREF_ID);
  const phases = DEFAULT_PHASES.map((p) => ({ ...p, minutes: loadMinutes(prefs[p.key], p.minutes) }));
  let soundEnabled = loadBool(prefs.sound, DEFAULT_SOUND);
  let flashEnabled = loadBool(prefs.flash, DEFAULT_FLASH);

  let phaseIdx = 0;
  let totalSecs = phases[0].minutes * 60;
  let remaining = totalSecs;
  let started = false;   // has this phase counted down at all?
  let running = false;
  let timer = null;
  let flashTimer = null;
  let completed = 0;
  let audioCtx = null;
  let closeTimeUpDialog = null;

  const widget = document.createElement("div");
  widget.id = "pomo-widget";
  widget.className = "pomo-widget";
  widget.setAttribute("role", "dialog");
  widget.setAttribute("aria-label", "Pomodoro timer");

  const header = document.createElement("div");
  header.className = "pomo-header";

  const titleEl = document.createElement("span");
  titleEl.className = "pomo-title";
  titleEl.textContent = "🍅 Pomodoro";

  const settingsBtn = document.createElement("button");
  settingsBtn.type = "button"; settingsBtn.className = "pomo-gear btn ghost"; settingsBtn.textContent = "⚙";
  settingsBtn.title = "Timer settings";
  settingsBtn.setAttribute("aria-label", "Timer settings");
  settingsBtn.setAttribute("aria-expanded", "false");

  const close = document.createElement("button");
  close.type = "button"; close.className = "pomo-close btn ghost"; close.textContent = "×";
  close.setAttribute("aria-label", "Close timer");
  header.append(titleEl, settingsBtn, close);

  // --- settings: phase lengths + notifications -------------------------
  const settings = document.createElement("div");
  settings.className = "pomo-settings";
  settings.hidden = true;

  const durationsTitle = document.createElement("div");
  durationsTitle.className = "pomo-settings-title";
  durationsTitle.textContent = "Phase lengths";
  settings.append(durationsTitle);

  const inputs = phases.map((p, i) => {
    const row = document.createElement("div");
    row.className = "pomo-setting-row";

    const label = document.createElement("label");
    label.className = "pomo-setting-label";
    label.textContent = p.label;

    const input = document.createElement("input");
    input.type = "number";
    input.className = "pomo-minutes";
    input.min = String(MIN_MINUTES);
    input.max = String(MAX_MINUTES);
    input.step = "1";
    input.dataset.phase = p.key;
    input.value = String(p.minutes);
    input.setAttribute("aria-label", `${p.label} minutes`);

    const unit = document.createElement("span");
    unit.className = "pomo-setting-unit";
    unit.textContent = "min";

    input.addEventListener("change", () => {
      const raw = input.value;
      const n = Number(raw);
      const next = raw !== "" && Number.isFinite(n) ? clampMinutes(n) : phases[i].minutes;
      input.value = String(next);
      if (next !== phases[i].minutes) applyMinutes(i, next);
    });

    row.append(label, input, unit);
    settings.append(row);
    return input;
  });

  const notifTitle = document.createElement("div");
  notifTitle.className = "pomo-settings-title";
  notifTitle.textContent = "Notifications";
  settings.append(notifTitle);

  const soundInput = document.createElement("input");
  soundInput.type = "checkbox";
  soundInput.className = "pomo-check";
  soundInput.dataset.setting = "sound";
  soundInput.checked = soundEnabled;
  const soundLabel = document.createElement("label");
  soundLabel.className = "pomo-check-label";
  soundLabel.append(soundInput, document.createTextNode("Sound"));
  soundInput.addEventListener("change", () => {
    soundEnabled = soundInput.checked;
    if (soundEnabled && !audioCtx) {
      try { audioCtx = new (window.AudioContext || window.webkitAudioContext)(); } catch { audioCtx = null; }
    }
    savePrefs();
  });

  const flashInput = document.createElement("input");
  flashInput.type = "checkbox";
  flashInput.className = "pomo-check";
  flashInput.dataset.setting = "flash";
  flashInput.checked = flashEnabled;
  const flashLabel = document.createElement("label");
  flashLabel.className = "pomo-check-label";
  flashLabel.append(flashInput, document.createTextNode("Flash widget"));
  flashInput.addEventListener("change", () => {
    flashEnabled = flashInput.checked;
    savePrefs();
  });

  settings.append(soundLabel, flashLabel);

  settingsBtn.addEventListener("click", () => {
    settings.hidden = !settings.hidden;
    settingsBtn.setAttribute("aria-expanded", String(!settings.hidden));
  });

  // --- row 1: phase buttons (one row) ----------------------------------
  const phaseTabs = document.createElement("div");
  phaseTabs.className = "pomo-phases";
  const tabBtns = phases.map((p, i) => {
    const b = document.createElement("button");
    b.type = "button"; b.className = "pomo-phase-btn btn ghost";
    b.addEventListener("click", () => selectPhase(i));
    phaseTabs.append(b);
    return b;
  });

  function refreshPhaseLabels() {
    tabBtns.forEach((b, i) => { b.textContent = phases[i].short; });
    inputs.forEach((el, i) => { el.value = String(phases[i].minutes); });
  }

  // --- row 2: countdown -------------------------------------------------
  const display = document.createElement("div");
  display.className = "pomo-display";

  const ring = document.createElement("svg");
  ring.setAttribute("viewBox", "0 0 100 100");
  ring.className = "pomo-ring";
  const bgCircle = document.createElementNS("http://www.w3.org/2000/svg", "circle");
  bgCircle.setAttribute("cx","50"); bgCircle.setAttribute("cy","50"); bgCircle.setAttribute("r","44");
  bgCircle.className.baseVal = "pomo-ring-bg";
  const fgCircle = document.createElementNS("http://www.w3.org/2000/svg", "circle");
  fgCircle.setAttribute("cx","50"); fgCircle.setAttribute("cy","50"); fgCircle.setAttribute("r","44");
  fgCircle.className.baseVal = "pomo-ring-fg";
  const CIRCUM = 2 * Math.PI * 44;
  fgCircle.setAttribute("stroke-dasharray", String(CIRCUM));
  ring.append(bgCircle, fgCircle);

  const timeEl = document.createElement("div");
  timeEl.className = "pomo-time";
  display.append(ring, timeEl);

  // --- row 3: start / reset --------------------------------------------
  const controls = document.createElement("div");
  controls.className = "pomo-controls";

  const startBtn = document.createElement("button");
  startBtn.type = "button"; startBtn.className = "pomo-start btn primary";

  const resetBtn = document.createElement("button");
  resetBtn.type = "button"; resetBtn.className = "pomo-reset btn ghost"; resetBtn.textContent = "Reset";

  controls.append(startBtn, resetBtn);

  // Completed count
  const countEl = document.createElement("div");
  countEl.className = "pomo-count";

  widget.append(header, settings, phaseTabs, display, controls, countEl);
  document.body.append(widget);

  // Top-bar toggle: the running marker stays visible while the widget is hidden
  const toggleBtn = document.createElement("button");
  toggleBtn.id = "pomo-toggle";
  toggleBtn.type = "button";
  toggleBtn.className = "btn small ghost";
  toggleBtn.textContent = "🍅";
  toggleBtn.title = "Pomodoro timer";
  toggleBtn.setAttribute("aria-label", "Pomodoro timer");
  const toolbar = document.querySelector("#topbar") || document.querySelector("header") || document.body;
  toolbar.append(toggleBtn);

  function fmt(secs) {
    const m = Math.floor(secs / 60).toString().padStart(2, "0");
    const s = (secs % 60).toString().padStart(2, "0");
    return `${m}:${s}`;
  }

  function updateDisplay() {
    timeEl.textContent = fmt(remaining);
    const frac = remaining / totalSecs;
    fgCircle.setAttribute("stroke-dashoffset", String(CIRCUM * (1 - frac)));
    document.title = running ? `${fmt(remaining)} — ${phases[phaseIdx].label}` : document.title;
  }

  function savePrefs() {
    setUiPluginPrefs(PREF_ID, {
      ...Object.fromEntries(phases.map((p) => [p.key, p.minutes])),
      sound: soundEnabled,
      flash: flashEnabled,
    });
  }

  /** Running state, mirrored on the top-bar button as an indicator. */
  function setRunning(next) {
    running = next;
    toggleBtn.classList.toggle("running", next);
    toggleBtn.title = next ? "Pomodoro timer (running)" : "Pomodoro timer";
  }

  /** Apply a new phase length: re-arm or re-scale the live countdown. */
  function applyMinutes(i, minutes) {
    phases[i].minutes = minutes;
    if (i === phaseIdx) {
      totalSecs = minutes * 60;
      remaining = started ? Math.min(remaining, totalSecs) : totalSecs;
      updateDisplay();
    }
    refreshPhaseLabels();
    savePrefs();
  }

  function selectPhase(i) {
    clearInterval(timer); timer = null; setRunning(false);
    phaseIdx = i;
    totalSecs = phases[i].minutes * 60;
    remaining = totalSecs;
    started = false;
    phases.forEach((p) => widget.classList.remove(p.cls));
    widget.classList.add(phases[i].cls);
    tabBtns.forEach((b, j) => b.classList.toggle("active", j === i));
    updateDisplay();
    startBtn.textContent = "Start";
  }

  /**
   * End-of-phase notification.  Sound is independent of visibility;
   * the flash needs the widget on screen — otherwise (flash enabled,
   * widget hidden) a small dialog says the time is up.
   */
  function notifyPhaseDone(finishedLabel) {
    if (soundEnabled) beep(audioCtx);
    if (!flashEnabled) return;
    if (widget.hidden) {
      showTimeUpDialog(finishedLabel);
      return;
    }
    widget.classList.remove("pomo-flash");
    void widget.offsetWidth; // restart the animation
    widget.classList.add("pomo-flash");
    clearTimeout(flashTimer);
    flashTimer = setTimeout(() => {
      widget.classList.remove("pomo-flash");
      flashTimer = null;
    }, FLASH_MS);
  }

  function showTimeUpDialog(finishedLabel) {
    closeTimeUpDialog?.();

    const overlay = document.createElement("div");
    overlay.className = "pomo-dialog-overlay";

    const box = document.createElement("div");
    box.className = "pomo-dialog";
    box.setAttribute("role", "alertdialog");
    box.setAttribute("aria-modal", "true");
    box.setAttribute("aria-label", "Time is up");

    const heading = document.createElement("div");
    heading.className = "pomo-dialog-title";
    heading.textContent = "⏰ Time is up";

    const message = document.createElement("p");
    message.className = "pomo-dialog-msg";
    message.textContent = `${finishedLabel} finished.`;

    const ok = document.createElement("button");
    ok.type = "button"; ok.className = "btn primary"; ok.textContent = "OK";
    ok.setAttribute("aria-label", "Dismiss");

    const onKey = (e) => { if (e.key === "Escape") dismiss(); };
    function dismiss() {
      overlay.remove();
      document.removeEventListener("keydown", onKey);
      if (closeTimeUpDialog === dismiss) closeTimeUpDialog = null;
    }
    ok.addEventListener("click", dismiss);
    overlay.addEventListener("click", (e) => { if (e.target === overlay) dismiss(); });
    document.addEventListener("keydown", onKey);

    box.append(heading, message, ok);
    overlay.append(box);
    document.body.append(overlay);
    closeTimeUpDialog = dismiss;
    ok.focus();
  }

  function tick() {
    remaining--;
    updateDisplay();
    if (remaining <= 0) {
      clearInterval(timer); timer = null; setRunning(false);
      startBtn.textContent = "Start";
      if (phaseIdx === 0) { completed++; countEl.textContent = `🍅 × ${completed} today`; }
      const finishedLabel = phases[phaseIdx].label;
      // Auto-advance first: selectPhase rewrites widget.className and
      // would drop the flash class.
      selectPhase((phaseIdx + 1) % phases.length);
      notifyPhaseDone(finishedLabel);
    }
  }

  startBtn.addEventListener("click", () => {
    if (running) {
      clearInterval(timer); timer = null; setRunning(false);
      startBtn.textContent = "Resume";
    } else {
      if (remaining === 0) { remaining = totalSecs; }
      timer = setInterval(tick, 1000);
      setRunning(true);
      started = true;
      startBtn.textContent = "Pause";
    }
    updateDisplay();
  });

  resetBtn.addEventListener("click", () => selectPhase(phaseIdx));
  close.addEventListener("click", () => { widget.hidden = true; });
  toggleBtn.addEventListener("click", () => { widget.hidden = !widget.hidden; });

  // Draggable header
  header.style.cursor = "move";
  header.addEventListener("mousedown", (e) => {
    if (e.target !== header && e.target !== titleEl) return;
    e.preventDefault();
    const rect = widget.getBoundingClientRect();
    const dx = e.clientX - rect.left;
    const dy = e.clientY - rect.top;
    const move = (ev) => {
      widget.style.left = `${ev.clientX - dx}px`;
      widget.style.top = `${ev.clientY - dy}px`;
      widget.style.right = "auto"; widget.style.bottom = "auto";
    };
    const up = () => { document.removeEventListener("mousemove", move); document.removeEventListener("mouseup", up); };
    document.addEventListener("mousemove", move);
    document.addEventListener("mouseup", up);
  });

  refreshPhaseLabels();
  selectPhase(0);

  return {
    destroy() {
      clearInterval(timer);
      clearTimeout(flashTimer);
      closeTimeUpDialog?.();
      widget.remove();
      toggleBtn.remove();
    },
  };
}
