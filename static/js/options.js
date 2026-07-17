// options.js — The compression options panel (Zone C).
//
// Reads the option controls into state.options so compress.js can build the
// CompressionRequest. Controls start disabled in index.html and are enabled
// once a file has been analysed. The preset selector seeds the fine-grained
// controls; moving a slider or toggling a checkbox afterwards overrides the
// preset (exactly like resolveOptions on the Go side).

import { state } from './state.js';

// Preset → default control values. Mirrors resolveOptions() in analyzer.go so
// the UI preview matches what the backend will actually do.
const PRESETS = {
  light:      { jpegQuality: 90, maxEdgePx: 2560, convertOpaquePng: false, quantizeTransparentPng: false },
  balanced:   { jpegQuality: 82, maxEdgePx: 1920, convertOpaquePng: true,  quantizeTransparentPng: false },
  aggressive: { jpegQuality: 72, maxEdgePx: 1440, convertOpaquePng: true,  quantizeTransparentPng: true  },
};

// Cache the control elements once per call (cheap; keeps the code readable).
function els() {
  return {
    preset:        document.getElementById('opt-preset'),
    quality:       document.getElementById('opt-jpeg-quality'),
    qualityValue:  document.getElementById('opt-jpeg-quality-value'),
    maxEdge:       document.getElementById('opt-max-edge'),
    minSize:       document.getElementById('opt-min-size'),
    convertPng:    document.getElementById('opt-convert-opaque-png'),
    quantizePng:   document.getElementById('opt-quantize-transparent-png'),
    useWebp:       document.getElementById('opt-use-webp'),
    removeUnused:  document.getElementById('opt-remove-unused-media'),
    stripFonts:    document.getElementById('opt-strip-embedded-fonts'),
    webpWarning:   document.getElementById('webp-warning'),
    outputMode:    document.querySelectorAll('input[name="opt-output-mode"]'),
    replaceWarning: document.getElementById('replace-warning'),
    videoGroup:    document.getElementById('video-options'),
    videoCount:    document.getElementById('video-count'),
    removeVideos:  document.getElementById('opt-remove-videos'),
    videoCompression: document.getElementById('opt-video-compression'),
    removeVideosWarning: document.getElementById('remove-videos-warning'),
    ffmpegWarning: document.getElementById('ffmpeg-warning'),
  };
}

/** Return the currently-selected output mode ("copy" or "replace"). */
function selectedOutputMode(radios) {
  for (const r of radios) if (r.checked) return r.value;
  return 'copy';
}

/** Wire up the options panel. Called once on init. */
export function initOptions() {
  const e = els();

  // Live-update the quality label as the slider moves.
  if (e.quality && e.qualityValue) {
    e.quality.addEventListener('input', () => {
      e.qualityValue.textContent = e.quality.value;
      state.options.jpegQuality = parseInt(e.quality.value, 10);
    });
  }

  // Changing the preset seeds quality / max-edge / format toggles.
  if (e.preset) {
    e.preset.addEventListener('change', () => applyPreset(e.preset.value));
  }

  // Toggling WebP shows/hides the compatibility warning.
  if (e.useWebp && e.webpWarning) {
    e.useWebp.addEventListener('change', () => {
      e.webpWarning.style.display = e.useWebp.checked ? '' : 'none';
    });
  }

  // Choosing "Replace the original file" shows a destructive-action warning.
  if (e.outputMode && e.replaceWarning) {
    e.outputMode.forEach((r) => {
      r.addEventListener('change', () => {
        const replace = selectedOutputMode(e.outputMode) === 'replace';
        e.replaceWarning.style.display = replace ? '' : 'none';
        state.options.replaceOriginal = replace;
      });
    });
  }

  // Ticking "Remove videos" shows its warning and greys out the compression
  // select — removal always wins over compression, so offering both at once
  // would be misleading.
  if (e.removeVideos) {
    e.removeVideos.addEventListener('change', () => {
      state.options.removeVideos = e.removeVideos.checked;
      if (e.removeVideosWarning) {
        e.removeVideosWarning.style.display = e.removeVideos.checked ? '' : 'none';
      }
      syncVideoCompressionEnabled();
    });
  }

  if (e.videoCompression) {
    e.videoCompression.addEventListener('change', () => {
      state.options.videoCompression = e.videoCompression.value;
    });
  }

  // Reflect the initial preset defaults into the controls.
  if (e.preset) applyPreset(e.preset.value);
}

