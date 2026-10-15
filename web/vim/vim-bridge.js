/**
 * Vim Bridge - postMessage protocol between parent window and vim iframe
 * 
 * This module runs inside the sandboxed iframe and manages:
 * - xterm.js terminal rendering
 * - WASI-vim WebAssembly runtime (stub for Phase 1, real vim in Phase 3)
 * - Communication with parent window via postMessage
 * - Buffer synchronization and save requests
 */

// Using CDN for Phase 1 MVP (bundling in Phase 3)
import { Terminal } from 'https://cdn.jsdelivr.net/npm/@xterm/xterm@5.5.0/+esm';
import { FitAddon } from 'https://cdn.jsdelivr.net/npm/@xterm/addon-fit@0.10.0/+esm';

// Terminal instance
let term = null;
let fitAddon = null;

// Editor state
let currentBuffer = '';
let currentPath = '';
let currentBaseSha = null;
let isDirty = false;
let vimMode = 'NORMAL'; // NORMAL, INSERT, VISUAL, COMMAND

// Mock Vim state (Phase 1 stub; replaced with real WASM in Phase 3)
let cursorLine = 0;
let cursorCol = 0;
let lines = [];

// Heartbeat interval
let heartbeatTimer = null;

// Status element
const statusEl = document.getElementById('status');

/**
 * Initialize the terminal and vim environment
 */
async function init() {
  try {
    statusEl.textContent = 'Initializing terminal...';
    
    // Create terminal
    term = new Terminal({
      cursorBlink: true,
      cursorStyle: 'block',
      fontSize: 14,
      fontFamily: '"JetBrains Mono", Menlo, Monaco, monospace',
      theme: {
        background: '#1e1e1e',
        foreground: '#d4d4d4',
        cursor: '#ffffff',
        cursorAccent: '#1e1e1e',
        selection: 'rgba(255, 255, 255, 0.3)',
        black: '#000000',
        red: '#cd3131',
        green: '#0dbc79',
        yellow: '#e5e510',
        blue: '#2472c8',
        magenta: '#bc3fbc',
        cyan: '#11a8cd',
        white: '#e5e5e5',
        brightBlack: '#666666',
        brightRed: '#f14c4c',
        brightGreen: '#23d18b',
        brightYellow: '#f5f543',
        brightBlue: '#3b8eea',
        brightMagenta: '#d670d6',
        brightCyan: '#29b8db',
        brightWhite: '#ffffff'
      },
      rows: 40,
      cols: 120,
      scrollback: 1000
    });

    // Fit addon for auto-resize
    fitAddon = new FitAddon();
    term.loadAddon(fitAddon);

    // Mount terminal to DOM
    term.open(document.getElementById('terminal'));
    fitAddon.fit();

    // Handle window resize
    window.addEventListener('resize', () => {
      fitAddon.fit();
    });

    // Keyboard input handling
    term.onData(handleTerminalInput);

    statusEl.textContent = 'Loading Vim...';
    
    // Initialize mock Vim (Phase 1 stub)
    await initMockVim();
    
    statusEl.textContent = 'Ready';
    
    // Notify parent that we're ready
    sendToParent({ type: 'vim:ready' });
    
    // Start heartbeat
    startHeartbeat();
    
  } catch (error) {
    showError('Initialization failed: ' + error.message);
    sendToParent({ type: 'vim:error', message: error.message, fatal: true });
  }
}

/**
 * Initialize mock Vim (Phase 1 stub - replaced with WASI-vim in Phase 3)
 */
async function initMockVim() {
  // Stub: simulate vim startup
  term.writeln('\x1b[32m~\x1b[0m');
  term.writeln('\x1b[32m~\x1b[0m                    \x1b[1mVIM - Vi IMproved\x1b[0m');
  term.writeln('\x1b[32m~\x1b[0m');
  term.writeln('\x1b[32m~\x1b[0m                   version 9.0 (mock)');
  term.writeln('\x1b[32m~\x1b[0m              by Bram Moolenaar et al.');
  term.writeln('\x1b[32m~\x1b[0m         Vim is open source and freely distributable');
  term.writeln('\x1b[32m~\x1b[0m');
  term.writeln('\x1b[32m~\x1b[0m         type  :q<Enter>               to exit');
  term.writeln('\x1b[32m~\x1b[0m         type  :help<Enter>             for help');
  term.writeln('\x1b[32m~\x1b[0m');
  for (let i = 0; i < 20; i++) {
    term.writeln('\x1b[32m~\x1b[0m');
  }
  term.write('\x1b[30;1H'); // Move cursor to bottom
  updateStatusLine();
}

