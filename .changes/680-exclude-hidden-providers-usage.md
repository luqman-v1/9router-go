### Fixed
- Excluded registry providers marked `Hidden: true` from the `GET /api/usage/providers` list so internal/system providers (like `mimo-free`) do not appear in the analytics provider dropdown (fixes #680, upstream `decolua/9router#dcc6a205` parity).
