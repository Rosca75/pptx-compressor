// fonts.js — The Fonts tab: per-family embedded-font selection.
//
// Embedded fonts are stored uncompressed and cannot be shrunk, only removed, so
// this tab lists one row per font family (largest first) with a checkbox. Ticked
// families flow into state.options.removeFontTypefaces, which readOptions() sends
// as the CompressionRequest; the backend strips exactly those families. Nothing
// is ticked by default — font removal changes render fidelity, so it stays
// opt-in, but the size of each family (and the running total) is shown loudly.

import { state } from './state.js';
import { formatBytes, escapeHtml } from './helpers.js';

// The families from the latest analysis (view-only; the selection source of
// truth is state.options.removeFontTypefaces).
let fonts = [];

/** Wire the "select all" checkbox once on init. */
export function initFonts() {
  const selectAll = document.getElementById('fonts-select-all');
  if (selectAll && !selectAll.dataset.wired) {
    selectAll.dataset.wired = '1';
    selectAll.addEventListener('change', () => {
      const on = selectAll.checked;
      document.querySelectorAll('#fonts-list .font-select').forEach((cb) => { cb.checked = on; });
      syncSelection();
    });
  }
}

/**
 * Render the per-font list from an AnalysisResult and reset the selection.
 * @param {Object} analysis - AnalysisResult { fonts[] }.
 */
export function renderFonts(analysis) {
  fonts = (analysis && analysis.fonts) || [];
  const list = document.getElementById('fonts-list');
  const selectAll = document.getElementById('fonts-select-all');

  // A fresh analysis clears any prior selection.
  state.options.removeFontTypefaces = [];
  if (selectAll) {
    selectAll.checked = false;
    selectAll.indeterminate = false;
    selectAll.disabled = fonts.length === 0;
  }

  if (!list) return;
  if (!fonts.length) {
    list.innerHTML = '<p class="option-note">This file has no embedded fonts.</p>';
    updateWarning();
    return;
  }

  list.innerHTML = fonts.map(rowHtml).join('');
  list.querySelectorAll('.font-select').forEach((cb) => {
    cb.addEventListener('change', syncSelection);
  });
  syncSelection();
}

/** One selectable row per font family. */
function rowHtml(f) {
  const weights = f.weights === 1 ? '1 weight' : `${f.weights} weights`;
  return `<label class="font-row">
    <input type="checkbox" class="font-select" data-typeface="${escapeHtml(f.typeface)}">
    <span class="font-name">${escapeHtml(f.typeface || '(unnamed font)')}</span>
    <span class="font-detail">${weights}</span>
    <span class="font-size num">${formatBytes(f.bytes)}</span>
  </label>`;
}

/** Collect ticked families into state and refresh the warning + tab badge. */
function syncSelection() {
  const chosen = [];
  document.querySelectorAll('#fonts-list .font-select').forEach((cb) => {
    if (cb.checked) chosen.push(cb.dataset.typeface);
  });
  state.options.removeFontTypefaces = chosen;

  const selectAll = document.getElementById('fonts-select-all');
  if (selectAll) {
    selectAll.checked = fonts.length > 0 && chosen.length === fonts.length;
    selectAll.indeterminate = chosen.length > 0 && chosen.length < fonts.length;
  }

  updateWarning();
  updateTabMeta(chosen);
}

/** Show the fidelity warning whenever at least one family is selected. */
function updateWarning() {
  const w = document.getElementById('strip-fonts-warning');
  if (w) w.style.display = (state.options.removeFontTypefaces || []).length ? '' : 'none';
}

/**
 * Keep the Fonts tab badge in sync with the live selection. Updates the DOM
 * element directly (rather than importing tabs.js) to avoid a circular import.
 */
function updateTabMeta(chosen) {
  const el = document.getElementById('tab-meta-fonts');
  if (!el) return;
  const totalBytes = fonts.reduce((n, f) => n + (f.bytes || 0), 0);
  if (!fonts.length) { el.textContent = 'none'; return; }
  if (!chosen.length) {
    el.textContent = `${fonts.length} · ${formatBytes(totalBytes)} removable`;
    return;
  }
  const selBytes = fonts
    .filter((f) => chosen.includes(f.typeface))
    .reduce((n, f) => n + (f.bytes || 0), 0);
  el.textContent = `${chosen.length}/${fonts.length} selected · −${formatBytes(selBytes)}`;
}
