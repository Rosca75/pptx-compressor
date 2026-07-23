# BUILD — doc-anonymiser

## Ground rules

- `CLAUDE.md` is non-negotiable; re-read it before every phase.
- Every phase ends with: passing build (`go build ./...` + `wails build` where the environment allows), passing tests (`go test ./...`), and one named commit.
- No dependency is added unless it appears in the dependency table below.
- Only `ollama/client.go` talks to Ollama; `engine/*` consumes the `LLM` interface. Only `static/api.js` calls Go bound methods; `static/state.js` is the only frontend state holder.
- The application performs no network I/O except loopback HTTP to Ollama. Re-verify this at every phase touching networking.
- Wails **v2.10.x** — do not use Wails v3 idioms. Go **1.23.x** — standard library first.
- Engine functions never touch user-chosen filesystem paths; bytes in, bytes out.

## Dependency table

| Dependency | Version (pinned) | Introduced in phase | Justification |
|---|---|---|---|
| Go toolchain | 1.23.x | bootstrap | language runtime |
| github.com/wailsapp/wails/v2 | v2.10.x | bootstrap | desktop shell, JS↔Go bridge, native dialogs |
| Wails CLI (CI only) | v2.10.x | bootstrap | build tooling; coupled with the library version — CI asserts the pair |
| Go standard library (`encoding/csv`, `regexp`, `archive/zip`, `net/http`, `encoding/json`) | — | throughout | CSV parsing, PII regexes, zip export, Ollama client — zero third-party runtime dependencies |
| Ollama (external, optional, user-installed) | HTTP API as of 2026 (`/api/tags`, `/api/chat` with `format:json`) | Phase 5 | optional LLM features; never bundled, never required |

No other dependency is authorised. If a phase appears to need one, stop and update this table + CLAUDE.md §7 first.

## Performance budgets

| Operation | Budget | Measured in phase |
|---|---|---|
| Deterministic pipeline (passes 1+2+4) over 50 documents × 50 KB | ≤ 5 s on a reference corporate laptop CPU | Phase 4 |
| CSV import → markdown-table render, 10 000 rows × 20 cols | ≤ 2 s | Phase 2 |
| UI responsiveness during pipeline | no frozen window: long operations run in goroutines with Wails event progress updates | Phase 8 |
| LLM deep-scan per document | soft budget 30 s per 50 KB document with the default 3B model; surfaced in UI as per-file progress, cancellable | Phase 9 |

If the deterministic budget is breached, profile before optimising; the usual culprit is per-call regex compilation — regexes are compiled once at package init (CLAUDE.md §6).

## Phase 1 — Document model and ingestion

### Goal
Solid, fully tested ingestion of `.txt`, `.csv`, `.md` into the `Document` model, including CSV round-trip fidelity.