/**
 * Handle terminal keyboard input
 */
function handleTerminalInput(data) {
  // Phase 1 stub: Basic vim command parsing
  
  if (vimMode === 'NORMAL') {
    handleNormalMode(data);
  } else if (vimMode === 'INSERT') {
    handleInsertMode(data);
  } else if (vimMode === 'COMMAND') {
    handleCommandMode(data);
  }
}

let commandBuffer = '';

function handleNormalMode(data) {
  switch (data) {
    case 'i':
      vimMode = 'INSERT';
      updateStatusLine();
      term.write('-- INSERT --');
      break;
    case ':':
      vimMode = 'COMMAND';
      commandBuffer = '';
      term.write('\r\n:');
      break;
    case 'h':
      // Move left (stub)
      break;
    case 'j':
      // Move down (stub)
      break;
    case 'k':
      // Move up (stub)
      break;
    case 'l':
      // Move right (stub)
      break;
    default:
      // Ignore other keys in normal mode stub
      break;
  }
}

function handleInsertMode(data) {
  if (data === '\x1b') { // ESC
    vimMode = 'NORMAL';
    updateStatusLine();
    term.write('\r\x1b[K'); // Clear status line
    return;
  }
  
  // In insert mode, add to buffer
  if (data === '\r') {
    term.write('\r\n');
    lines.splice(cursorLine + 1, 0, '');
    cursorLine++;
    cursorCol = 0;
  } else if (data === '\x7f') { // Backspace
    if (cursorCol > 0) {
      term.write('\b \b');
      const line = lines[cursorLine] || '';
      lines[cursorLine] = line.slice(0, cursorCol - 1) + line.slice(cursorCol);
      cursorCol--;
    }
  } else {
    term.write(data);
    const line = lines[cursorLine] || '';
    lines[cursorLine] = line.slice(0, cursorCol) + data + line.slice(cursorCol);
    cursorCol += data.length;
  }
  
  markDirty();
}

function handleCommandMode(data) {
  if (data === '\r') { // Enter
    executeCommand(commandBuffer);
    commandBuffer = '';
    vimMode = 'NORMAL';
    updateStatusLine();
  } else if (data === '\x1b') { // ESC
    commandBuffer = '';
    vimMode = 'NORMAL';
    term.write('\r\x1b[K');
    updateStatusLine();
  } else if (data === '\x7f') { // Backspace
    if (commandBuffer.length > 0) {
      commandBuffer = commandBuffer.slice(0, -1);
      term.write('\b \b');
    }
  } else {
    commandBuffer += data;
    term.write(data);
  }
}

function executeCommand(cmd) {
  term.write('\r\n');
  
  if (cmd === 'w' || cmd === 'write') {
    // Save command
    saveBuffer();
  } else if (cmd === 'q' || cmd === 'quit') {
    // Quit command
    if (isDirty) {
      term.writeln('\x1b[31mNo write since last change (add ! to override)\x1b[0m');
    } else {
      sendToParent({ type: 'vim:close' });
    }
  } else if (cmd === 'q!' || cmd === 'quit!') {
    // Force quit
    sendToParent({ type: 'vim:close', force: true });
  } else if (cmd === 'wq' || cmd === 'x') {
    // Save and quit
    saveBuffer();
    setTimeout(() => {
      sendToParent({ type: 'vim:close' });
    }, 100);
  } else {
    term.writeln(`\x1b[31mUnknown command: ${cmd}\x1b[0m`);
  }
  
  updateStatusLine();
}

function saveBuffer() {
  const content = lines.join('\n');
  sendToParent({ type: 'vim:save', content });
  isDirty = false;
  term.writeln(`\x1b[32m"${currentPath}" ${lines.length}L written\x1b[0m`);
}

function markDirty() {
  if (!isDirty) {
    isDirty = true;
    sendToParent({ type: 'vim:dirty' });
  }
}

/**
 * Insert text at the current cursor position (used for resource embeds).
 */