/**
 * Show or hide the Videos option group based on the latest analysis.
 * Called by analyze.js after each successful analysis (and AFTER
 * setOptionsEnabled, which blanket-enables every panel control).
 *
 * The group only appears when the deck actually contains videos, so users
 * without videos never see the extra controls. The compression select is
 * additionally gated on ffmpeg being installed; removal works without it.
 * @param {Object} analysis - AnalysisResult { videoCount, ffmpegAvailable }.
 */
export function updateVideoOptions(analysis) {
  const e = els();
  const count = (analysis && analysis.videoCount) || 0;
  state.ffmpegAvailable = !!(analysis && analysis.ffmpegAvailable);

  if (e.videoGroup) e.videoGroup.style.display = count > 0 ? '' : 'none';
  if (e.videoCount) e.videoCount.textContent = String(count);

  if (count === 0) {
    // No videos: make sure stale choices from a previous file don't linger.
    if (e.removeVideos) e.removeVideos.checked = false;
    if (e.videoCompression) e.videoCompression.value = 'none';
    if (e.removeVideosWarning) e.removeVideosWarning.style.display = 'none';
    state.options.removeVideos = false;
    state.options.videoCompression = 'none';
    return;
  }

  if (e.ffmpegWarning) e.ffmpegWarning.style.display = state.ffmpegAvailable ? 'none' : '';
  syncVideoCompressionEnabled();
}

/**
 * The MP4-compression select is usable only when ffmpeg exists AND
 * "Remove videos" is not ticked (removal makes compression moot).
 */
function syncVideoCompressionEnabled() {
  const e = els();
  if (!e.videoCompression) return;
  const removing = !!(e.removeVideos && e.removeVideos.checked);
  e.videoCompression.disabled = removing || !state.ffmpegAvailable;
}

/**
 * Apply a preset's defaults to the fine-grained controls and state.
 * The user can still override any control afterwards.
 * @param {string} name - "light" | "balanced" | "aggressive"
 */
function applyPreset(name) {
  const p = PRESETS[name] || PRESETS.balanced;
  const e = els();

  if (e.quality)      { e.quality.value = p.jpegQuality; }
  if (e.qualityValue) { e.qualityValue.textContent = String(p.jpegQuality); }
  if (e.maxEdge)      { e.maxEdge.value = p.maxEdgePx; }
  if (e.convertPng)   { e.convertPng.checked = p.convertOpaquePng; }
  if (e.quantizePng)  { e.quantizePng.checked = p.quantizeTransparentPng; }

  state.options.preset = name;
  state.options.jpegQuality = p.jpegQuality;
  state.options.maxEdgePx = p.maxEdgePx;
  state.options.convertOpaquePng = p.convertOpaquePng;
  state.options.quantizeTransparentPng = p.quantizeTransparentPng;
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
 * Read the current control values into a CompressionOptions-shaped object
 * (matching types.go). Numbers are parsed; missing controls fall back to a
 * sensible default. Per-image overrides come from the table (state.overrides).
 * @returns {Object} options matching CompressionOptions in types.go.
 */
export function readOptions() {
  const e = els();
  const intVal = (el, dflt) => {
    const n = parseInt(el && el.value, 10);
    return isNaN(n) ? dflt : n;
  };

  return {
    preset: (e.preset && e.preset.value) || 'balanced',
    jpegQuality: intVal(e.quality, 82),
    maxEdgePx: intVal(e.maxEdge, 0),
    minSizeKB: intVal(e.minSize, 20),
    convertOpaquePng: !!(e.convertPng && e.convertPng.checked),
    quantizeTransparentPng: !!(e.quantizePng && e.quantizePng.checked),
    useWebp: !!(e.useWebp && e.useWebp.checked),
    removeUnusedMedia: !!(e.removeUnused && e.removeUnused.checked),
    stripEmbeddedFonts: !!(e.stripFonts && e.stripFonts.checked),
    removeVideos: !!(e.removeVideos && e.removeVideos.checked),
    videoCompression: (e.videoCompression && e.videoCompression.value) || 'none',
    replaceOriginal: selectedOutputMode(e.outputMode) === 'replace',
    perImageOverrides: { ...state.overrides },
  };
}
