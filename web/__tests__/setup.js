// Test setup and global mocks for Jest (jsdom environment)

// jsdom lacks TextEncoder/TextDecoder; provide Node's implementations
import { TextEncoder, TextDecoder } from 'node:util';

if (typeof globalThis.TextEncoder === 'undefined') {
  globalThis.TextEncoder = TextEncoder;
}
if (typeof globalThis.TextDecoder === 'undefined') {
  globalThis.TextDecoder = TextDecoder;
}

// jsdom lacks WebCrypto.subtle; provide Node's crypto.subtle for
// sha256Hex / presignUpload tests (stage 7).
if (typeof globalThis.crypto === 'undefined' || !globalThis.crypto.subtle) {
  const { webcrypto } = await import('node:crypto');
  Object.defineProperty(globalThis, 'crypto', {
    value: webcrypto,
    configurable: true,
    writable: true,
  });
}

// jsdom does not implement scrollIntoView (Settings nav uses it).
if (typeof Element !== 'undefined' && !Element.prototype.scrollIntoView) {
  Element.prototype.scrollIntoView = () => {};
}

// jsdom has no ResizeObserver (the topbar's search-compact layout uses
// one). A no-op stub is enough: layout tests that care about the
// compact breakpoint drive it through other means (resize events).
if (typeof globalThis.ResizeObserver === 'undefined') {
  globalThis.ResizeObserver = class ResizeObserver {
    observe() {}
    unobserve() {}
    disconnect() {}
  };
}

// localStorage is provided by jsdom, but ensure it's a clean object
beforeEach(() => {
  if (globalThis.localStorage) {
    globalThis.localStorage.clear();
  }
});
