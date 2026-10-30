### refactor(providers): one wire-contract table for custom node types

The "no choices in OpenAI response" bug occurred because what wire a custom node
type speaks lived in two separate places that could drift: `compatibleNodeLane`
knew anthropic-compatible nodes dial `/messages`, while the format machinery
still treated them as Chat Completions.

This collapses the contract into `providers.CompatibleNodeWires`: one entry per
node type declaring its lane and format together. Detection
(`isCompatibleNodeProvider`), the lane (`compatibleNodeLane`), and the config
stamping (`getProviderConfig` via `compatibleNodeFormat`) all read that table,
ensuring lane and format remain in sync.

Three test suites pin the table:
- providers: lane/format coherence per entry.
- chat: lane, format, and `getProviderConfig` stamping asserted per node type.
- dashboard: `nodeIDPrefixes` covers exactly the wire-table keys.
