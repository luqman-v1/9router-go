### fix(claude): upstream flow parity for bare model resolution, live catalog, and CLI fingerprint

- Route bare `claude-*` model requests to Claude OAuth connection when no Anthropic API key connection is active (upstream `decolua/9router` commit `29f3db11` parity).
- Add live `/models` catalog enumeration for `claude` and `anthropic` connections in dashboard with OAuth beta header and display name parsing (upstream commit `43bc940e` parity).
- Update base Claude CLI fingerprint floor from `2.1.280` to `2.1.322` and update static model catalogs to include `claude-fable-5-2`, `claude-sonnet-5-5`, and `claude-haiku-5-5` (upstream commits `12e7930e` and `8aef7480` parity).
