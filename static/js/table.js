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

// Set of part names currently ticked for a bulk override. View-only transient
// selection (like sortKey), keyed by part name so it survives re-sorts.
const selected = new Set();

/**
 * Render the analysis table from an AnalysisResult.
 * @param {Object} result - AnalysisResult { media[], ... }.
 */
export function renderTable(result) {
  const emptyState = document.getElementById('empty-state');
  const table = document.getElementById('media-table');
  const reportCard = document.getElementById('report-card');

  currentMedia = (result && result.media) || [];

  // A fresh analysis clears any prior selection and hides the report card.
  selected.clear();
  if (reportCard) reportCard.style.display = 'none';

  const has = currentMedia.length > 0;
  if (emptyState) emptyState.style.display = has ? 'none' : '';
  if (table) table.style.display = has ? '' : 'none';

  wireSortHeaders();
  wireBulkControls();
  drawRows();
  updateBulkBar();
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
    if (key === 'usedOnSlide') return m.usedOnSlide ? 1 : 0;
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

  const isSel = selected.has(m.partName);
  return `<tr data-part="${escapeHtml(m.partName)}"${isSel ? ' class="row-selected"' : ''}>
    <td class="select-cell"><input type="checkbox" class="row-select" data-part="${escapeHtml(m.partName)}"${isSel ? ' checked' : ''}></td>
    <td class="thumb-cell"><div class="thumb" data-part="${escapeHtml(m.partName)}"></div></td>
    <td class="part-name" title="${escapeHtml(m.partName)}">${escapeHtml(shortName(m.partName))}</td>
    <td>${escapeHtml(m.isVideo ? 'video' : m.format)}</td>
    <td class="num">${formatDimensions(m.width, m.height)}</td>
    <td class="num">${formatBytes(m.bytes)}</td>
    <td>${m.hasAlpha ? 'yes' : '—'}</td>
    <td>${usageCell(m)}</td>
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

/**
 * Build the "Usage" cell: whether the image is actually placed on a slide, only
 * on a layout/master, or nowhere (present but unused). The label comes from the
 * backend (MediaInfo.usage); the reference count is kept in the tooltip.
 */
function usageCell(m) {
  const usage = m.usage || (m.usedOnSlide ? 'slide' : 'unused');
  const unused = /^unused/.test(usage);
  const cls = 'usage-badge' + (unused ? ' unused' : (m.usedOnSlide ? ' on-slide' : ' off-slide'));
  const title = `${m.refCount || 0} relationship reference(s)`;
  return `<span class="${cls}" title="${escapeHtml(title)}">${escapeHtml(usage)}</span>`;
}

/** Trim the ppt/media/ prefix for display; full name stays in the title attr. */
function shortName(part) {
  return String(part || '').replace(/^ppt\/media\//, '');
}

/** Wire the per-row override dropdowns and selection checkboxes. */
function wireRowControls(tbody) {
  tbody.querySelectorAll('.override-select').forEach((sel) => {
    sel.addEventListener('change', () => {
      const part = sel.dataset.part;
      if (sel.value === 'auto') delete state.overrides[part];
      else state.overrides[part] = sel.value;
      drawRows(); // re-render so the action badge + estimate reflect the override
    });
  });

  tbody.querySelectorAll('.row-select').forEach((cb) => {
    cb.addEventListener('change', () => {
      const part = cb.dataset.part;
      if (cb.checked) selected.add(part);
      else selected.delete(part);
      // Reflect selection styling on the row without a full redraw.
      const tr = cb.closest('tr');
      if (tr) tr.classList.toggle('row-selected', cb.checked);
      updateBulkBar();
    });
  });
}

/**
 * Wire the bulk-action bar controls and the header "select all" checkbox.
 * Guarded so the listeners are attached only once (like wireSortHeaders).
 */
function wireBulkControls() {
  const selectAll = document.getElementById('select-all');
  if (selectAll && !selectAll.dataset.wired) {
    selectAll.dataset.wired = '1';
    selectAll.addEventListener('change', () => {
      if (selectAll.checked) currentMedia.forEach((m) => selected.add(m.partName));
      else selected.clear();
      drawRows();
      updateBulkBar();
    });
  }

  const applyBtn = document.getElementById('bulk-apply');
  if (applyBtn && !applyBtn.dataset.wired) {
    applyBtn.dataset.wired = '1';
    applyBtn.addEventListener('click', () => {
      const value = document.getElementById('bulk-override').value;
      selected.forEach((part) => {
        if (value === 'auto') delete state.overrides[part];
        else state.overrides[part] = value;
      });
      drawRows(); // reflect new action badges + estimates
    });
  }

  const clearBtn = document.getElementById('bulk-clear');
  if (clearBtn && !clearBtn.dataset.wired) {
    clearBtn.dataset.wired = '1';
    clearBtn.addEventListener('click', () => {
      selected.clear();
      drawRows();
      updateBulkBar();
    });
  }
}

/** Show/hide the bulk bar and sync the count + "select all" checkbox state. */
function updateBulkBar() {
  const bar = document.getElementById('bulk-actions');
  const count = document.getElementById('bulk-count');
  const selectAll = document.getElementById('select-all');

  const n = selected.size;
  if (bar) bar.style.display = n > 0 ? '' : 'none';
  if (count) count.textContent = n + ' selected';

  if (selectAll) {
    const total = currentMedia.length;
    selectAll.checked = total > 0 && n === total;
    // Show the partial-selection (indeterminate) state when some, but not all,
    // rows are ticked.
    selectAll.indeterminate = n > 0 && n < total;
  }
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
