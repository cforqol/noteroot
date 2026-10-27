/**
 * HTML code-block renderer plugin.
 *
 * When enabled, replaces every ```html fenced block in the rendered
 * note with a sandboxed <iframe> that previews the HTML snippet.
 * The iframe uses srcdoc and a restrictive sandbox (allow-scripts only)
 * so styles and JavaScript in the snippet run in an isolated origin.
 *
 * The plugin patches `renderRead` by hooking into a custom event fired
 * after each note render; it must be re-applied on every renderRead
 * call, so we listen for "noteread" events dispatched from app.js.
 */
export const htmlRendererPlugin = {
  id: "html-renderer",
  name: "HTML code-block renderer",
  description: "Renders ```html fenced blocks as sandboxed previews inside the note.",

  init(_slug) {
    const handler = () => applyHtmlRenderer();
    document.addEventListener("noteread", handler);
    // Also apply immediately if a note is already displayed
    applyHtmlRenderer();
    return { destroy: () => document.removeEventListener("noteread", handler) };
  },
};

function applyHtmlRenderer() {
  const note = document.getElementById("note");
  if (!note) return;
  for (const pre of note.querySelectorAll("pre:not([data-html-rendered])")) {
    const code = pre.querySelector("code.language-html");
    if (!code) continue;
    pre.dataset.htmlRendered = "1";
    const src = code.textContent || "";
    const frame = document.createElement("iframe");
    frame.srcdoc = src;
    frame.sandbox = "allow-scripts";
    frame.className = "html-plugin-frame";
    frame.setAttribute("title", "HTML preview");
    frame.setAttribute("loading", "lazy");
    pre.insertAdjacentElement("afterend", frame);
    pre.hidden = true;
    // Toggle between source and rendered view on click of a small button
    const toggle = document.createElement("button");
    toggle.type = "button";
    toggle.className = "html-plugin-toggle btn small ghost";
    toggle.textContent = "Show source";
    toggle.setAttribute("title", "Toggle between rendered and raw HTML");
    toggle.addEventListener("click", () => {
      const showSrc = frame.hidden;
      frame.hidden = !showSrc;
      pre.hidden = showSrc;
      toggle.textContent = showSrc ? "Show source" : "Show rendered";
    });
    frame.insertAdjacentElement("afterend", toggle);
  }
}
