// options.js — The compression options panel (Zone C).
//
// Reads the option controls into state.options so compress.js can build the
// CompressionRequest. Controls start disabled in index.html and are enabled
// once a file has been analysed.
//
// SKELETON: only the JPEG-quality live label and the enable/disable wiring are
// sketched here; per-control change handlers are filled in a later session.

import { state } from './state.js';

/** Wire up the options panel. Called once on init. */
export function initOptions() {
  const quality = document.getElementById('opt-jpeg-quality');
  const qualityValue = document.getElementById('opt-jpeg-quality-value');

  // Live-update the quality label as the slider moves.
  if (quality && qualityValue) {
    quality.addEventListener('input', () => {
      qualityValue.textContent = quality.value;
      state.options.jpegQuality = parseInt(quality.value, 10);
    });
  }

  // TODO: wire preset, maxEdgePx, minSizeKB and the checkboxes into
  // state.options, and apply preset defaults when the preset changes.
}

/**
 * Enable or disable every control in the options panel.
 * Called with true once a file has been analysed.
 * @param {boolean} enabled
 */
export function setOptionsEnabled(enabled) {
  document.querySelectorAll('.options-panel input, .options-panel select')
    .forEach((el) => { el.disabled = !enabled; });
}

/**
 * Read the current control values into a CompressionOptions-shaped object.
 * TODO: read every control; for now returns the state defaults plus overrides.
 * @returns {Object} options matching CompressionOptions in types.go.
 */
export function readOptions() {
  return { ...state.options, perImageOverrides: state.overrides };
}
