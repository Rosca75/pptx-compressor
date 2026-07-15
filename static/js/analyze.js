// analyze.js — File selection + read-only analysis flow.
//
// Wires the "Open…" and "Analyze" buttons in the top bar. Selecting a file
// enables Analyze; a successful analysis populates state.analysis and enables
// Compress. All Go calls go through api.js.

import { state } from './state.js';
import { apiSelectPptxFile, apiAnalyzePptx } from './api.js';
import { showToast } from './components.js';
import { renderTable } from './table.js';
import { setOptionsEnabled } from './options.js';

/** Wire up the file picker and Analyze button. Called once on init. */
export function initAnalyze() {
  const selectBtn = document.getElementById('select-btn');
  const analyzeBtn = document.getElementById('analyze-btn');

  if (selectBtn) selectBtn.addEventListener('click', onSelectFile);
  if (analyzeBtn) analyzeBtn.addEventListener('click', onAnalyze);
}

/** Open the native .pptx picker and store the chosen path. */
async function onSelectFile() {
  try {
    const path = await apiSelectPptxFile();
    if (!path) return; // user cancelled
    state.pptxPath = path;
    document.getElementById('pptx-path').value = path;
    document.getElementById('analyze-btn').disabled = false;
    // A new file invalidates any prior analysis.
    document.getElementById('compress-btn').disabled = true;
    state.analysis = null;
  } catch (e) {
    showToast('Could not open file: ' + e, 'error');
  }
}

/** Run analysis on the selected file and render the table. */
async function onAnalyze() {
  if (!state.pptxPath) return;
  try {
    // TODO: show a loading state while the backend inventories the archive.
    const result = await apiAnalyzePptx(state.pptxPath);
    if (result && result.error) {
      showToast(result.error, 'error');
      return;
    }
    state.analysis = result;
    renderTable(result);
    setOptionsEnabled(true); // Options panel becomes usable after analysis.
    document.getElementById('compress-btn').disabled = false;
  } catch (e) {
    showToast('Analysis failed: ' + e, 'error');
  }
}
