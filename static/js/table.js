// table.js — Renders the media analysis table (Zone D).
//
// Given an AnalysisResult, builds one row per media part with: a lazy-loaded
// thumbnail, the observed facts (format, dimensions, size, alpha, refs), the
// proposed action, an estimated size, and a per-image override dropdown
// (Auto / Skip / Remove). Column headers are click-to-sort.

import { formatBytes, formatDimensions, formatSavings, escapeHtml } from './helpers.js';
import { apiGetImagePreview } from './api.js';
import { state } from './state.js';

// Local render/sort state (view-only; the source of truth stays in state.js).
let currentMedia = [];
let sortKey = null;
let sortDir = 1; // 1 = ascending, -1 = descending

/**
 * Render the analysis table from an AnalysisResult.
 * @param {Object} result - AnalysisResult { media[], ... }.
 */
export function renderTable(result) {
  const emptyState = document.getElementById('empty-state');
  const table = document.getElementById('media-table');
  const reportCard = document.getElementById('report-card');

  currentMedia = (result && result.media) || [];

  // A fresh analysis hides any prior report card.
  if (reportCard) reportCard.style.display = 'none';

  const has = currentMedia.length > 0;
  if (emptyState) emptyState.style.display = has ? 'none' : '';
  if (table) table.style.display = has ? '' : 'none';

  wireSortHeaders();
  drawRows();
}

/** Attach one-time click handlers to the sortable column headers. */
function wireSortHeaders() {
  document.querySelectorAll('.media-table th.sortable').forEach((th) => {
    if (th.dataset.wired) return;
    th.dataset.wired = '1';
    th.addEventListener('click', () => {
      const key = th.dataset.sort;
      if (sortKey === key) sortDir = -sortDir; // toggle direction
      else { sortKey = key; sortDir = 1; }
      drawRows();
      markSortedHeader();
    });
  });
}

/** Add a visual marker (▲/▼) to the currently-sorted header. */
function markSortedHeader() {
  document.querySelectorAll('.media-table th.sortable').forEach((th) => {
    const base = th.textContent.replace(/[ ▲▼]+$/, '');
    th.textContent = base + (th.dataset.sort === sortKey ? (sortDir > 0 ? ' ▲' : ' ▼') : '');
  });
}

/** Sort (if a key is set) and rebuild all table rows. */
function drawRows() {
  const tbody = document.getElementById('media-tbody');
  if (!tbody) return;

  const rows = currentMedia.slice();
  if (sortKey) {
    rows.sort((a, b) => sortDir * compareBy(a, b, sortKey));
  }

  tbody.innerHTML = rows.map(rowHtml).join('');
  wireRowControls(tbody);
  loadThumbnails(tbody, rows);
}

/** Comparison for the sort key; "pixels" is derived from width*height. */
function compareBy(a, b, key) {
  const val = (m) => {
    if (key === 'pixels') return (m.width || 0) * (m.height || 0);
    if (key === 'hasAlpha') return m.hasAlpha ? 1 : 0;
    return m[key];
  };
  const av = val(a), bv = val(b);
  if (typeof av === 'string') return av.localeCompare(bv);
  return (av || 0) - (bv || 0);
}

/** Build the HTML for a single MediaInfo row. */
function rowHtml(m) {
  const override = state.overrides[m.partName] || 'auto';
  const effAction = effectiveAction(m, override);
  const effEst = effectiveEstimate(m, override);

  return `<tr data-part="${escapeHtml(m.partName)}">
    <td class="thumb-cell"><div class="thumb" data-part="${escapeHtml(m.partName)}"></div></td>
    <td class="part-name" title="${escapeHtml(m.partName)}">${escapeHtml(shortName(m.partName))}</td>
    <td>${escapeHtml(m.format)}</td>
    <td class="num">${formatDimensions(m.width, m.height)}</td>
    <td class="num">${formatBytes(m.bytes)}</td>
    <td>${m.hasAlpha ? 'yes' : '—'}</td>
    <td class="num">${m.refCount}${m.refCount === 0 ? ' <small>unused</small>' : ''}</td>
    <td><span class="${badgeClass(effAction)}">${escapeHtml(effAction)}</span></td>
    <td class="num">${formatBytes(effEst)} <small>${formatSavings(m.bytes, effEst)}</small></td>
    <td>
      <select class="override-select" data-part="${escapeHtml(m.partName)}">
        <option value="auto"${override === 'auto' ? ' selected' : ''}>Auto</option>
        <option value="skip"${override === 'skip' ? ' selected' : ''}>Skip</option>
        <option value="remove"${override === 'remove' ? ' selected' : ''}>Remove</option>
      </select>
    </td>
  </tr>`;
}

/** Map an action label to a badge colour class (see table.css). */
function badgeClass(action) {
  const a = (action || '').toLowerCase();
  let cls = 'action-badge';
  if (a.includes('remove')) cls += ' remove';
  else if (a.includes('skip') || a.includes('kept')) cls += ' skip';
  else if (a.includes('jpeg') || a.includes('convert') || a.includes('webp')) cls += ' convert';
  else if (a) cls += ' recompress';
  return cls;
}

/** The action to display given a per-image override. */
function effectiveAction(m, override) {
  if (override === 'skip') return 'skip';
  if (override === 'remove') return 'remove';
  return m.proposedAction || '—';
}

/** The estimated size to display given a per-image override. */
function effectiveEstimate(m, override) {
  if (override === 'skip') return m.bytes;
  if (override === 'remove') return 0;
  return m.estimatedBytes;
}

/** Trim the ppt/media/ prefix for display; full name stays in the title attr. */
function shortName(part) {
  return String(part || '').replace(/^ppt\/media\//, '');
}

/** Wire the per-row override dropdowns to state.overrides. */
function wireRowControls(tbody) {
  tbody.querySelectorAll('.override-select').forEach((sel) => {
    sel.addEventListener('change', () => {
      const part = sel.dataset.part;
      if (sel.value === 'auto') delete state.overrides[part];
      else state.overrides[part] = sel.value;
      drawRows(); // re-render so the action badge + estimate reflect the override
    });
  });
}

/**
 * Lazily fetch and inject thumbnails for raster parts. Runs after rows are in
 * the DOM so the table appears immediately; previews fill in as they arrive.
 */
function loadThumbnails(tbody, rows) {
  rows.forEach((m) => {
    // Vectors / media have no raster preview.
    if (['emf', 'wmf', 'svg', 'media', 'unknown'].includes(m.format)) return;
    const cell = tbody.querySelector(`.thumb[data-part="${cssEscape(m.partName)}"]`);
    if (!cell) return;
    apiGetImagePreview(m.partName).then((b64) => {
      if (b64) cell.innerHTML = `<img alt="" src="data:image/jpeg;base64,${b64}">`;
    }).catch(() => { /* preview is best-effort */ });
  });
}

/** Minimal CSS attribute-selector escaping for part names. */
function cssEscape(s) {
  return String(s).replace(/["\\]/g, '\\$&');
}
