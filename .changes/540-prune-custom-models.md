### fix(db): prune custom models when provider node is deleted

- **Deleting a provider node orphaned its custom models.** `DeleteProviderNode`
  removed the node row and its connections, but left behind custom models in
  the `customModels` KV scope. A deleted node therefore continued to surface its
  models in the catalog.
- **Port upstream decolua/9router#3115 (commit a8f63f4d).** `DeleteProviderNode`
  now cleans up associated `customModels` rows atomically inside the same
  transaction as the node and connection rows.
- **Support both key separators and owning alias in row value.** Uses
  `splitCustomModelKey` to match pipe-separated (`<id>|<model>|...`) and
  slash-separated (`<id>/<model>`) keys, and inspects the JSON row value's
  authoritative `providerAlias` field so models whose value names the deleted
  node are also pruned. Malformed JSON falls back safely to key matching, while
  a row is left intact only when neither its key nor its value names the deleted
  node.
- **Atomic cleanup with checked connection errors.** Any failure during
  `providerConnections` or `customModels` deletion returns an error and aborts
  the transaction rather than committing a partial node deletion.
- **Missing node is a no-op.** Deleting an unknown node id returns without
  modifying any tables.
