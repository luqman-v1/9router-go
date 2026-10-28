### Added
- Upstream parity (`decolua/9router` commit `da1756a8`): added advanced model search filter utility (`web/src/lib/modelSearchFilter.ts`) supporting:
  - Negation tokens via `!keyword` or `-keyword` (e.g. `claude !haiku`, `!embed`).
  - Regular expressions via `/pattern/flags` or `/pattern/` (e.g. `/gpt-[45]/`).
  - Multi-term conjunct queries with case-insensitive matching across candidates (`name`, `id`, `value`, `alias`, `provider`).
- Integrated advanced filtering into `web/src/components/combos/pickerData.ts` for filtering combo lists and grouped provider models.
- Enhanced `ModelPickerModal.svelte` with active filter chips (`regex`, `!exclude`) and clear search action.
- Added test suites in `web/src/lib/modelSearchFilter.test.ts` and `web/src/components/combos/pickerData.test.ts`.
