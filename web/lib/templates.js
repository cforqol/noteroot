/**
 * Template variable substitution and application.
 *
 * Built-in variables:
 * - {{title}}       — note title (from filename)
 * - {{date}}        — YYYY-MM-DD (today)
 * - {{time}}        — HH:MM:SS (current time)
 * - {{datetime}}    — YYYY-MM-DDTHH:MM (ISO without seconds)
 * - {{id}}          — ULID (same as frontmatter.mintId)
 * - {{path}}        — vault-relative path
 * - {{folder}}      — parent folder path
 *
 * Custom variables from template.variables are also substituted.
 */

import { mintId, localDate } from './frontmatter.js';

const VAR_PATTERN = /\{\{(\w+)\}\}/g;

/** Apply a template, substituting built-in and custom variables. */
export function applyTemplate(template, context = {}) {
  const { content = '', variables = {} } = template || {};
  if (!content) return '';

  // Built-in variables
  const now = new Date();
  const builtins = {
    title: context.title || '',
    date: localDate(now),
    time: now.toLocaleTimeString([], { hour12: false, hour: '2-digit', minute: '2-digit', second: '2-digit' }),
    datetime: now.toISOString().slice(0, 16).replace('T', 'T'),
    id: mintId(),
    path: context.path || '',
    folder: context.folder || '',
  };

  // Merge custom variables (they can override built-ins)
  const vars = { ...builtins, ...variables };

  return content.replace(VAR_PATTERN, (_, key) => vars[key] ?? '');
}

/** Extract variable names from a template (for UI validation). */
export function extractVariables(template) {
  const { content = '' } = template || {};
  const vars = new Set();
  let match;
  while ((match = VAR_PATTERN.exec(content)) !== null) {
    vars.add(match[1]);
  }
  return [...vars];
}

/** Default template for new notes (used when no template selected). */
export function defaultNoteContent(path) {
  const title = path.split('/').pop()?.replace(/\.md$/i, '') || 'Untitled';
  return `# ${title}\n\n`;
}