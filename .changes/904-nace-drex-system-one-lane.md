<<<<<<< HEAD
### Register the Nace Drex System One lane

Nace Drex serves decision models on the System One protocol from `console.nace.ai` — the same lane shape as v1m: a state plus typed questions (`noul` / `choice` / `score`) in, calibrated probabilities out. The lane forwards the body and the JSON reply untouched, so a score answer's `legend` array and every `confidence` reach the client as Drex returns them. `drex-max` also reads a top-level `images` array; the text-only `drex-v2.0` answers 422 when it is non-empty, so nothing rewrites images here (upstream v0.5.112, `f7f6f5e`; catalog verified live against `console.nace.ai/v1/models`).

- `providers.go`: `nace` rides the System One endpoint only; `BaseURL` stays empty so an empty base never resolves a chat connection, exactly as v1m.
- `registry_models.go`: `drex-max` and `drex-v2.0` in both the catalog (`ProviderModels`) and the model-kind map (`ProviderModelKinds`).
- `aliases.go`: `drex -> nace`, so `drex/<model>` reaches the lane.
- `validate.go`: `validateV1M`'s body becomes a shared `validateSystemOneLane`, since both lanes prove a key with one single-token noul evaluation and only a 401/403 means an invalid key.

Verified: `TestAggregatorProviders/nace` pins the registration (catalog, alias, empty chat base), and the `providers` and `dashboard` packages pass.
=======
### Register the Nace Drex System One lane

Nace Drex serves decision models on the System One protocol from `console.nace.ai` — the same lane shape as v1m: a state plus typed questions (`noul` / `choice` / `score`) in, calibrated probabilities out. The lane forwards the body and the JSON reply untouched, so a score answer's `legend` array and every `confidence` reach the client as Drex returns them. `drex-max` also reads a top-level `images` array; the text-only `drex-v2.0` answers 422 when it is non-empty, so nothing rewrites images here (upstream v0.5.112, `f7f6f5e`; catalog verified live against `console.nace.ai/v1/models`).

- `providers.go`: `nace` rides the System One endpoint only; `BaseURL` stays empty so an empty base never resolves a chat connection, exactly as v1m.
- `registry_models.go`: `drex-max` and `drex-v2.0` in both the catalog (`ProviderModels`) and the model-kind map (`ProviderModelKinds`).
- `aliases.go`: `drex -> nace`, so `drex/<model>` reaches the lane.
- `validate.go`: `validateV1M`'s body becomes a shared `validateSystemOneLane`, since both lanes prove a key with one single-token noul evaluation and only a 401/403 means an invalid key.

Verified: `TestAggregatorProviders/nace` pins the registration (catalog, alias, empty chat base), and the `providers` and `dashboard` packages pass.
>>>>>>> 1b5688b (docs(changes): fragments for the Nace and NVIDIA NIM ports)
