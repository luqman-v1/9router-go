### Fixed
- Raw Responses API streams (`/v1/responses`) that end after `response.completed`, `response.done` or `response.incomplete` are now treated as terminal, so the gateway no longer appends a Chat Completions `network_error` frame to a completed stream (#1073).
