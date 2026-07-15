// helpers.js — Pure utility functions for formatting and display.
// No DOM access, no side effects. All functions are stateless.

/**
 * Format a byte count into a human-readable string (e.g. "4.3 MB").
 * Returns "--" for null/NaN input.
 */
export function formatBytes(bytes) {
  if (bytes == null || isNaN(bytes)) return "--";
  if (bytes === 0) return "0 B";
  const units = ["B", "KB", "MB", "GB", "TB"];
  let i = Math.floor(Math.log(bytes) / Math.log(1024));
  if (i >= units.length) i = units.length - 1;
  return (bytes / Math.pow(1024, i)).toFixed(i === 0 ? 0 : 1) + " " + units[i];
}

/**
 * Format a saved-bytes delta as a percentage of the original (e.g. "-42%").
 * Returns "--" when the original size is unknown or zero.
 */
export function formatSavings(before, after) {
  if (!before || before <= 0 || after == null) return "--";
  const pct = Math.round((1 - after / before) * 100);
  return (pct > 0 ? "-" : "") + Math.abs(pct) + "%";
}

/**
 * Format pixel dimensions as "1920 × 1080", or "--" when unknown (0×0).
 */
export function formatDimensions(w, h) {
  if (!w || !h) return "--";
  return w + " × " + h;
}

/**
 * Escape HTML special characters to prevent XSS when injecting text.
 */
export function escapeHtml(s) {
  if (!s) return "";
  return String(s).replace(/&/g, "&amp;").replace(/</g, "&lt;")
    .replace(/>/g, "&gt;").replace(/"/g, "&quot;");
}
