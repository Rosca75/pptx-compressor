// report.js — Renders the before/after report after a successful run.
//
// Shown when GetProgress reports state "done". Displays total size before and
// after, the percentage saved, the output path, and an "open folder" action.

import { formatBytes, formatSavings, escapeHtml } from './helpers.js';
import { apiOpenOutputFolder } from './api.js';

/**
 * Render the before/after report card from a completed ProgressResult.
 * @param {Object} p - ProgressResult { bytesBefore, bytesAfter, outputPath, errors }.
 */
export function renderReport(p) {
  const card = document.getElementById('report-card');
  if (!card || !p) return;

  card.style.display = '';
  card.innerHTML = `
    <div class="panel-header">Compression complete</div>
    <p>Before: <strong>${formatBytes(p.bytesBefore)}</strong> &rarr;
       After: <strong>${formatBytes(p.bytesAfter)}</strong>
       (<strong>${formatSavings(p.bytesBefore, p.bytesAfter)}</strong>)</p>
    <p>Saved to: <code>${escapeHtml(p.outputPath || '')}</code></p>
    <div class="btn-row">
      <button class="btn btn-outline" id="open-folder-btn">Open folder</button>
    </div>
  `;

  const openBtn = document.getElementById('open-folder-btn');
  if (openBtn) {
    openBtn.addEventListener('click', () => apiOpenOutputFolder(p.outputPath));
  }

  // TODO: list per-image errors from p.errors when present.
}
