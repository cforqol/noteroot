// Plugin status panels (stage 9). The notebook's plugins publish
// small JSON fragments through ui.set_panel; the frontend renders
// them into the status strip. Panels are degradation-grade: a
// missing endpoint or malformed spec must never break the UI.

export async function fetchPanels(slug, fetchImpl = globalThis.fetch) {
  try {
    const r = await fetchImpl(`/n/${encodeURIComponent(slug)}/plugins/panels`, {
      headers: { Accept: 'application/json' },
    });
    if (!r.ok) return {};
    return await r.json();
  } catch {
    return {};
  }
}

// applyTheme honours a {type: "theme"} panel by setting data-theme
// on the document root; "auto" and unknown names leave the OS
// preference in charge. Returns the applied name, or null.
export function applyTheme(panels, doc = document) {
  const theme = panels && panels.theme;
  if (theme && typeof theme.name === 'string' && theme.name !== '') {
    doc.documentElement.dataset.theme = theme.name;
    return theme.name;
  }
  return null;
}

// renderPanelChips rebuilds the status strip from a panels map:
// every spec with a string text becomes a chip; the theme panel is
// applied to the document instead of shown.
export function renderPanelChips(container, panels, doc = document) {
  container.replaceChildren();
  applyTheme(panels, doc);
  for (const [name, spec] of Object.entries(panels || {})) {
    if (!spec || typeof spec.text !== 'string') continue;
    if (spec.type === 'theme') continue;
    const chip = doc.createElement('span');
    chip.className = spec.level
      ? `panel-chip panel-${spec.level}`
      : 'panel-chip';
    chip.dataset.panel = name;
    chip.textContent = spec.text;
    container.append(chip);
  }
}
