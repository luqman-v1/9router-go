### Fixed

- **Bounded linear estimation fallback for adversarial token counting** — Prevents CPU stalling on pathological unicode inputs during token estimation by falling back to bounded linear counting (upstream OmniRoute #19650 parity).