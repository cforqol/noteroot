// UMD wrapper for turndown - exports TurndownService as ES module
import './turndown.umd.js';

// Re-export TurndownService from global scope
export const TurndownService = globalThis.TurndownService;