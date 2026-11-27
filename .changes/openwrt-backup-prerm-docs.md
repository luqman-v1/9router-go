### fix(openwrt): back up uploads, clean the runtime copy on removal, refresh docs

- `9router-go-backup save` now archives `data_dir/uploads` to `<file>.uploads.tar`
  beside the database, and `restore` extracts it back. The database was never the
  only state: a restore onto a new router left the dashboard pointing at logos
  that were no longer there. `tar -C` keeps both paths literal, and a missing
  busybox `tar` skips the media with a message instead of failing the backup.
- `prerm` removes the runtime binary copy and the downloaded `cloudflared` from
  `data_dir`, and nothing else. The database, uploads and JWT secret survive a
  removal so a mistaken `opkg remove` is recoverable.
- `docs/OPENWRT.md` catches up with the healthcheck and LAN-firewall options the
  previous PR added: the cron entry is installed automatically, and `open_lan` is
  the only path that writes a firewall rule.

Two regression tests pin the contracts: `TestBackupCoversUploads` and
`TestPrermKeepsUserData`.
