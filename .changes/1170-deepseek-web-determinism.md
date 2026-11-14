### Fixed

- DeepSeek web tool rendering is now byte-stable: the bound schema text and the nonce-stripped arguments use the deterministic marshaling helper, so identical requests produce identical bytes and keep prefix-cache hits (#1167).
