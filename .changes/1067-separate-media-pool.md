### feat(connections): separate media pools from the LLM pool per connection

- Allow connections to be toggled independently for media generation (image, audio, video) without disabling text chat routing.
- Media routing checks `isMediaActive` / `disabledKinds` to select candidate connections.
- Dashboard media views update media pool eligibility independently.
