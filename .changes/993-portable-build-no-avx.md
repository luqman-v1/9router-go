### Build

- Release binaries are now always built with `CGO_ENABLED=0` and
  `GOAMD64=v1`, so they start on x86-64 CPUs that have SSE4.2 but no AVX/AVX2
  (Celeron/Pentium N-series, older Atom, some VMs).
- The previous `linux-amd64` artifact died with `SIGILL` before `main()` on a
  Celeron N4000. `CGO_ENABLED` defaulted to 1 whenever a C compiler was
  installed, and the gcc-compiled `runtime/cgo` shim (`x_cgo_getstackbound`)
  inherited the build host's `-march`, emitting AVX (`vmovq`/`vpinsrq`).
- `make build`, `make cross`, `build.sh`, and the release workflow now set both
  variables; either can still be overridden explicitly (`CGO_ENABLED=1`).
- Added a `9router-go-linux-amd64-noavx` release asset and `install-noavx.sh`
  (also `VARIANT=noavx` on `install.sh`) so the baseline build has a stable,
  explicit name to install. Every amd64 target uses the same settings, so the
  asset is byte-identical to the default one today; the separate name keeps the
  choice discoverable and scriptable.
- Verified on the AVX-less CPU: the binary reports `CGO_ENABLED=0 GOAMD64=v1`,
  is statically linked, and `version`, `--help`, `service status`,
  `start`/`status`/`stop`, and the dashboard (`GET /` → 200) all run without
  `SIGILL`.
