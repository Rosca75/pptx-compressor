// table.js — Renders the media analysis table (Zone D).
//
// Given an AnalysisResult, builds one row per media part. The per-image
// skip/remove override controls are added in a later session.

import { formatBytes, formatDimensions, formatSavings, escapeHtml } from './helpers.js';

/**
 * Render the analysis table from an AnalysisResult.
 * @param {Object} result - AnalysisResult { path, media[], totalBytes, estimatedBytes }.
 */
export function renderTable(result) {
  const emptyState = document.getElementById('empty-state');
  const table = document.getElementById('media-table');
  const tbody = document.getElementById('media-tbody');
  if (!tbody) return;

  const media = (result && result.media) || [];

  // Toggle empty state vs. table.
  if (emptyState) emptyState.style.display = media.length ? 'none' : '';
  if (table) table.style.display = media.length ? '' : 'none';

  // Build rows. (TODO: add per-image override controls + preview on click.)
  tbody.innerHTML = media.map(rowHtml).join('');
}

/** Build the HTML for a single MediaInfo row. */
function rowHtml(m) {
  const action = (m.proposedAction || '').toLowerCase();
  // Map an action to a badge colour class (see table.css).
  let cls = 'action-badge';
  if (action.includes('remove')) cls += ' remove';
  else if (action.includes('skip')) cls += ' skip';
  else if (action.includes('jpeg') || action.includes('convert')) cls += ' convert';
  else if (action) cls += ' recompress';

  return `<tr>
    <td class="part-name">${escapeHtml(m.partName)}</td>
    <td>${escapeHtml(m.format)}</td>
    <td class="num">${formatDimensions(m.width, m.height)}</td>
    <td class="num">${formatBytes(m.bytes)}</td>
    <td>${m.hasAlpha ? 'yes' : '—'}</td>
    <td class="num">${m.refCount}</td>
    <td><span class="${cls}">${escapeHtml(m.proposedAction || '—')}</span></td>
    <td class="num">${formatBytes(m.estimatedBytes)} <small>${formatSavings(m.bytes, m.estimatedBytes)}</small></td>
  </tr>`;
}
