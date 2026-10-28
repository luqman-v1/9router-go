### Bulk custom-model parser: colon and dot are valid id characters, not separators

The paste parser (`web/src/lib/bulk-models.ts` `SEPARATORS`) and the server-side
`invalidModelIDReason` guard (`internal/handlers/dashboard/model_id.go`) both
split on whitespace, commas and semicolons only. A colon (`:`) or a dot (`.`)
inside a pasted token is part of the model id, not a boundary — so a paste like
`model1:free,modelX:paid` correctly produces two ids (`model1:free` and
`modelX:paid`) split on the comma, with the colon kept inside each id.

A slash (`/`) remains a reserved character: it is the `<prefix>/<id>` address
separator and the kv-store key delimiter, so `model2/provModel:xvertor` is
correctly refused as one id. The operator should paste each half separately.

