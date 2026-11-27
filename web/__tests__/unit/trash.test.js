/**
 * Trash retention helpers: coercion, wording and the Settings menu.
 */
import {
  RETENTION_NEVER, DEFAULT_RETENTION_DAYS, MAX_RETENTION_DAYS,
  RETENTION_CHOICES, normalizeRetention, describeRetention, describeExpiry, retentionChoices,
  daysLeft, compareTrashTitles, sortTrashEntries,
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
    expect(describeRetention(30)).toMatch(/deleted 30 days after/);
    expect(describeRetention(30)).toMatch(/sweeps them automatically/);
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

describe('daysLeft', () => {
  const now = Date.UTC(2026, 5, 1, 12, 0, 0);
  const inDays = (d) => (now + d * DAY) / 1000;

  test('rounds up, same as describeExpiry', () => {
    expect(daysLeft(inDays(12), now)).toBe(12);
    expect(daysLeft(inDays(1.4), now)).toBe(2);
    expect(daysLeft(inDays(2), now)).toBe(2);
  });

  test('today rounds up to 1, like describeExpiry; overdue reads as 0', () => {
    expect(daysLeft(inDays(0.5), now)).toBe(1);
    expect(daysLeft(inDays(-1), now)).toBe(0);
    expect(daysLeft(now / 1000, now)).toBe(0);
  });

  test('no deadline means null, not a placeholder number', () => {
    for (const none of [undefined, null, 0, '', NaN, 'later', -5]) {
      expect(daysLeft(none, now)).toBeNull();
    }
  });

  test('defaults to the current clock', () => {
    const soon = (Date.now() + 5 * DAY) / 1000;
    expect(daysLeft(soon)).toBe(5);
  });
});

describe('compareTrashTitles', () => {
  test('sorts numbers by value, not lexicographically', () => {
    const titles = ['100', '99', '50', '13', '3', '2', '1'];
    expect([...titles].sort(compareTrashTitles)).toEqual(['1', '2', '3', '13', '50', '99', '100']);
  });

  test('is case-insensitive, like the tags/folder sorts elsewhere', () => {
    expect(compareTrashTitles('b', 'A')).toBeGreaterThan(0);
    expect(compareTrashTitles('a', 'A')).toBe(0);
  });

  test('coerces non-string input rather than throwing', () => {
    expect(() => compareTrashTitles(1, 2)).not.toThrow();
  });
});

describe('sortTrashEntries', () => {
  const now = Date.UTC(2026, 5, 1, 12, 0, 0);
  const inDays = (d) => (now + d * DAY) / 1000;
  const entries = [
    { name: 'a', origPath: 'note13.md', expiresAt: inDays(13) },
    { name: 'b', origPath: 'note2.md', expiresAt: inDays(2) },
    { name: 'c', origPath: 'note100.md' }, // never purges: no deadline
  ];

  test('default: title, ascending, numeric-aware', () => {
    expect(sortTrashEntries(entries).map((e) => e.name)).toEqual(['b', 'a', 'c']);
  });

  test('title, descending', () => {
    expect(sortTrashEntries(entries, { by: 'title', dir: 'desc' }).map((e) => e.name))
      .toEqual(['c', 'a', 'b']);
  });

  test('days left, ascending: soonest first, no-deadline entries always last', () => {
    expect(sortTrashEntries(entries, { by: 'expires', dir: 'asc' }, now).map((e) => e.name))
      .toEqual(['b', 'a', 'c']);
  });

  test('days left, descending: still no-deadline entries last', () => {
    expect(sortTrashEntries(entries, { by: 'expires', dir: 'desc' }, now).map((e) => e.name))
      .toEqual(['a', 'b', 'c']);
  });

  test('does not mutate the input array', () => {
    const copy = [...entries];
    sortTrashEntries(entries, { by: 'title' });
    expect(entries).toEqual(copy);
  });

  test('all entries without a deadline keep their relative order', () => {
    const none = [{ name: 'x', origPath: 'b.md' }, { name: 'y', origPath: 'a.md' }];
    expect(sortTrashEntries(none, { by: 'expires' }).map((e) => e.name)).toEqual(['x', 'y']);
  });

  test('one entry with a deadline, one without, in either starting order', () => {
    const withDeadline = { name: 'has', origPath: 'has.md', expiresAt: inDays(5) };
    const noDeadline = { name: 'none', origPath: 'none.md' };
    expect(sortTrashEntries([withDeadline, noDeadline], { by: 'expires' }, now).map((e) => e.name))
      .toEqual(['has', 'none']);
    expect(sortTrashEntries([noDeadline, withDeadline], { by: 'expires' }, now).map((e) => e.name))
      .toEqual(['has', 'none']);
  });

  test('falls back to name when origPath is absent', () => {
    const list = [{ name: '13.md' }, { name: '2.md' }];
    expect(sortTrashEntries(list, { by: 'title' }).map((e) => e.name)).toEqual(['2.md', '13.md']);
  });
});
