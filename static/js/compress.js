// compress.js — Compression run + progress polling (StartX / GetProgress / Cancel).
//
// Wires the Compress and Cancel buttons. Compress builds a CompressionRequest
// from the selected file + options, calls StartCompression, then polls
// GetProgress every 500ms until the job reaches a terminal state.

import { state } from './state.js';
import { apiStartCompression, apiGetProgress, apiCancelCompression } from './api.js';
import { readOptions } from './options.js';
import { showToast, showConfirm } from './components.js';
import { renderReport } from './report.js';
import { formatBytes } from './helpers.js';

const POLL_MS = 500;

/** Wire up the Compress and Cancel buttons. Called once on init. */
export function initCompress() {
  const compressBtn = document.getElementById('compress-btn');
  const cancelBtn = document.getElementById('cancel-btn');

  if (compressBtn) compressBtn.addEventListener('click', onCompress);
  if (cancelBtn) cancelBtn.addEventListener('click', onCancel);
}

/** Start a compression run for the selected file with the current options. */
async function onCompress() {
  if (!state.pptxPath) return;

  const req = { path: state.pptxPath, options: readOptions() };

  // Replacing the original is destructive and has no undo — confirm first.
  if (req.options.replaceOriginal) {
    showConfirm(
      'This will overwrite the original file with the compressed version. Continue?',
      () => startRun(req)
    );
    return;
  }
  startRun(req);
}

/** Kick off the background job and begin polling for progress. */
async function startRun(req) {
  try {
    await apiStartCompression(req);
    setRunningUI(true);
    startPolling();
  } catch (e) {
    showToast('Could not start compression: ' + e, 'error');
    setRunningUI(false);
  }
}

/** Ask the backend to cancel the running job. */
async function onCancel() {
  try {
    await apiCancelCompression();
  } catch (e) {
    showToast('Cancel failed: ' + e, 'error');
  }
}

/** Poll GetProgress until the job reaches a terminal state. */
function startPolling() {
  stopPolling();
  state.pollTimer = setInterval(async () => {
    try {
      const p = await apiGetProgress();
      state.progress = p;
      updateProgressUI(p);

      // Terminal states end the poll loop.
      if (p.state === 'done' || p.state === 'cancelled' || p.state === 'error') {
        stopPolling();
        setRunningUI(false);
        if (p.state === 'done') renderReport(p);
        else if (p.state === 'cancelled') showToast('Compression cancelled', 'error');
        else if (p.state === 'error') showToast('Compression failed', 'error');
      }
    } catch (e) {
      stopPolling();
      setRunningUI(false);
      showToast('Lost contact with backend: ' + e, 'error');
    }
  }, POLL_MS);
}

/** Stop the polling interval if active. */
function stopPolling() {
  if (state.pollTimer) {
    clearInterval(state.pollTimer);
    state.pollTimer = null;
  }
}

/** Toggle the top-bar UI between running and idle. */
function setRunningUI(running) {
  document.getElementById('compress-btn').style.display = running ? 'none' : '';
  document.getElementById('cancel-btn').style.display = running ? '' : 'none';
  document.getElementById('progress-section').style.display = running ? '' : 'none';
}

/** Push the latest progress values into the progress bar. */
function updateProgressUI(p) {
  if (!p) return;
  const pct = p.totalCount > 0 ? Math.round((p.processedCount / p.totalCount) * 100) : 0;
  document.getElementById('progress-fill').style.width = pct + '%';

  // Show the current file (trimmed of the ppt/media/ prefix) being processed.
  const current = (p.currentFile || '').replace(/^ppt\/media\//, '');
  document.getElementById('progress-phase').textContent = current
    ? 'Compressing ' + current + '…'
    : 'Compressing…';

  // Running bytes saved so far (before − after over processed parts).
  const saved = (p.bytesBefore || 0) - (p.bytesAfter || 0);
  const savedEl = document.getElementById('progress-saved');
  if (savedEl) savedEl.textContent = saved > 0 ? 'saved ' + formatBytes(saved) : '';

  document.getElementById('progress-counts').textContent =
    (p.processedCount || 0) + ' / ' + (p.totalCount || 0);
}