function insertAtCursor(text) {
  if (!text) return;
  // Ensure lines array has content
  if (lines.length === 0) lines = [''];

  const line = lines[cursorLine] ?? '';
  const before = line.slice(0, cursorCol);
  const after = line.slice(cursorCol);

  // If text contains newlines, split into multiple lines
  const parts = text.split('\n');
  if (parts.length === 1) {
    lines[cursorLine] = before + text + after;
    cursorCol += text.length;
  } else {
    const newLines = [
      before + parts[0],
      ...parts.slice(1, -1),
      parts[parts.length - 1] + after,
    ];
    lines.splice(cursorLine, 1, ...newLines);
    cursorLine += parts.length - 1;
    cursorCol = parts[parts.length - 1].length;
  }

  markDirty();

  // Re-render the buffer
  term.clear();
  for (let i = 0; i < Math.min(lines.length, 30); i++) {
    term.writeln(lines[i]);
  }
  // Position cursor hint
  term.write(`\x1b[${Math.min(cursorLine + 1, 30)};${cursorCol + 1}H`);
}

function updateStatusLine() {
  const modeText = vimMode === 'NORMAL' ? '' : `-- ${vimMode} --`;
  statusEl.textContent = modeText;
}

/**
 * Handle messages from parent window
 */
window.addEventListener('message', async (event) => {
  // Validate origin
  if (event.origin !== window.location.origin) {
    console.warn('Message from untrusted origin:', event.origin);
    return;
  }
  
  const msg = event.data;
  
  try {
    switch (msg.type) {
      case 'vim:load':
        await loadBuffer(msg.path, msg.content, msg.baseSha);
        break;
      case 'vim:reload':
        await reloadBuffer(msg.content, msg.baseSha);
        break;
      case 'vim:get-content':
        // Send current buffer content back to parent (for draft autosave)
        sendToParent({ type: 'vim:content', content: lines.join('\n') });
        break;
      case 'vim:close':
        cleanup();
        break;
      case 'vim:focus':
        term.focus();
        break;
      case 'vim:insert':
        insertAtCursor(msg.content || '');
        break;
      default:
        console.warn('Unknown message type:', msg.type);
    }
  } catch (error) {
    sendToParent({ type: 'vim:error', message: error.message, fatal: false });
  }
});

/**
 * Load buffer content
 */
async function loadBuffer(path, content, baseSha) {
  currentPath = path;
  currentBuffer = content;
  currentBaseSha = baseSha;
  isDirty = false;
  
  // Parse content into lines
  lines = content.split('\n');
  cursorLine = 0;
  cursorCol = 0;
  
  // Clear terminal and render content
  term.clear();
  
  for (let i = 0; i < Math.min(lines.length, 30); i++) {
    term.writeln(lines[i]);
  }
  
  // Fill remaining with tildes
  const visibleLines = Math.min(lines.length, 30);
  for (let i = visibleLines; i < 30; i++) {
    term.writeln('\x1b[32m~\x1b[0m');
  }
  
  updateStatusLine();
  term.focus();
}

/**
 * Reload buffer (external change)
 */
async function reloadBuffer(content, baseSha) {
  currentBuffer = content;
  currentBaseSha = baseSha;
  isDirty = false;
  lines = content.split('\n');
  
  term.clear();
  for (let i = 0; i < Math.min(lines.length, 30); i++) {
    term.writeln(lines[i]);
  }
  
  term.writeln('\x1b[33mBuffer reloaded from server\x1b[0m');
}

/**
 * Send message to parent window
 */
function sendToParent(message) {
  window.parent.postMessage(message, window.location.origin);
}

/**
 * Start heartbeat to parent
 */
function startHeartbeat() {
  heartbeatTimer = setInterval(() => {
    sendToParent({ type: 'vim:heartbeat', timestamp: Date.now() });
  }, 10000); // every 10 seconds
}

/**
 * Show error banner
 */
function showError(message) {
  const banner = document.createElement('div');
  banner.className = 'error-banner';
  banner.textContent = message;
  document.body.prepend(banner);
}

/**
 * Cleanup on close
 */
function cleanup() {
  if (heartbeatTimer) {
    clearInterval(heartbeatTimer);
  }
  if (term) {
    term.dispose();
  }
}

// Initialize on load
init().catch(error => {
  showError('Fatal error: ' + error.message);
  sendToParent({ type: 'vim:error', message: error.message, fatal: true });
});
