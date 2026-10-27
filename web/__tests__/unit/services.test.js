import {
  fetchIntegrationStatus,
  resetIntegrationStatus,
  karakeepArchive,
  slinkUpload,
  isHttpUrl,
} from '../../lib/services.js';

beforeEach(() => resetIntegrationStatus());

// --- isHttpUrl ---

describe('isHttpUrl', () => {
  test('accepts http URL', () => expect(isHttpUrl('http://example.com')).toBe(true));
  test('accepts https URL', () => expect(isHttpUrl('https://example.com/path?q=1')).toBe(true));
  test('rejects ftp', () => expect(isHttpUrl('ftp://example.com')).toBe(false));
  test('rejects plain text', () => expect(isHttpUrl('hello world')).toBe(false));
  test('rejects URL with whitespace', () => expect(isHttpUrl('https://a.com b')).toBe(false));
  test('rejects empty', () => expect(isHttpUrl('')).toBe(false));
  test('rejects null', () => expect(isHttpUrl(null)).toBe(false));
});

// --- fetchIntegrationStatus ---

describe('fetchIntegrationStatus', () => {
  test('returns parsed status on success', async () => {
    const fetchFn = jest.fn().mockResolvedValue({
      ok: true,
      json: () => Promise.resolve({ karakeep: true, slink: false }),
    });
    const st = await fetchIntegrationStatus({ fetchFn });
    expect(st).toEqual({ karakeep: true, slink: false });
    expect(fetchFn).toHaveBeenCalledTimes(1);
  });

  test('caches the result', async () => {
    const fetchFn = jest.fn().mockResolvedValue({
      ok: true,
      json: () => Promise.resolve({ karakeep: false, slink: true }),
    });
    await fetchIntegrationStatus({ fetchFn });
    await fetchIntegrationStatus({ fetchFn });
    expect(fetchFn).toHaveBeenCalledTimes(1);
  });

  test('returns false defaults on HTTP error', async () => {
    const fetchFn = jest.fn().mockResolvedValue({ ok: false });
    const st = await fetchIntegrationStatus({ fetchFn });
    expect(st).toEqual({ karakeep: false, slink: false });
  });
});

// --- karakeepArchive ---

describe('karakeepArchive', () => {
  test('returns bookmarkUrl on success', async () => {
    const fetchFn = jest.fn().mockResolvedValue({
      ok: true,
      json: () => Promise.resolve({ bookmarkUrl: 'https://kk.example.com/bookmarks/abc' }),
    });
    const url = await karakeepArchive('https://example.com', { fetchFn });
    expect(url).toBe('https://kk.example.com/bookmarks/abc');
    const [endpoint, opts] = fetchFn.mock.calls[0];
    expect(endpoint).toBe('/api/v1/integrations/karakeep/archive');
    expect(JSON.parse(opts.body)).toEqual({ url: 'https://example.com' });
  });

  test('throws on HTTP error', async () => {
    const fetchFn = jest.fn().mockResolvedValue({
      ok: false,
      status: 503,
      json: () => Promise.resolve({ error: 'not configured' }),
    });
    await expect(karakeepArchive('https://example.com', { fetchFn })).rejects.toThrow('not configured');
  });
});

// --- slinkUpload ---

describe('slinkUpload', () => {
  test('returns hosted url on success', async () => {
    const fetchFn = jest.fn().mockResolvedValue({
      ok: true,
      json: () => Promise.resolve({ url: 'https://slink.example.com/f/image.png' }),
    });
    const file = new File(['data'], 'image.png', { type: 'image/png' });
    const url = await slinkUpload(file, { fetchFn });
    expect(url).toBe('https://slink.example.com/f/image.png');
    const [endpoint, opts] = fetchFn.mock.calls[0];
    expect(endpoint).toBe('/api/v1/integrations/slink/upload');
    expect(opts.method).toBe('POST');
    expect(opts.body).toBeInstanceOf(FormData);
  });

  test('throws on HTTP error', async () => {
    const fetchFn = jest.fn().mockResolvedValue({
      ok: false,
      status: 502,
      json: () => Promise.resolve({}),
    });
    const file = new File(['data'], 'img.jpg', { type: 'image/jpeg' });
    await expect(slinkUpload(file, { fetchFn })).rejects.toThrow('slink upload failed (502)');
  });
});
