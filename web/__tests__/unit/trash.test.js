/**
 * Trash retention helpers: coercion, wording and the Settings menu.
 */
import {
  RETENTION_NEVER, DEFAULT_RETENTION_DAYS, MAX_RETENTION_DAYS,
  RETENTION_CHOICES, normalizeRetention, describeRetention, describeExpiry, retentionChoices,
} from '../../lib/trash.js';

const DAY = 24 * 60 * 60 * 1000;

describe('normalizeRetention', () => {
  test('keeps a sane day count', () => {
    expect(normalizeRetention(7)).toBe(7);
    expect(normalizeRetention('30')).toBe(30);
    expect(normalizeRetention(1)).toBe(1);
    expect(normalizeRetention(45.9)).toBe(45);
  });

  test('anything unusable means never, never invents a deadline', () => {
    for (const bad of [0, -1, '', null, undefined, NaN, 'soon', {}, Infinity]) {
      expect(normalizeRetention(bad)).toBe(RETENTION_NEVER);
    }
  });

  test('caps absurd values at the server maximum', () => {
    expect(normalizeRetention(MAX_RETENTION_DAYS)).toBe(MAX_RETENTION_DAYS);
    expect(normalizeRetention(MAX_RETENTION_DAYS + 1)).toBe(MAX_RETENTION_DAYS);
    expect(normalizeRetention(1e9)).toBe(MAX_RETENTION_DAYS);
  });
});

describe('describeRetention', () => {
  test('spells out the schedule', () => {
    expect(describeRetention(RETENTION_NEVER)).toMatch(/until you delete them/);
    expect(describeRetention(1)).toMatch(/a day after/);
    expect(describeRetention(30)).toBe('Trashed notes are deleted 30 days after they are trashed.');
    expect(describeRetention(365)).toMatch(/a year after/);
    expect(describeRetention(DEFAULT_RETENTION_DAYS)).toMatch(/30 days/);
  });

  test('junk reads as never, not as a broken sentence', () => {
    expect(describeRetention('nope')).toMatch(/until you delete them/);
    expect(describeRetention(-3)).toMatch(/until you delete them/);
  });
});

describe('describeExpiry', () => {
  const now = Date.UTC(2026, 5, 1, 12, 0, 0);
  const inDays = (d) => (now + d * DAY) / 1000;

  test('counts the days left, rounding up', () => {
    expect(describeExpiry(inDays(12), now)).toBe('deletes in 12 days');
    expect(describeExpiry(inDays(1.4), now)).toBe('deletes in 2 days');
    expect(describeExpiry(inDays(2), now)).toBe('deletes in 2 days');
  });

  test('today and overdue read plainly', () => {
    expect(describeExpiry(inDays(0.5), now)).toBe('deletes today');
    expect(describeExpiry(inDays(1), now)).toBe('deletes today');
    expect(describeExpiry(inDays(-1), now)).toBe('due for deletion');
    expect(describeExpiry(now / 1000, now)).toBe('due for deletion');
  });

  test('no deadline means no label at all', () => {
    for (const none of [undefined, null, 0, '', NaN, 'later', -5]) {
      expect(describeExpiry(none, now)).toBe('');
    }
  });

  test('defaults to the current clock', () => {
    const soon = (Date.now() + 5 * DAY) / 1000;
    expect(describeExpiry(soon)).toBe('deletes in 5 days');
  });
});

describe('retentionChoices', () => {
  test('offers the standard menu for a standard value', () => {
    expect(retentionChoices(30)).toBe(RETENTION_CHOICES);
    expect(retentionChoices(RETENTION_NEVER)).toBe(RETENTION_CHOICES);
    expect(retentionChoices('bogus')).toBe(RETENTION_CHOICES);
  });

  test('folds in a value the server set that we do not offer', () => {
    const got = retentionChoices(45);
    expect(got).not.toBe(RETENTION_CHOICES);
    expect(got.map((c) => c.value)).toEqual([0, 7, 14, 30, 45, 60, 90, 180, 365]);
    expect(got.find((c) => c.value === 45).label).toBe('After 45 days');
    // The standard menu is left untouched for the next caller.
    expect(RETENTION_CHOICES.map((c) => c.value)).toEqual([0, 7, 14, 30, 60, 90, 180, 365]);
  });

  test('every choice has a usable label', () => {
    for (const c of RETENTION_CHOICES) {
      expect(typeof c.label).toBe('string');
      expect(c.label.length).toBeGreaterThan(0);
      expect(Number.isInteger(c.value)).toBe(true);
    }
  });
});
