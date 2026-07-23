// tabs.js — Main-area orchestration: file-composition breakdown + the
// Images / Videos / Fonts tabs.
//
// After a successful analysis, analyze.js calls renderAnalysis(), which shows
// the composition bar, fills each tab's size/potential badge, greys out empty
// categories, renders each panel (delegating to table.js / videos.js /
// fonts.js), and opens the largest non-empty category by default — so a
// font-bloated deck lands straight on the Fonts tab.

import { state } from './state.js';
import { formatBytes } from './helpers.js';
import { renderTable } from './table.js';
import { renderVideos } from './videos.js';
import { renderFonts } from './fonts.js';
import { updateVideoOptions } from './options.js';

const TAB_IDS = ['images', 'videos', 'fonts'];

/** Wire the tab buttons once on init. */
export function initTabs() {
  document.querySelectorAll('#tab-bar .tab-btn').forEach((btn) => {
    if (btn.dataset.wired) return;
    btn.dataset.wired = '1';
    btn.addEventListener('click', () => {
      if (btn.classList.contains('disabled')) return;
      setActiveTab(btn.dataset.tab);
    });
  });
}

/**
 * Render the whole analysis view from an AnalysisResult.
 * @param {Object} analysis - AnalysisResult (see types.go).
 */
export function renderAnalysis(analysis) {
  const view = document.getElementById('analysis-view');
  const empty = document.getElementById('empty-state');
  const report = document.getElementById('report-card');
  if (empty) empty.style.display = 'none';
  if (report) report.style.display = 'none';
  if (view) view.style.display = '';

  const cats = categoryStats(analysis);

  renderComposition(analysis);
  renderTable(analysis);       // Images tab
  renderVideos(analysis);      // Videos tab (list)
  updateVideoOptions(analysis); // Videos tab (controls)
  renderFonts(analysis);       // Fonts tab

  // Per-tab size + potential badges.
  setTabMeta('images', cats.images.count > 0
    ? `${cats.images.count} · ${formatBytes(cats.images.bytes)}` +
      (cats.images.potential > 0 ? ` · save ~${formatBytes(cats.images.potential)}` : '')
    : 'none');
  setTabMeta('videos', cats.videos.count > 0
    ? `${cats.videos.count} · ${formatBytes(cats.videos.bytes)}`
    : 'none');
  setTabMeta('fonts', cats.fonts.count > 0
    ? `${cats.fonts.count} · ${formatBytes(cats.fonts.bytes)} removable`
    : 'none');

  setTabEnabled('images', cats.images.count > 0);
  setTabEnabled('videos', cats.videos.count > 0);
  setTabEnabled('fonts', cats.fonts.count > 0);

  // Open the biggest non-empty category so the space hog is front-and-centre.
  const order = TAB_IDS
    .filter((k) => cats[k].count > 0)
    .sort((a, b) => cats[b].bytes - cats[a].bytes);
  setActiveTab(order[0] || 'images');
}

/** Switch the visible panel and highlight the active tab button. */
export function setActiveTab(name) {
  state.activeTab = name;
  TAB_IDS.forEach((k) => {
    const btn = document.getElementById('tab-btn-' + k);
    const panel = document.getElementById('panel-' + k);
    const active = k === name;
    if (btn) btn.classList.toggle('active', active);
    if (panel) panel.style.display = active ? '' : 'none';
  });
}

/** Count + size + estimated-saving per category, derived from the analysis. */
function categoryStats(a) {
  const media = (a && a.media) || [];
  let imgCount = 0;
  let imgPotential = 0;
  media.forEach((m) => {
    if (m.isVideo) return;
    imgCount++;
    const save = (m.bytes || 0) - (m.estimatedBytes || 0);
    if (save > 0) imgPotential += save;
  });
  const fonts = (a && a.fonts) || [];
  return {
    images: { count: imgCount, bytes: (a && a.imageBytes) || 0, potential: imgPotential },
    videos: { count: (a && a.videoCount) || 0, bytes: (a && a.videoBytes) || 0 },
    // Every embedded-font byte is reclaimable, so the whole size is the potential.
    fonts: { count: fonts.length, bytes: (a && a.fontBytes) || 0 },
  };
}

/** Draw the stacked file-composition bar + legend (images/videos/fonts/other). */
function renderComposition(a) {
  const total = (a && a.fileBytes) || 0;
  const totalEl = document.getElementById('composition-total');
  if (totalEl) totalEl.textContent = formatBytes(total);

  const segs = [
    { label: 'Images', bytes: (a && a.imageBytes) || 0, cls: 'seg-images' },
    { label: 'Videos', bytes: (a && a.videoBytes) || 0, cls: 'seg-videos' },
    { label: 'Fonts',  bytes: (a && a.fontBytes) || 0,  cls: 'seg-fonts' },
    { label: 'Other',  bytes: (a && a.otherBytes) || 0, cls: 'seg-other' },
  ].filter((s) => s.bytes > 0);

  const denom = total > 0 ? total : (segs.reduce((n, s) => n + s.bytes, 0) || 1);

  const bar = document.getElementById('composition-bar');
  if (bar) {
    bar.innerHTML = segs.map((s) => {
      const pct = (s.bytes / denom) * 100;
      return `<span class="composition-seg ${s.cls}" style="width:${pct.toFixed(2)}%"` +
             ` title="${s.label}: ${formatBytes(s.bytes)}"></span>`;
    }).join('');
  }

  const legend = document.getElementById('composition-legend');
  if (legend) {
    legend.innerHTML = segs.map((s) => {
      const pct = Math.round((s.bytes / denom) * 100);
      return `<span class="composition-key"><span class="composition-dot ${s.cls}"></span>` +
             `${s.label} — ${formatBytes(s.bytes)} (${pct}%)</span>`;
    }).join('');
  }
}

/** Set a tab button's size/potential badge text. */
function setTabMeta(name, text) {
  const el = document.getElementById('tab-meta-' + name);
  if (el) el.textContent = text;
}

/** Enable or grey out a tab button (empty categories are greyed). */
function setTabEnabled(name, enabled) {
  const btn = document.getElementById('tab-btn-' + name);
  if (!btn) return;
  btn.classList.toggle('disabled', !enabled);
  btn.disabled = !enabled;
}
