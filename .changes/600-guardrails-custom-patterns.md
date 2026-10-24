### Added
- Custom regex pattern detection and redaction in Guardrails (`internal/guardrails/custom.go`) (#599).
- Supported user-defined regular expressions per policy with custom entity `"CUSTOM_PATTERN"`, configurable actions (block, mask, warn, log_only), and optional custom replacement strings.
- Added API validation in `internal/handlers/dashboard/guardrails.go` ensuring custom regexes compile cleanly before saving.
- Added custom pattern editor in `web/src/components/security/SecurityPanel.svelte`.
- Comprehensive backend unit tests in `internal/guardrails/custom_test.go` and `internal/handlers/dashboard/guardrails_test.go`.
