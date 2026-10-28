### Fixed — Provider detail identity stays left-aligned

- Keep the provider icon, name, and connection count together on the left side of the provider detail header. The provider-wide toggle remains on the right.
- Verified with `cd web && bun run build`, `make vet-svelte`, and a browser smoke check on the OpenCode Free detail page.
