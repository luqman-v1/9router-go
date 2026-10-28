### Usage breakdown sorting, a provider view, and bulk model removal

- Every column header in the Usage breakdown table is now sortable — click to sort on that column, click again to reverse — with the default left at requests-descending, so the initial view is unchanged; the active column shows its direction and each `th` carries `aria-sort`.
- The view selector gained **Usage by Provider**, which `/api/usage/stats` has always answered (`byProvider`) without the dashboard ever offering it; the redundant Provider column is dropped in that view.
- `byProvider` buckets now carry `lastUsed`, so the Last Used column has something to sort on for raw-history windows (`24h`, `<n>h`); a daily window stores no time of day, so the field is omitted rather than filled with midnight.
- **Remove all models** next to **Test all models** on a provider node, backed by `DELETE /api/providers/{id}/models`, undoes a `/models` import in one request instead of deleting hundreds of rows one at a time. It removes that node's custom models and the aliases pointing at them; another node's rows, and the catalog models that ship with the build, are left alone.
