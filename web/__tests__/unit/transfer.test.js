import { jest } from '@jest/globals';
import {
  isArchive, isMarkdown, exportFileName, summarizeImport, describeImport,
  uniquePath, downloadBlob,
} from '../../lib/transfer.js';

describe('file-type checks', () => {
  test('isArchive accepts .tar.gz and .tgz, case-insensitively', () => {
    expect(isArchive('notes.tar.gz')).toBe(true);
    expect(isArchive('N.TGZ')).toBe(true);
    expect(isArchive('notes.zip')).toBe(false);
    expect(isArchive(null)).toBe(false);
    expect(isArchive(undefined)).toBe(false);
  });

  test('isMarkdown accepts .md and .markdown', () => {
    expect(isMarkdown('a.md')).toBe(true);
    expect(isMarkdown('A.MARKDOWN')).toBe(true);
    expect(isMarkdown('a.txt')).toBe(false);
    expect(isMarkdown(null)).toBe(false);
  });
});

test('exportFileName stamps the local date', () => {
  expect(exportFileName('my nb', new Date(2026, 9, 17))).toBe('my nb-2026-10-17.tar.gz');
  expect(exportFileName('nb')).toMatch(/^nb-\d{4}-\d{2}-\d{2}\.tar\.gz$/);
});

describe('summarizeImport', () => {
  test('counts statuses and totals, ignoring unknown ones', () => {
    const s = summarizeImport([
      { status: 'created' }, { status: 'created' }, { status: 'updated' },
      { status: 'skipped' }, { status: 'error' }, { status: 'weird' },
    ]);
    expect(s).toEqual({ created: 2, updated: 1, skipped: 1, error: 1, total: 6 });
    expect(summarizeImport()).toEqual({ created: 0, updated: 0, skipped: 0, error: 0, total: 0 });
  });
});

describe('describeImport', () => {
  test('lists each non-zero bucket, pluralising notes and '/' for the rest', () => {
    expect(describeImport({ created: 2, updated: 1, skipped: 3, error: 1 }))
      .toBe('2 new notes, 1 overwritten, 3 unchanged, 1 failed');
    expect(describeImport({ created: 1, updated: 1 })).toBe('1 new note, 1 overwritten');
  });

  test('dry runs use the future tense for overwrites and failures', () => {
    expect(describeImport({ created: 1, updated: 2, error: 1 }, { dryRun: true }))
      .toBe('1 new note, 2 will be overwritten, 1 cannot be imported');
  });

  test('an empty summary reads as no notes', () => {
    expect(describeImport({ created: 0, updated: 0, skipped: 0, error: 0 })).toBe('No notes in this archive');
  });
});

describe('uniquePath', () => {
  test('returns the path itself when free', () => {
    expect(uniquePath('dir/a.md', () => false)).toBe('dir/a.md');
  });

  test('appends -2, -3… before the extension, keeping the folder', () => {
    const taken = new Set(['dir/a.md', 'dir/a-2.md']);
    expect(uniquePath('dir/a.md', (p) => taken.has(p))).toBe('dir/a-3.md');
  });

  test('handles a root note and an extension-less base', () => {
    const taken = new Set(['a.md']);
    expect(uniquePath('a.md', (p) => taken.has(p))).toBe('a-2.md');
    expect(uniquePath('notes.txt', (p) => p === 'notes.txt')).toBe('notes.txt-2.md');
  });
});

describe('downloadBlob', () => {
  test('wires a temporary link and revokes the object URL', () => {
    const revoked = [];
    const realCreate = URL.createObjectURL;
    const realRevoke = URL.revokeObjectURL;
    URL.createObjectURL = jest.fn(() => 'blob:fake');
    URL.revokeObjectURL = jest.fn((u) => revoked.push(u));
    const seen = [];
    const click = jest.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(function clickImpl() {
      seen.push({ download: this.download, href: this.href, hidden: this.hidden, inDoc: document.contains(this) });
    });

    jest.useFakeTimers();
    try {
      downloadBlob(new Blob(['x']), 'note.md'); // default global document
      expect(seen).toEqual([{ download: 'note.md', href: 'blob:fake', hidden: true, inDoc: true }]);
      expect(document.querySelector('a[download="note.md"]')).toBeNull(); // removed again
      jest.advanceTimersByTime(1000);
      expect(revoked).toEqual(['blob:fake']);
    } finally {
      jest.useRealTimers();
      URL.createObjectURL = realCreate;
      URL.revokeObjectURL = realRevoke;
      click.mockRestore();
    }
  });
});
