/**
 * Trash retention: the choices the user picks from and the wording that
 * goes with them. Pure functions — the DOM work lives in app.js.
 *
 * Retention is a whole number of days, where 0 means "never purge".
 * The server owns the setting (it is per notebook, not per browser), so
 * these helpers only ever describe a value, never store one.
 */

/** 0 means trashed notes are kept until somebody removes them. */
export const RETENTION_NEVER = 0;

/** The server's own default, used when a notebook says nothing. */
export const DEFAULT_RETENTION_DAYS = 30;

/** Matches the server's MaxTrashRetentionDays. */
export const MAX_RETENTION_DAYS = 3650;

/** The menu offered in Settings, longest-lived last. */
export const RETENTION_CHOICES = [
  { value: 0, label: 'Never — keep until I delete them' },
  { value: 7, label: 'After 7 days' },
  { value: 14, label: 'After 14 days' },
  { value: 30, label: 'After 30 days' },
  { value: 60, label: 'After 60 days' },
  { value: 90, label: 'After 90 days' },
  { value: 180, label: 'After 180 days' },
  { value: 365, label: 'After a year' },
];

const DAY_MS = 24 * 60 * 60 * 1000;

/**
 * Coerce anything the server or a form hands us into a valid retention.
 * Junk, negatives and overlong values fall back to "never" rather than
 * inventing a deletion schedule nobody asked for.
 */
export function normalizeRetention(days) {
  const n = Math.floor(Number(days));
  if (!Number.isFinite(n) || n <= 0) return RETENTION_NEVER;
  return Math.min(n, MAX_RETENTION_DAYS);
}

/** A sentence for the Settings panel. */
export function describeRetention(days) {
  const n = normalizeRetention(days);
  if (n === RETENTION_NEVER) return 'Trashed notes are kept until you delete them — the server never removes them on its own.';
  const sweep = ' The server sweeps them automatically when it starts, on any setting change, and at least daily.';
  if (n === 1) return `Trashed notes are deleted a day after they are trashed.${sweep}`;
  if (n === 365) return `Trashed notes are deleted a year after they are trashed.${sweep}`;
  return `Trashed notes are deleted ${n} days after they are trashed.${sweep}`;
}

/**
 * When a trashed note is due, relative to `now`. Returns '' when the
 * notebook never purges or the entry carries no deadline, so callers can
 * drop the label entirely rather than print a placeholder.
 *
 * @param {number} [expiresAt] unix seconds
 * @param {number} [now] unix milliseconds
 */
export function describeExpiry(expiresAt, now = Date.now()) {
  const at = Number(expiresAt);
  if (!Number.isFinite(at) || at <= 0) return '';
  const ms = at * 1000 - now;
  if (ms <= 0) return 'due for deletion';
  const days = Math.ceil(ms / DAY_MS);
  if (days <= 1) return 'deletes today';
  return `deletes in ${days} days`;
}

/**
 * The retention choices, with the notebook's current value folded in so
 * a server-side setting we do not offer (hand-edited notebook.toml, or a
 * newer build) still shows up selected instead of silently resetting.
 */
export function retentionChoices(current) {
  const n = normalizeRetention(current);
  if (RETENTION_CHOICES.some((c) => c.value === n)) return RETENTION_CHOICES;
  return [...RETENTION_CHOICES, { value: n, label: `After ${n} days` }]
    .sort((a, b) => a.value - b.value);
}
