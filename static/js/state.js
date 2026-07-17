// state.js — Single source of truth for all shared application state.
// Other modules import and read/write this object directly. Never store
// shared state as module-level variables in other files.

/** Global application state shared across all modules. */
export const state = {
  /** Absolute path of the selected .pptx (null before selection). */
  pptxPath: null,

  /** Latest AnalysisResult from AnalyzePptx (null before first analysis). */
  analysis: null,

  /**
   * Whether the backend found an ffmpeg executable (from the latest
   * AnalysisResult). Gates the MP4 video-compression control.
   */
  ffmpegAvailable: false,

  /** Interval timer ID for polling compression progress (null when idle). */
  pollTimer: null,

  /** Latest ProgressResult from GetProgress (null before first run). */
  progress: null,

  /**
   * Per-image action overrides keyed by part name, e.g.
   * { "ppt/media/image3.png": "skip" }. Sent as options.perImageOverrides.
   */
  overrides: {},

  /** Current values of the options panel (mirrors CompressionOptions). */
  options: {
    preset: 'balanced',
    jpegQuality: 82,
    maxEdgePx: 0,
    minSizeKB: 20,
    convertOpaquePng: true,
    quantizeTransparentPng: true,
    useWebp: false,
    removeUnusedMedia: true,
    stripEmbeddedFonts: false,
    replaceOriginal: false,
    removeVideos: false,
    videoCompression: 'none',
  },
};
