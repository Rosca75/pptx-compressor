// report.js — Renders the before/after report after a successful run.
//
// Shown when GetProgress reports state "done". Displays the whole-file size
// before and after, the percentage saved, a per-image results table, any
// errors, the output path, and an "open folder" action.

import { formatBytes, formatSavings, escapeHtml } from './helpers.js';
import { apiOpenOutputFolder } from './api.js';

/**
 * Render the before/after report card from a completed ProgressResult.
 * @param {Object} p - ProgressResult (see types.go).
 */
export function renderReport(p) {
  const card = document.getElementById('report-card');
  if (!card || !p) return;

  // Prefer whole-file sizes for the headline; fall back to media totals.
  const before = p.fileBytesBefore || p.bytesBefore || 0;
  const after = p.fileBytesAfter || p.bytesAfter || 0;

  // Hide the whole analysis view (tabs + composition) so the report is the focus.
  const view = document.getElementById('analysis-view');
  if (view) view.style.display = 'none';
  card.style.display = '';

  card.innerHTML = `
    <div class="panel-header">Compression complete</div>
    <div class="report-headline">
      <span class="report-before">${formatBytes(before)}</span>
      <span class="report-arrow">&rarr;</span>
      <span class="report-after">${formatBytes(after)}</span>
      <span class="report-pct">${formatSavings(before, after)}</span>
    </div>
    <p class="report-path">Saved to: <code>${escapeHtml(p.outputPath || '')}</code></p>
    ${resultsTable(p.results)}
    ${errorsBlock(p.errors)}
    <div class="btn-row">
      <button class="btn btn-outline" id="open-folder-btn">Open folder</button>
    </div>
  `;

  const openBtn = document.getElementById('open-folder-btn');
  if (openBtn) {
    openBtn.addEventListener('click', () => apiOpenOutputFolder(p.outputPath));
  }
}

/** Build the per-image results table (one row per processed media part). */
function resultsTable(results) {
  const rows = results || [];
  if (!rows.length) return '';

  const body = rows.map((r) => {
    const before = r.beforeBytes || 0;
    const after = r.afterBytes || 0;
    const shrank = after < before;
    return `<tr>
      <td class="part-name">${escapeHtml(String(r.partName || '').replace(/^ppt\/media\//, ''))}</td>
      <td><span class="${badgeClass(r.action)}">${escapeHtml(r.action || '—')}</span></td>
      <td class="num">${formatBytes(before)}</td>
      <td class="num">${formatBytes(after)}</td>
      <td class="num ${shrank ? 'saved' : ''}">${formatSavings(before, after)}</td>
    </tr>`;
  }).join('');

  return `<table class="media-table report-table">
    <thead><tr>
      <th>Part</th><th>Action</th><th class="num">Before</th>
      <th class="num">After</th><th class="num">Saved</th>
    </tr></thead>
    <tbody>${body}</tbody>
  </table>`;
}

/** Build a warning block listing any per-image errors. */
function errorsBlock(errors) {
  const errs = errors || [];
  if (!errs.length) return '';
  const items = errs.map((e) => `<li>${escapeHtml(e)}</li>`).join('');
  return `<div class="report-errors">
    <strong>${errs.length} warning(s):</strong>
    <ul>${items}</ul>
  </div>`;
}

/** Map an action label to a badge colour class (mirrors table.js). */
function badgeClass(action) {
  const a = (action || '').toLowerCase();
  let cls = 'action-badge';
  if (a.includes('remove')) cls += ' remove';
  else if (a.includes('skip') || a.includes('kept')) cls += ' skip';
  else if (a.includes('jpeg') || a.includes('convert') || a.includes('webp')) cls += ' convert';
  else if (a) cls += ' recompress';
  return cls;
}