### Activities
1. Flesh out `engine/document.go`: line-ending normalisation (CRLF→LF), UTF-8 validation with clear error for other encodings (actionable message naming the file), format detection by extension only.
2. `engine/csvmd.go`: CSV → `Grid [][]string` (handle quoted fields, embedded commas/newlines, ragged rows padded with empty cells + a warning collected on the Document); Grid → markdown table (escape pipes); Grid → CSV writer for round-trip export.
3. Document-level warnings list (ragged CSV, empty file, very large file > 10 MB → warn, still process).
4. Extend `testdata/` with edge-case fixtures: quoted CSV with embedded newline, md with code fences (content inside fences is still text to anonymise — no special casing in v1, but the fixture pins today's behaviour), French accented text.

### Unit tests
- Table-driven tests: each format loads, markdown output matches golden strings, CSV round-trips byte-identically for well-formed input, ragged CSV produces the expected warning, invalid UTF-8 fails with the expected error message fragment.

### Definition of done
- Build and tests pass.
- Commit: `feat(engine): document model and txt/csv/md ingestion`

## Phase 2 — Deterministic PII pass

### Goal
Pass 1 of the pipeline: regex detection of hard PII with placeholder emission, mirroring the notebook's deterministic pre-pass.

### Activities
1. `engine/pii.go`: one documented regex per PII category — email, international + LU/FR/DE/BE phone formats, IBAN (with checksum validation in Go to kill false positives), EU VAT numbers, Luxembourg 13-digit matricule, URLs, monetary amounts (advanced level only), ISO and written dates (advanced level only). Each regex documented with match / no-match examples.
2. `engine/registry.go`: the placeholder registry — `Assign(category, original) placeholder` returning a stable `[CATEGORY_N]`; case-insensitive lookup for re-occurrences; export to sorted mapping (original, placeholder, category, occurrence count).
3. Detection returns spans (start, end, category, original) rather than mutating text — replacement is a separate function applying spans longest-first, non-overlapping. This span model is reused by every later pass.
4. Wire level-awareness: which PII categories fire at `soft`/`medium`/`advanced` per CLAUDE.md §5. Measure the CSV/pipeline budgets from the performance table.

### Unit tests
- Per-category positive and negative cases (e.g. IBAN checksum rejects a mutated IBAN; matricule of 12 digits does not fire).
- Registry stability: same email in two documents → same placeholder.
- Overlap resolution: an email inside a URL resolves deterministically.

### Definition of done
- Build and tests pass; budgets from Phase 2 rows measured and recorded in a comment in `pii_test.go`.
- Commit: `feat(engine): deterministic PII pass and placeholder registry`

## Phase 3 — Known entities, variants, allowlist

### Goal
Pass 2: engagement entities with the notebook's variant expansion, plus the allowlist that overrides everything.

### Activities
1. `engine/entities.go`: entity model (`Category`, `Canonical`, `Variants []string`); variant expansion for person names (First Last → "F. Last", "Last", "First", "F.Last", hyphen/space swaps) and organisation names (with/without legal suffixes S.A., S.à r.l., SA, GmbH, Ltd — documented list); manual variants addable.
2. `engine/allowlist.go`: default seed list (regulators CSSF, ECB, EBA; common methodologies; country names) + user additions; allowlist check applied before any replacement in every pass.
3. `custom_patterns`: user-supplied regexes validated at entry (compile check with actionable error surfaced later in UI).
4. Longest-match-first replacement across all entity variants, reusing the span model from Phase 2; word-boundary anchoring so "Alten" does not fire inside "Altenberg".

### Unit tests
- Variant expansion golden tests (including French particles: "Jean de la Croix").
- Allowlist beats entity: an allowlisted term listed as an entity is not replaced.
- Boundary tests: substring non-matches, punctuation-adjacent matches.

### Definition of done
- Build and tests pass.
- Commit: `feat(engine): known-entity pass, variant expansion, allowlist`

## Phase 4 — Pipeline orchestration, post-pass, report

### Goal
End-to-end deterministic pipeline over a set of documents, with cross-document consistency and statistics — the app's full "no-Ollama" capability.

### Activities
1. `engine/pipeline.go`: `Run(docs, entities, level, allowlist) Results` executing passes 1 → 2 → (LLM slot, skipped when nil) → 4; pass 4 re-applies the full registry to every document so late-discovered entities are replaced everywhere.
2. `engine/report.go`: per-document and aggregate counts by category, warnings, duration; serialisable to JSON for the UI and for export.
3. `engine/simplereplace.go`: ordered manual find→replace rules (literal, case-sensitive toggle) applied as a final optional pass; recorded in the report.
4. Measure the 50-document budget; record the number in a test comment.

### Unit tests
- Two-document consistency: entity found in doc A is replaced in doc B by pass 4.
- Level matrix: the same fixture at soft/medium/advanced produces the expected differing outputs (golden files).
- Simple-replace ordering and case toggle.

### Definition of done
- Build and tests pass; budget measured ≤ 5 s.
- Commit: `feat(engine): pipeline orchestration, post-pass, reporting`

## Phase 5 — Ollama client and LLM passes (headless)

### Goal
The complete optional-LLM layer, fully tested against a mocked HTTP server — no UI yet.

### Activities
1. Complete `ollama/client.go`: `Probe()` (exists from bootstrap), `ListModels()`, `Chat(model, systemPrompt, userPrompt) (string, error)` using `POST /api/chat`, `"stream":false`, `"format":"json"`, generous timeout (120 s) with context cancellation.
2. `Discover(doc)` — the Phase-A prompt: extract `client_names`, `project_names`, `pwc_internal_names`, `person_names` from a representative document; strict-JSON prompt with the exact keys; tolerant JSON parsing (strip accidental code fences) with actionable error on malformed output.
3. `DeepScan(doc, knownEntities, allowlist)` — the residual pass: propose missed entities; apply the **hallucination filter** (drop any proposal whose exact string is absent from the source text) and the allowlist before returning.
4. Multi-file discovery: run per file, merge and deduplicate categories.
5. Wire the LLM slot into `engine/pipeline.go` behind the `LLM` interface; nil interface = pass skipped, report notes "LLM pass: skipped (Ollama not available)".

### Unit tests
- `httptest.Server` mocks: happy path, Ollama down (connection refused → Available=false, clean detail string), `/api/chat` 404 (old Ollama → the pinned "too old" message), malformed JSON reply, hallucination filter drops fabricated entities, allowlist filters proposals.

### Definition of done
- Build and tests pass with zero real network calls in tests.
- Commit: `feat(ollama): client, discovery, deep-scan with hallucination filter`

## Phase 6 — UI shell, import screen, settings

### Goal
The Wails frontend skeleton: wizard navigation, document import via native dialog and drag-drop, settings.

### Activities
1. `state.js`: full state shape (documents, entities, allowlist, level, ollamaStatus, currentStep, results) with a subscribe/notify store; `api.js`: bound-method wrappers only.
2. Wizard chrome in `index.html` + `views/`: step header (1 Import → 2 Configure → 3 Entities → 4 Run → 5 Export), navigation guards (cannot advance without documents).
3. Import view: native multi-file dialog (Wails runtime `OpenMultipleFilesDialog` filtered to txt/csv/md) and drag-drop onto the window; imported list with per-file format badge, size, warnings, remove button; markdown preview pane (CSV shown as rendered table).
4. Settings view: anonymisation level radio (default medium), Ollama port override (loopback locked), model dropdown populated from `ListModels()`, "re-probe" button.
5. Greyed-state mechanics: a single `state.ollama.available` flag drives disabled attributes + tooltips on every LLM control.

### Unit tests
- Pure-JS logic extracted from views (state transitions, navigation guards) tested with Go-served static tests is out of scope; instead: keep views logic-free and test the store — add `static/state.test.js` runnable via `node --test` in CI (node is present on runners; this is a dev-time check, not an npm dependency).

### Definition of done
- `wails build` passes; app runs the wizard shell; import of the three formats works with preview.
- Commit: `feat(ui): wizard shell, import, settings`

## Phase 7 — Entities screen: discovery, review, allowlist, manual mode

### Goal
The heart of the UX: the notebook's interactive review cells become a proper screen, fully usable with or without Ollama.

### Activities
1. Discovery trigger: pick representative file(s) from the imported list, run `Discover` per file with progress events; disabled (tooltip) without Ollama.
2. Review table per category: accept / deny / edit each item; add-item row for manual entry (this IS the whole flow in no-Ollama mode); show expanded variant count per entity with an expandable variant list, editable.
3. Allowlist editor: seeded defaults visible, session additions, per-term delete.
4. Custom patterns editor: regex input with live compile validation and a sample-match tester against the loaded documents.
5. Persist all of it in `state.js`; `api.js` gains `RunDiscovery`, `ExpandVariants` calls.

### Unit tests
- Go side: `ExpandVariants` bound-method adapter test; discovery merge/dedupe test (multi-file).
- JS store tests: accept/deny/edit reducers.

### Definition of done
- Manual-only entity definition path works end to end without Ollama; discovery path works with Ollama running locally (manual verification note).
- Commit: `feat(ui): entity discovery, review, allowlist, custom patterns`

## Phase 8 — Run screen: pipeline execution, progress, results review

### Goal
Execute the pipeline from the UI with live progress, then let the user verify the result before export.

### Activities
1. `app.go` `RunPipeline` bound method executing in a goroutine; progress via Wails events (per-file, per-pass); cancel button (context cancellation, honoured between files and mid-LLM call).
2. Results view: side-by-side original/anonymised preview per document with replaced spans highlighted (category-coloured `<mark>` classes); per-document counts; aggregate report panel.
3. "Something missed?" loop: from the results view, add an entity or simple-replace rule and re-run pass 2+4 only (fast path — no LLM re-run) to refresh outputs.
4. Simple-replace editor (ordered rules) on this screen, mirroring notebook Cell 8.

### Unit tests
- Go: cancellation test (pipeline stops between documents); fast-path re-run test (new entity applied without full pipeline).
- JS: highlight-rendering function unit test (span → HTML, escaping).

### Definition of done
- Full run works on the fixture set with UI progress; window never freezes (verified with a 50-file synthetic batch).
- Commit: `feat(ui): pipeline run, progress, results review`

## Phase 9 — Export screen

### Goal
The "several options to extract" requirement: every useful egress path, all via explicit user action.

### Activities
1. Per-document save: native save dialog; md documents export as `.md`; CSV-origin documents offer `.csv` (round-trip through the anonymised Grid) or `.md`; txt-origin as `.txt` or `.md`.
2. Export all: single zip via `archive/zip` + save dialog (`<n>_anonymised.zip`), preserving filenames with an `_anon` suffix.
3. Copy to clipboard: per-document button (Wails clipboard runtime).
4. Entity mapping export: CSV and JSON of the registry (original ↔ placeholder ↔ category ↔ count) behind a confirmation dialog warning it is the re-identification key.
5. Report export: the Phase-4 JSON report plus a human-readable markdown summary.
6. Session save/load (`engine/session.go`): explicit save of entities + allowlist + registry + settings to a `.anonsession.json` with the sensitivity warning; load restores state for a follow-up batch. Measure and surface LLM per-file timing in the report (soft budget from the table).

### Unit tests
- Zip contents test; CSV round-trip export equals anonymised Grid; session save→load equality; mapping export golden file.

### Definition of done
- All export paths function on Windows (primary manual check) and pass automated tests.
- Commit: `feat(export): file, zip, clipboard, mapping, report, session`

## Phase 10 — Hardening and polish

### Goal
Production feel: errors, edge cases, first-run experience.

### Activities
1. Error surfaces: every bound method returns structured errors rendered as dismissible banners (what failed / expected / how to fix), never silent console logs.
2. Empty states and first-run hints on each screen; keyboard shortcuts (Ctrl+O import, Ctrl+E export).
3. Large-file handling: > 10 MB warning path exercised; preview virtualised or truncated with notice (render first 5 000 lines, full content still processed).
4. Ollama resilience: mid-run Ollama crash degrades that pass with a report warning instead of failing the batch.
5. Final `go vet`, dead-code sweep, comment-quality pass per CLAUDE.md §6.

### Unit tests
- Error-message format tests; large-file truncated-preview test; mid-run LLM failure degradation test (mock kills the server after file 1).

### Definition of done
- Build and tests pass.
- Commit: `chore: hardening, error surfaces, polish`

## Manual test matrix (pre-release)

| # | Scenario | Steps | Expected | Platform |
|---|---|---|---|---|
| 1 | Fresh-machine run, no Ollama | Download release zip, unzip, run exe (SmartScreen: More info → Run anyway) | App opens; Ollama badge grey; LLM controls disabled with tooltip; deterministic flow fully usable | Windows 11 (corporate laptop) |
| 2 | End-to-end without Ollama | Import 1 txt + 1 csv + 1 md → medium level → manual entities (1 client, 2 persons) → run → review highlights → export zip + mapping CSV | Consistent placeholders across all 3 files; CSV re-exports as valid CSV; zip opens | Windows 11 |
| 3 | End-to-end with Ollama | Install Ollama, `ollama pull qwen2.5:3b-instruct`, re-probe in settings → discovery on representative file → review/deny one false positive → run with deep-scan → export | Badge green; discovery table populated; denied item absent from output; deep-scan additions pass hallucination filter | Windows 11 |
| 4 | Levels differ | Same file at soft, then advanced | Dates/locations untouched at soft, replaced at advanced | Windows 11 |
| 5 | Allowlist honoured | Add "CSSF" as entity AND allowlist | "CSSF" never replaced | Windows 11 |
| 6 | Cancellation | Start 20-file run with deep-scan, cancel at file 3 | Run stops promptly; partial results marked; app stable | Windows 11 |
| 7 | Session round-trip | Save session, restart app, load session, import new file, run | Same placeholders as previous session for same entities | Windows 11 |
| 8 | Linux sanity | Repeat scenario 2 | Same behaviour | Ubuntu 24.04 |
| 9 | French document | Import French md with accented names and FR phone formats | Detection works; variants correct for particles | Windows 11 |

## Release phase

1. Verify manual test matrix complete; fix and re-run any failure.
2. Update README.md (screenshots, Ollama section verified against the shipped default model).
3. Tag `v1.0.0`; confirm `release.yml` produces Windows + Linux zips with README-FIRST.txt; macOS job may fail — acceptable.
4. Download the Windows zip on the corporate laptop and re-run scenario 1 from the actual release artefact.

## Deferred to v2

- DOCX / PDF / PPTX / XLSX input conversion (the nb1 converters — requires evaluating pure-Go document libraries against the zero-CGo rule; PDF extraction in pure Go is the hard one).
- UI internationalisation (French UI).
- ONNX NER model via ORT Web (the P4 fallback) if local-LLM NER quality is insufficient.
- Batch profiles / saved level presets per engagement type.
- Content inside markdown code fences treated specially (skip or force-scan toggle).
- De-anonymisation mode (apply a mapping file in reverse to restore originals).
- Automatic representative-file suggestion for discovery (currently user-picked).
