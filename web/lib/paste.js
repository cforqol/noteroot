/**
 * HTML to Markdown conversion for paste cleanup.
 * Uses turndown.js to convert clipboard HTML to clean Markdown.
 */

import { TurndownService } from './turndown.js';

const turndown = new TurndownService({
  headingStyle: 'atx',
  codeBlockStyle: 'fenced',
  bulletListMarker: '-',
  // Strip unwanted tags
  remove: ['script', 'style', 'meta', 'link', 'head', 'title', 'noscript'],
});

turndown.addRule('stripClass', {
  filter: ['span', 'div', 'font'],
  replacement: (content) => content, // unwrap
});

/** Convert HTML to clean Markdown. */
export function htmlToMarkdown(html) {
  if (!html) return '';
  try {
    return turndown.turndown(html).trim();
  } catch {
    return '';
  }
}

/** Heuristic: does the HTML look like rich text (Word, Google Docs, web)? */
export function looksLikeRichText(html) {
  if (!html) return false;
  // Check for block tags or common Word/Google Docs markers
  return /<(p|div|h[1-6]|ul|ol|li|table|blockquote|span|font|b|i|u|strong|em)[^>]*>/i.test(html);
}

/** Get plain text from clipboard data, preferring text/plain. */
export function getPlainText(clipboardData) {
  return clipboardData?.getData('text/plain') || '';
}

/** Get HTML from clipboard data. */
export function getHtml(clipboardData) {
  return clipboardData?.getData('text/html') || '';
}