// Test setup and global mocks for Jest (jsdom environment)

// jsdom lacks TextEncoder/TextDecoder; provide Node's implementations
import { TextEncoder, TextDecoder } from 'node:util';

if (typeof globalThis.TextEncoder === 'undefined') {
  globalThis.TextEncoder = TextEncoder;
}
if (typeof globalThis.TextDecoder === 'undefined') {
  globalThis.TextDecoder = TextDecoder;
}

// localStorage is provided by jsdom, but ensure it's a clean object
beforeEach(() => {
  if (globalThis.localStorage) {
    globalThis.localStorage.clear();
  }
});
