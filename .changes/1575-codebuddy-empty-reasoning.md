### fix(codebuddy): keep reasoning opt-in when reasoning_effort is empty

- `transformCodebuddyBody` no longer injects `reasoning_summary: "auto"` when
  `reasoning_effort` is an empty string. Upstream `decolua/9router` treats
  reasoning as opt-in (`else if (eff)`), so an empty value is falsy and must
  not trip Tencent's content filter — the same class of failure as upstream
  #2071, where forced reasoning params made CodeBuddy return a content error.
- Added a table-driven regression test covering the empty-string case, next
  to the existing no-effort / `none` / `off` / active-effort cases.
- Verified with `go test ./internal/proxy/executor/ -count=1`.
