// app.js — Entry point: imports all modules and initializes the application.
// This is the only file loaded by index.html via <script type="module">.

import { initAnalyze } from './analyze.js';
import { initOptions } from './options.js';
import { initCompress } from './compress.js';

/** Initialize all application modules once the DOM is ready. */
document.addEventListener('DOMContentLoaded', () => {
  initAnalyze();   // Wire up file picker + Analyze button.
  initOptions();   // Wire up the options panel controls.
  initCompress();  // Wire up Compress/Cancel + progress polling.

  // Render Feather icons (replaces <i data-feather="..."> with SVGs).
  if (typeof feather !== 'undefined') feather.replace();
});
