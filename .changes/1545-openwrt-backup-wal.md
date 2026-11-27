### OpenWrt: keep the SQLite WAL together with the database in `9router-go-backup`

`restore` overwrote `data.sqlite` and left the replaced database's `-wal` file beside it. SQLite replays whatever WAL it finds next to a database, so a restore could replay frames belonging to a different database — the two failure shapes are `database disk image is malformed` and rows silently reverting to an older state. In the other direction, `save` copied `data.sqlite` alone, so a gateway killed before checkpointing (OOM, watchdog, hard power cut) produced a backup missing its most recent committed transactions.

- `save` now copies an uncheckpointed `data.sqlite-wal` to `<file>-wal`, and removes a stale `<file>-wal` from an earlier save.
- `restore` deletes `data.sqlite-wal` and `data.sqlite-shm` belonging to the database it replaces, then writes the incoming file and puts a saved `-wal` back beside it. The `-shm` file is a shared-memory index, never data, and is still never copied.
- This matters because WAL is not optional here: `internal/db/client.go` sets `journal_mode = WAL`, which is persistent in the database header.

Verified with six new `scripts/openwrt-shell-harness.py` cases driving the real packaged script. Reverting `openwrt/9router-go-backup` gives **11/14**, failing exactly the three WAL assertions (`save carries an uncheckpointed -wal`, `restore drops the replaced database's -wal and -shm`, `restore brings a saved -wal back`); the branch gives **14/14**.

The harness `uci` stub learned to resolve `<config>.<section>.<option>` for configs other than `firewall`, which is what lets the backup script's `uci get 9router-go.main.data_dir` reach the temp data dir instead of `/opt/9router`.
