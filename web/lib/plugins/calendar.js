/**
 * Calendar widget plugin.
 *
 * Adds a floating, draggable calendar to the page that shows the
 * current month with previous/next navigation.  The widget persists
 * across note changes while the plugin is enabled; it can be
 * minimised or closed with the × button.
 */
export const calendarPlugin = {
  id: "calendar",
  name: "Calendar widget",
  description: "Shows a floating monthly calendar you can keep open while taking notes.",

  init(_slug) {
    return mountCalendar();
  },
};

function mountCalendar() {
  let year = new Date().getFullYear();
  let month = new Date().getMonth(); // 0-based

  const widget = document.createElement("div");
  widget.id = "cal-widget";
  widget.className = "cal-widget";
  widget.setAttribute("role", "dialog");
  widget.setAttribute("aria-label", "Calendar");

  const header = document.createElement("div");
  header.className = "cal-header";

  const titleEl = document.createElement("span");
  titleEl.className = "cal-title";

  const prev = document.createElement("button");
  prev.type = "button"; prev.className = "cal-nav btn ghost"; prev.textContent = "‹";
  prev.setAttribute("aria-label", "Previous month");

  const next = document.createElement("button");
  next.type = "button"; next.className = "cal-nav btn ghost"; next.textContent = "›";
  next.setAttribute("aria-label", "Next month");

  const today = document.createElement("button");
  today.type = "button"; today.className = "cal-today btn ghost"; today.textContent = "Today";

  const close = document.createElement("button");
  close.type = "button"; close.className = "cal-close btn ghost"; close.textContent = "×";
  close.setAttribute("aria-label", "Close calendar");

  header.append(prev, titleEl, next, today, close);

  const grid = document.createElement("div");
  grid.className = "cal-grid";

  widget.append(header, grid);
  document.body.append(widget);

  // Toggle button in the top-bar
  const toggleBtn = document.createElement("button");
  toggleBtn.id = "cal-toggle";
  toggleBtn.type = "button";
  toggleBtn.className = "btn small ghost";
  toggleBtn.textContent = "📅";
  toggleBtn.title = "Calendar";
  toggleBtn.setAttribute("aria-label", "Toggle calendar");
  const toolbar = document.querySelector("#topbar") || document.querySelector("header") || document.body;
  toolbar.append(toggleBtn);

  function render() {
    const now = new Date();
    titleEl.textContent = new Date(year, month).toLocaleString("default", { month: "long", year: "numeric" });
    grid.innerHTML = "";
    const days = ["Su","Mo","Tu","We","Th","Fr","Sa"];
    for (const d of days) {
      const h = document.createElement("div");
      h.className = "cal-day-head";
      h.textContent = d;
      grid.append(h);
    }
    const first = new Date(year, month, 1).getDay();
    const last = new Date(year, month + 1, 0).getDate();
    for (let i = 0; i < first; i++) {
      const blank = document.createElement("div"); blank.className = "cal-cell empty"; grid.append(blank);
    }
    for (let d = 1; d <= last; d++) {
      const cell = document.createElement("div");
      cell.className = "cal-cell";
      if (d === now.getDate() && month === now.getMonth() && year === now.getFullYear())
        cell.classList.add("today");
      cell.textContent = d;
      grid.append(cell);
    }
  }

  prev.addEventListener("click", () => { month--; if (month < 0) { month = 11; year--; } render(); });
  next.addEventListener("click", () => { month++; if (month > 11) { month = 0; year++; } render(); });
  today.addEventListener("click", () => {
    const n = new Date(); year = n.getFullYear(); month = n.getMonth(); render();
  });
  close.addEventListener("click", () => { widget.hidden = true; });
  toggleBtn.addEventListener("click", () => { widget.hidden = !widget.hidden; });

  // Draggable header
  let dragX = 0, dragY = 0;
  header.style.cursor = "move";
  header.addEventListener("mousedown", (e) => {
    if (e.target !== header && e.target !== titleEl) return;
    e.preventDefault();
    const rect = widget.getBoundingClientRect();
    dragX = e.clientX - rect.left;
    dragY = e.clientY - rect.top;
    const move = (ev) => {
      widget.style.left = `${ev.clientX - dragX}px`;
      widget.style.top = `${ev.clientY - dragY}px`;
      widget.style.right = "auto"; widget.style.bottom = "auto";
    };
    const up = () => { document.removeEventListener("mousemove", move); document.removeEventListener("mouseup", up); };
    document.addEventListener("mousemove", move);
    document.addEventListener("mouseup", up);
  });

  render();

  return {
    destroy() {
      widget.remove();
      toggleBtn.remove();
    },
  };
}
