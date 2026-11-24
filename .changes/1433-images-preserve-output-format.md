### fix(images): preserve explicit output_format in image generation

Preserve client-specified `output_format` on `/v1/images/generations` payloads and forward it to upstream image providers.

Upstream parity: diegosouzapw/OmniRoute#21098 (merged commit a152ed81). Closes #1425.
