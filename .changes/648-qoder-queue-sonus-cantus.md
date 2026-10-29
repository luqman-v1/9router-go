### Qoder: queued models, Sonus/Cantus, capabilities, stable machine id

- 10605 (`isQueued` / `RESOURCE_LIMIT`) is treated as a transient model-queue signal instead of a billing block: the executor holds the request open, polls the `/algo/api/v2/service/ask/queue/status` endpoint, and replays with a fresh COSY signature once the model is ready (budget 10 min, `QODER_MODEL_QUEUE_MAX_WAIT_MS` to raise; 503 `rate_limit_error` when the budget runs out). The double-encoded queue payload (`code`/`isQueued`/`queueType`/`retryAfterSeconds` behind nested JSON strings) is parsed accordingly.
- Static `model_config` fallback for Sonus (`smodel`) and Cantus (`cmodel`) — absent from many per-account catalogs but accepted by chat; a live catalog entry always wins. Both ids join the model registry and the picker.
- Qoder/qoder-cn capability table restored (vision/reasoning per concrete id, opaque ids no longer fall through as text-only).
- Machine id derives stably from the user id when the connection stores none, so a PAT connection keeps its queue position instead of rejoining the back of the queue every request.
