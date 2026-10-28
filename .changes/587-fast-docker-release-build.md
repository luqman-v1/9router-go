### Optimize release build and Docker multi-arch compilation

- In `Dockerfile`, use `--platform=$BUILDPLATFORM` for `web-builder` and `builder` stages with Buildx automatic multi-platform arguments (`ARG TARGETOS TARGETARCH`). Go is a native cross-compiler (`CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH`), eliminating slow QEMU CPU emulation for ARM64 builds (which previously took ~13 minutes per release run).
- Add BuildKit cache mounts (`--mount=type=cache`) in `Dockerfile` for `/root/.bun/install/cache`, `/go/pkg/mod`, and `/root/.cache/go-build` to prevent redundant module downloads and object file recompilation.
- In `Makefile`, parallelize `make cross` across all 5 targets (`linux-amd64`, `linux-arm64`, `darwin-amd64`, `darwin-arm64`, `windows-amd64`) via `make -j` and dedicated target rules, reducing cross-compilation time from ~2.5m sequential to under 30s.
- In `.github/workflows/release.yml`, warm the Go module cache (`go mod download all`) with retries before running `make cross`, matching `ci.yml` and preventing concurrent module proxy timeouts.
