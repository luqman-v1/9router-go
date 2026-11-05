### feat(headroom): add autostart option to launch Headroom proxy when 9router boots
- **Autostart Support**:
  - Add `HeadroomAutostart` setting in `internal/db/settings.go` and `internal/db/settings_parse.go`.
  - Automatically boot local loopback Headroom proxy in `startHeadroomOnBoot` inside `internal/app/server.go` during 9router-go startup when enabled (spawned detached, decoupled from `HeadroomEnabled`).
  - Expose **Autostart with 9router** toggle under Headroom's **Configure** section and inside the Headroom Setup/Manage modal in `web/src/components/TokenSaverView.svelte` (gated on Headroom installation).
  - Add test coverage in `internal/db/settings_test.go`.
  - Add retry polling wait in `internal/integration/bootfx/boot_test.go` to stabilize async usage history flush during shutdown.
