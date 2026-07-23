// videos.js — Renders the list of video parts in the Videos tab.
//
// The video ACTIONS (remove / MP4 compression) are wired in options.js and live
// in the #video-options block inside this tab; this module only lists the video
// parts found so the user can see what those actions will affect.

import { formatBytes, escapeHtml } from './helpers.js';

/**
 * Render the video-parts table from an AnalysisResult.
 * @param {Object} analysis - AnalysisResult { media[] }.
 */
export function renderVideos(analysis) {
  const table = document.getElementById('videos-table');
  const tbody = document.getElementById('videos-tbody');
  if (!tbody) return;

  const vids = ((analysis && analysis.media) || []).filter((m) => m.isVideo);
  if (table) table.style.display = vids.length ? '' : 'none';
  tbody.innerHTML = vids.map(rowHtml).join('');
}

/** One row per video part. */
function rowHtml(m) {
  const usage = m.usage || (m.usedOnSlide ? 'slide' : 'unused');
  return `<tr>
    <td class="part-name" title="${escapeHtml(m.partName)}">${escapeHtml(short(m.partName))}</td>
    <td>${escapeHtml(m.isMp4 ? 'mp4' : (m.format || 'video'))}</td>
    <td class="num">${formatBytes(m.bytes)}</td>
    <td>${escapeHtml(usage)}</td>
  </tr>`;
}

/** Trim the ppt/media/ prefix for display. */
function short(part) {
  return String(part || '').replace(/^ppt\/media\//, '');
}
