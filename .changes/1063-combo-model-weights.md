### feat(combos): per-model weights for round robin
- Add support for per-model integer weights in combo routing configuration so traffic is routed proportionally using smooth weighted round-robin (Nginx algorithm) instead of an even split.
- Models in `combo.Models` list can carry an optional integer `weight` property (defaulting to 1 if unset), as well as via `comboStrategies[name].weights` in settings.
- Handles zero-weight models safely by excluding them from the rotation head while preserving them at the tail of the fallback chain as last-resort fallbacks.
- When all weights are zero or unset, rotation defaults to legacy uniform round-robin distribution with zero regression.
- Supports capacity adapter prepending without disrupting or shifting the weighted rotation cycle.
- Verified with comprehensive unit tests covering proportional distribution, legacy parity, zero weights, sticky limits, mid-turn reuse, and database persistence.
