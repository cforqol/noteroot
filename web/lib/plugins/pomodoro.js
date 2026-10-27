/**
 * Pomodoro timer plugin.
 *
 * Floating draggable timer widget.  Supports 25/5/15 minute cycles
 * (work, short break, long break), an optional audio cue (Web Audio
 * beep), and counts completed pomodoros in the current session.
 */
export const pomodoroPlugin = {
  id: "pomodoro",
  name: "Pomodoro timer",
  description: "A floating 25/5/15-minute Pomodoro productivity timer.",

  init(_slug) {
    return mountPomodoro();
  },
};

const PHASES = [
  { label: "Work",        minutes: 25, cls: "phase-work"  },
  { label: "Short break", minutes: 5,  cls: "phase-short" },
  { label: "Long break",  minutes: 15, cls: "phase-long"  },
];

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
  let phaseIdx = 0;
  let totalSecs = PHASES[0].minutes * 60;
  let remaining = totalSecs;
  let running = false;
  let timer = null;
  let completed = 0;
  let audioCtx = null;

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

  const close = document.createElement("button");
  close.type = "button"; close.className = "pomo-close btn ghost"; close.textContent = "×";
  close.setAttribute("aria-label", "Close timer");
  header.append(titleEl, close);

  // Phase tabs
  const phaseTabs = document.createElement("div");
  phaseTabs.className = "pomo-phases";
  const tabBtns = PHASES.map((p, i) => {
    const b = document.createElement("button");
    b.type = "button"; b.className = "pomo-phase-btn btn ghost"; b.textContent = p.label;
    b.addEventListener("click", () => selectPhase(i));
    phaseTabs.append(b);
    return b;
  });

  // Timer display
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

  // Controls
  const controls = document.createElement("div");
  controls.className = "pomo-controls";

  const startBtn = document.createElement("button");
  startBtn.type = "button"; startBtn.className = "pomo-start btn primary";

  const resetBtn = document.createElement("button");
  resetBtn.type = "button"; resetBtn.className = "pomo-reset btn ghost"; resetBtn.textContent = "Reset";

  const soundBtn = document.createElement("button");
  soundBtn.type = "button"; soundBtn.className = "pomo-sound btn ghost"; soundBtn.textContent = "🔔";
  soundBtn.title = "Enable audio cue";
  let soundEnabled = false;
  soundBtn.addEventListener("click", () => {
    soundEnabled = !soundEnabled;
    soundBtn.textContent = soundEnabled ? "🔔" : "🔕";
    soundBtn.title = soundEnabled ? "Audio cue on" : "Audio cue off";
    if (soundEnabled && !audioCtx) {
      try { audioCtx = new (window.AudioContext || window.webkitAudioContext)(); } catch { audioCtx = null; }
    }
  });

  controls.append(startBtn, resetBtn, soundBtn);

  // Completed count
  const countEl = document.createElement("div");
  countEl.className = "pomo-count";

  widget.append(header, phaseTabs, display, controls, countEl);
  document.body.append(widget);

  // Toggle button
  const toggleBtn = document.createElement("button");
  toggleBtn.id = "pomo-toggle";
  toggleBtn.type = "button";
  toggleBtn.className = "btn small ghost";
  toggleBtn.textContent = "🍅";
  toggleBtn.title = "Pomodoro timer";
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
    document.title = running ? `${fmt(remaining)} — ${PHASES[phaseIdx].label}` : document.title;
  }

  function selectPhase(i) {
    clearInterval(timer); timer = null; running = false;
    phaseIdx = i;
    totalSecs = PHASES[i].minutes * 60;
    remaining = totalSecs;
    widget.className = `pomo-widget ${PHASES[i].cls}`;
    tabBtns.forEach((b, j) => b.classList.toggle("active", j === i));
    updateDisplay();
    startBtn.textContent = "Start";
  }

  function tick() {
    remaining--;
    updateDisplay();
    if (remaining <= 0) {
      clearInterval(timer); timer = null; running = false;
      startBtn.textContent = "Start";
      if (phaseIdx === 0) { completed++; countEl.textContent = `🍅 × ${completed} today`; }
      if (soundEnabled) beep(audioCtx);
      // Auto-advance to next phase
      selectPhase((phaseIdx + 1) % PHASES.length);
    }
  }

  startBtn.addEventListener("click", () => {
    if (running) {
      clearInterval(timer); timer = null; running = false;
      startBtn.textContent = "Resume";
    } else {
      if (remaining === 0) { remaining = totalSecs; }
      timer = setInterval(tick, 1000);
      running = true;
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

  selectPhase(0);

  return {
    destroy() {
      clearInterval(timer);
      widget.remove();
      toggleBtn.remove();
    },
  };
}
