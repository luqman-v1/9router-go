### Android gateway binary is published as a release asset

The Android app can replace its gateway without installing a new APK: it
downloads `9router-go-android-arm64`, verifies it against `SHA256SUMS.txt`,
writes it to `<DATA_DIR>/bin/lib9router.so`, and the next gateway start runs
it. That path has existed in the app since the APK shipped, and it has never
worked — not because the app is wrong, but because **no release has ever
carried the asset**. Every release published the six desktop binaries and
nothing for Android, so the app's own check could only ever report "the
<channel> channel has no gateway binary for this device".

The pipeline built such a binary already, for the APK to bundle as
`lib9router.so`. It was just never named as a release artifact:

- `Makefile`: `android-arm64` joins `CROSS_TARGETS`, with a
  `cross-android-arm64` target naming its output `9router-go-android-arm64`.
  The name is load-bearing — the app matches the release asset by that exact
  string. No NDK is involved: the only published ABI is arm64, which builds
  with `CGO_ENABLED=0` and needs no external linker.
- `.github/workflows/release.yml`: `9router-go-android-arm64` is added to the
  `files:` list, in the same PR as the target that produces it. A `files:`
  entry with no build behind it fails the release, so the two must not be
  separated.
- `.github/workflows/ci.yml`: the `cross` job now asserts the artifact's
  architecture (`aarch64`) and that `SHA256SUMS.txt` covers it. A file named
  `android-arm64` holding anything else installs on a phone and then fails to
  exec, which is precisely the failure those two assertions exist to catch
  before a release.

Verified locally: `make cross-android-arm64` produces an ELF 64-bit ARM aarch64
PIE executable in 3.4 s, and `sha256sum -c SHA256SUMS.txt` passes with the new
entry. On a Redmi 4 (Android 10, arm64-v8a) the published `linux-arm64` asset
was tried first as a substitute and rejected: it boots and serves `/health`,
but it has no `/etc/resolv.conf` to read and so fails every outbound request
against `[::1]:53`. That is the difference this asset carries rather than a
naming detail — the Android build is the one a phone can actually run.

An Android gateway still cannot resolve DNS on its own; the app supplies
`DNS_SERVERS` from the active network's link properties when it spawns it.
