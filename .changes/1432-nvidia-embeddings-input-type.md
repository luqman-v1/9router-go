### feat(nvidia): add live embedding models and support input_type parameter

- Drop deleted `nv-embedqa-e5-v5` from NVIDIA registry and ProviderModels.
- Add the 8 live NVIDIA embedding models (`nvidia/nemotron-3.5-embed-8b`, `nvidia/nemotron-3.5-embed-1b`, `nvidia/nemotron-3-embed-1b`, `nvidia/nv-embedqa-mistral-7b-v2`, `nvidia/embed-qa-4`, `nvidia/llama-3.2-nemoretriever-1b-vlm-embed-v1`, `nvidia/llama-3.2-nv-embedqa-1b-v1`, `nvidia/nvclip`) with kind `embedding`.
- For asymmetric models (all except `nvclip`), inject `input_type` (default `query`) when omitted or invalid in embeddings forwarding.
