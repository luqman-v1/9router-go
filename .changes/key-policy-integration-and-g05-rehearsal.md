### test: cover the key-policy gate end to end, and rehearse a settings import

**Key policy.** The dashboard's per-key model access and the routing gate it
feeds were covered separately, so nothing asserted that the two agree. The new
integration suite drives the real router: an allowlisted key lists exactly the
models it was granted, an alias spelling reaches the same grant as the canonical
name, a combo does not silently widen it, and an access denial answers with the
structured message the dashboard renders rather than a bare 403.

**G05 rehearsal.** A settings import is the one operation that can destroy a
working install, and it had no test that ran it against a corpus. The rehearsal
builds a synthetic snapshot, imports it into a fresh data directory, and asserts
the target is unchanged when the import fails, the round trip preserves the
fields the export writes, and the report the caller receives carries no secrets.
The runbook records what a failed import leaves behind, so an operator reading it
mid-incident does not have to infer that from the code.
