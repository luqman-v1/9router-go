package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// fakeBinary is the smallest thing that proves a binary member survives the
// round trip byte for byte. A real cross-compiled binary is 40 MB and would
// make this test the slowest thing in `go test ./...` for no extra coverage.
var fakeBinary = []byte("\x7fELF\x02\x01\x01\x00fake-router-binary\n")

// fakeArmBinary is a second, distinguishable payload: the alias tests need to
// prove that one binary is packed under several architecture names unchanged.
var fakeArmBinary = []byte("\x7fELF\x02\x01\x01\x00fake-arm-binary-different\n")

// assetsDir resolves the packaging directory from the test's own location, so
// the test does not depend on the working directory the runner picks.
func assetsDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs(filepath.Join("..", "..", "openwrt"))
	if err != nil {
		t.Fatalf("resolve assets dir: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "control.in")); err != nil {
		t.Fatalf("packaging assets missing at %s: %v", dir, err)
	}
	return dir
}

// targets is the architecture set the tests build: one alias pair sharing a
// binary plus one distinct binary, which is what the real Makefile produces.
func targets(t *testing.T) []archTarget {
	t.Helper()
	dir := t.TempDir()
	amd := filepath.Join(dir, "linux-amd64")
	arm := filepath.Join(dir, "linux-arm64")
	if err := os.WriteFile(amd, fakeBinary, 0o755); err != nil {
		t.Fatalf("write amd binary: %v", err)
	}
	if err := os.WriteFile(arm, fakeArmBinary, 0o755); err != nil {
		t.Fatalf("write arm binary: %v", err)
	}
	return []archTarget{
		{arch: "x86_64", bin: amd},
		{arch: "aarch64_generic", bin: arm},
		{arch: "aarch64_cortex-a53", bin: arm},
	}
}

// buildOnce produces a full feed tree in a temp dir and returns it.
func buildOnce(t *testing.T) string {
	t.Helper()
	dist := t.TempDir()
	if err := run("1.9.14", dist, assetsDir(t), "", targets(t)); err != nil {
		t.Fatalf("ipkbuild run: %v", err)
	}
	return dist
}

func TestFeedContents(t *testing.T) {
	dist := buildOnce(t)

	for _, tc := range []struct{ arch, binary string }{
		{"x86_64", "9router-go_1.9.14_x86_64.ipk"},
		{"aarch64_generic", "9router-go_1.9.14_aarch64_generic.ipk"},
		{"aarch64_cortex-a53", "9router-go_1.9.14_aarch64_cortex-a53.ipk"},
	} {
		archDir := filepath.Join(dist, tc.arch)
		entries, err := os.ReadDir(archDir)
		if err != nil {
			t.Fatalf("read output for %s: %v", tc.arch, err)
		}
		want := map[string]bool{
			tc.binary:                            false,
			"luci-app-9router-go_1.9.14_all.ipk": false,
			"Packages":                           false,
			"Packages.gz":                        false,
		}
		for _, e := range entries {
			if _, ok := want[e.Name()]; !ok {
				t.Errorf("%s: unexpected file %s", tc.arch, e.Name())
				continue
			}
			want[e.Name()] = true
		}
		for name, seen := range want {
			if !seen {
				t.Errorf("%s: missing %s", tc.arch, name)
			}
		}
	}
}

func TestPackagesIndexMatchesFiles(t *testing.T) {
	dist := buildOnce(t)
	archDir := filepath.Join(dist, "aarch64_cortex-a53")

	raw, err := os.ReadFile(filepath.Join(archDir, "Packages"))
	if err != nil {
		t.Fatalf("read Packages: %v", err)
	}
	// Packages.gz must decode to exactly the bytes opkg reads from the plain
	// file: a stale .gz is what makes a feed appear to have no update.
	gzRaw, err := os.ReadFile(filepath.Join(archDir, "Packages.gz"))
	if err != nil {
		t.Fatalf("read Packages.gz: %v", err)
	}
	zr, err := gzip.NewReader(bytes.NewReader(gzRaw))
	if err != nil {
		t.Fatalf("open Packages.gz: %v", err)
	}
	gunzipped, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("read Packages.gz: %v", err)
	}
	if !bytes.Equal(gunzipped, raw) {
		t.Fatalf("Packages.gz does not match Packages (%d vs %d bytes)", len(gunzipped), len(raw))
	}

	stanzas := indexStanzas(t, string(raw))
	if len(stanzas) != 2 {
		t.Fatalf("expected 2 stanzas, got %d", len(stanzas))
	}
	for _, s := range stanzas {
		file := s["Filename"]
		info, err := os.Stat(filepath.Join(archDir, file))
		if err != nil {
			t.Errorf("stanza for %s names missing file %s: %v", s["Package"], file, err)
			continue
		}
		// Size and digest in the index are what opkg verifies a download
		// against. A wrong Size truncates the install; a wrong SHA256sum is
		// reported as a corrupt package.
		if s["Size"] != strconv.FormatInt(info.Size(), 10) {
			t.Errorf("%s: Size %s, file is %d bytes", s["Package"], s["Size"], info.Size())
		}
		if len(s["SHA256sum"]) != 64 {
			t.Errorf("%s: SHA256sum %q is not a sha256 hex digest", s["Package"], s["SHA256sum"])
		}
		if s["Version"] != "1.9.14" {
			t.Errorf("%s: Version %q", s["Package"], s["Version"])
		}
		// opkg compares this field to DISTRIB_ARCH and nothing else, so it has
		// to be the directory's architecture, not a generic name.
		if s["Package"] == "9router-go" && s["Architecture"] != "aarch64_cortex-a53" {
			t.Errorf("9router-go Architecture = %q, want aarch64_cortex-a53", s["Architecture"])
		}
	}
}

// TestOneBinaryManyArchNames is the reason the -arch flag exists: the same file
// must be packaged under every OpenWrt name it is valid for, byte for byte, so
// an 18.06 ipq40xx device and a 19.07 armvirt device can both install it.
func TestOneBinaryManyArchNames(t *testing.T) {
	dist := buildOnce(t)

	var first []byte
	for _, arch := range []string{"aarch64_generic", "aarch64_cortex-a53"} {
		path := filepath.Join(dist, arch, "9router-go_1.9.14_"+arch+".ipk")
		blob, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", arch, err)
		}
		members, err := parseAr(blob)
		if err != nil {
			t.Fatalf("parse %s: %v", arch, err)
		}
		data := untarGz(t, members[2]["data.tar.gz"])
		bin := data["usr/libexec/9router-go/9router-go"]
		if !bytes.Equal(bin, fakeArmBinary) {
			t.Fatalf("%s: packed binary differs from the arm64 input", arch)
		}
		if first == nil {
			first = bin
		} else if !bytes.Equal(first, bin) {
			t.Fatalf("%s: binary differs from the other alias", arch)
		}
	}

	// The amd64 package must carry its own binary, not the arm one.
	blob, err := os.ReadFile(filepath.Join(dist, "x86_64", "9router-go_1.9.14_x86_64.ipk"))
	if err != nil {
		t.Fatalf("read x86_64 package: %v", err)
	}
	members, err := parseAr(blob)
	if err != nil {
		t.Fatalf("parse ar: %v", err)
	}
	bin := untarGz(t, members[2]["data.tar.gz"])["usr/libexec/9router-go/9router-go"]
	if !bytes.Equal(bin, fakeBinary) {
		t.Error("x86_64 package carries the wrong binary")
	}
}

func TestIpkArchiveLayout(t *testing.T) {
	dist := buildOnce(t)
	path := filepath.Join(dist, "aarch64_generic", "9router-go_1.9.14_aarch64_generic.ipk")

	blob, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read ipk: %v", err)
	}
	members, err := parseAr(blob)
	if err != nil {
		t.Fatalf("parse ar: %v", err)
	}
	if len(members) != 3 {
		t.Fatalf("expected 3 ar members, got %d", len(members))
	}
	if got := string(members[0]["debian-binary"]); got != "2.0\n" {
		t.Errorf("debian-binary = %q, want %q", got, "2.0\n")
	}

	control := untarGz(t, members[1]["control.tar.gz"])
	for _, name := range []string{"control", "postinst", "prerm", "conffiles"} {
		if _, ok := control[name]; !ok {
			t.Errorf("control.tar.gz missing %s", name)
		}
	}
	ctl := string(control["control"])
	for _, field := range []string{"Package: 9router-go", "Version: 1.9.14", "Architecture: aarch64_generic", "Depends: "} {
		if !strings.Contains(ctl, field) {
			t.Errorf("control lacks %q", field)
		}
	}
	if strings.Contains(ctl, "TODO") || strings.Contains(ctl, "example.com") {
		t.Errorf("control contains a placeholder:\n%s", ctl)
	}
	// The version must be substituted, or opkg cannot tell an upgrade from a
	// reinstall of the same version.
	if strings.Contains(string(control["postinst"]), "@VERSION@") {
		t.Error("postinst still contains the @VERSION@ placeholder")
	}
	if !strings.Contains(string(control["postinst"]), "1.9.14") {
		t.Error("postinst does not carry the package version")
	}

	data := untarGz(t, members[2]["data.tar.gz"])
	bin, ok := data["usr/libexec/9router-go/9router-go"]
	if !ok {
		t.Fatalf("data.tar.gz missing the package binary; has %v", keys(data))
	}
	if !bytes.Equal(bin, fakeArmBinary) {
		t.Error("packaged binary differs from the input")
	}
	for _, name := range []string{
		"usr/bin/9router-go",
		"usr/bin/9router-go-feed",
		"usr/bin/9router-go-backup",
		"usr/bin/9router-go-healthcheck",
		"etc/init.d/9router-go",
		"etc/config/9router-go",
	} {
		if _, ok := data[name]; !ok {
			t.Errorf("data.tar.gz missing %s", name)
		}
	}
	// The LuCI tree belongs to the other package, and shipping it twice would
	// make the two packages own the same file.
	for name := range data {
		if strings.Contains(name, "/luci/") {
			t.Errorf("main package must not ship LuCI files, found %s", name)
		}
	}
}

// TestSysupgradeKeepsDatabase pins the difference between a firmware flash
// that keeps the operator's gateway and one that wipes it.
//
// OpenWrt's sysupgrade preserves only what /etc/sysupgrade.conf and
// /lib/upgrade/keep.d/* name, plus changed conffiles. The default keep set is
// base-files-essential (a handful of /etc files) and a commented-out
// /etc/sysupgrade.conf, so nothing this package owns is preserved unless the
// package ships a keep.d drop-in. Before this test existed the package shipped
// none, and the database was discarded on every flash while the documentation
// promised otherwise.
func TestSysupgradeKeepsDatabase(t *testing.T) {
	dist := buildOnce(t)
	blob, err := os.ReadFile(filepath.Join(dist, "x86_64", "9router-go_1.9.14_x86_64.ipk"))
	if err != nil {
		t.Fatalf("read ipk: %v", err)
	}
	members, err := parseAr(blob)
	if err != nil {
		t.Fatalf("parse ar: %v", err)
	}
	data := untarGz(t, members[2]["data.tar.gz"])
	raw, ok := data["lib/upgrade/keep.d/9router-go"]
	if !ok {
		t.Fatalf("package ships no /lib/upgrade/keep.d/9router-go; sysupgrade would discard the database")
	}
	// Comments and blank lines are stripped by sysupgrade's own reader
	// (`sed -ne '/^[[:space:]]*$/d; /^#/d; p'`), so only the paths are compared.
	var kept []string
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		kept = append(kept, line)
	}
	for _, want := range []string{"/etc/9router-go", "/opt/9router/db", "/opt/9router/uploads"} {
		found := false
		for _, got := range kept {
			if got == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("keep.d does not name %s; paths are %v", want, kept)
		}
	}
	// The binary copy must NOT be in the keep list: it is ~52 MB, sysupgrade
	// stages its backup in /tmp (RAM), and postinst rewrites the copy anyway.
	// Listing it turns a firmware flash on a 128 MB router into an OOM event.
	for _, got := range kept {
		if strings.Contains(got, "/bin") {
			t.Errorf("keep.d must not name binaries (%s): the backup is staged in RAM", got)
		}
	}
	// A non-default data_dir cannot be covered by a static file, so postinst
	// has to add it to /etc/sysupgrade.conf. Without this the operator who moved
	// the database to a USB mount silently loses it on the next flash.
	postinst := string(untarGz(t, members[1]["control.tar.gz"])["postinst"])
	if !strings.Contains(postinst, "/etc/sysupgrade.conf") {
		t.Error("postinst never records a custom data_dir in /etc/sysupgrade.conf")
	}
	// /etc/config/9router-go must be a conffile. That stanza is what stops opkg
	// from overwriting the operator's settings on upgrade, and it is the same
	// mechanism that keeps them across a flash — so its absence is a silent
	// reset of port, data_dir and password on the next `opkg upgrade`, with no
	// error anywhere. Pinned here because nothing else reads that file.
	conffiles := string(untarGz(t, members[1]["control.tar.gz"])["conffiles"])
	if !strings.Contains(conffiles, "/etc/config/9router-go") {
		t.Errorf("conffiles does not list /etc/config/9router-go; an upgrade would overwrite the operator's settings (got %q)", conffiles)
	}
}

func TestLuciPackageIsArchAllWithLuaTree(t *testing.T) {
	dist := buildOnce(t)
	path := filepath.Join(dist, "x86_64", "luci-app-9router-go_1.9.14_all.ipk")
	blob, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read ipk: %v", err)
	}
	members, err := parseAr(blob)
	if err != nil {
		t.Fatalf("parse ar: %v", err)
	}
	ctl := string(untarGz(t, members[1]["control.tar.gz"])["control"])
	if !strings.Contains(ctl, "Architecture: all") {
		t.Errorf("luci package must be Architecture: all, got:\n%s", ctl)
	}
	// luci-compat is deliberately absent: it does not exist on 18.06, so
	// depending on it would make the package uninstallable on the oldest
	// release this supports.
	if !strings.Contains(ctl, "Depends: luci-base") {
		t.Errorf("luci package must depend on luci-base:\n%s", ctl)
	}
	if strings.Contains(ctl, "luci-compat") {
		t.Errorf("luci package must not depend on luci-compat (absent on 18.06):\n%s", ctl)
	}

	data := untarGz(t, members[2]["data.tar.gz"])
	// The Lua tree is what 18.06 runs. A JS-only tree (entry/ view/ menu.d/)
	// would render nothing there, so its absence is part of the contract.
	for _, name := range []string{
		"usr/lib/lua/luci/controller/9router-go.lua",
		"usr/lib/lua/luci/model/cbi/9router-go/main.lua",
		"usr/lib/lua/luci/view/9router-go/overview.htm",
		"usr/share/rpcd/acl.d/luci-app-9router-go.json",
	} {
		if _, ok := data[name]; !ok {
			t.Errorf("luci package missing %s; has %v", name, keys(data))
		}
	}
	if _, err := json.Marshal(data); err != nil {
		t.Errorf("luci payload not serialisable: %v", err)
	}
	acl, ok := data["usr/share/rpcd/acl.d/luci-app-9router-go.json"]
	if !ok {
		t.Fatal("missing acl")
	}
	var parsed map[string]any
	if err := json.Unmarshal(acl, &parsed); err != nil {
		t.Errorf("acl is not valid JSON: %v", err)
	}
	// The controller uses module() and the CBI uses Map/SimpleSection: both are
	// Lua LuCI 5.1 constructs. Spot-check that the files are non-empty and
	// carry the construct they are chosen for.
	if !bytes.Contains(data["usr/lib/lua/luci/controller/9router-go.lua"], []byte("module(")) {
		t.Error("controller is not a Lua LuCI controller")
	}
	if !bytes.Contains(data["usr/lib/lua/luci/model/cbi/9router-go/main.lua"], []byte("Map(")) {
		t.Error("CBI model does not build a Map")
	}
}

// TestAbsoluteBaseURL feeds the release layout: the index lives on Pages while
// the .ipk files are release assets, so Filename must be absolute or opkg
// looks for the package next to the index and 404s.
func TestAbsoluteBaseURL(t *testing.T) {
	pkgs := []builtPkg{{
		meta: pkgMeta{Name: "9router-go", Version: "1.9.14", Arch: "x86_64", Section: "net", Description: "x"},
		file: "9router-go_1.9.14_x86_64.ipk",
		size: 42,
		sum:  strings.Repeat("a", 64),
	}}
	dir := t.TempDir()
	if err := writeIndex(dir, pkgs, "https://github.com/luqman-v1/9router-go/releases/latest/download/"); err != nil {
		t.Fatalf("writeIndex: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "Packages"))
	if err != nil {
		t.Fatalf("read Packages: %v", err)
	}
	want := "Filename: https://github.com/luqman-v1/9router-go/releases/latest/download/9router-go_1.9.14_x86_64.ipk"
	if !strings.Contains(string(raw), want) {
		t.Errorf("Packages lacks %q:\n%s", want, raw)
	}
}

// TestNoCarriageReturnsInScripts pins the packaging contract that a shipped
// script must run under /bin/sh on the router: a CRLF makes the shebang line
// "#!/bin/sh\r", which the kernel reports as "not found".
func TestNoCarriageReturnsInScripts(t *testing.T) {
	dist := buildOnce(t)
	path := filepath.Join(dist, "x86_64", "9router-go_1.9.14_x86_64.ipk")
	blob, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read ipk: %v", err)
	}
	luciPath := filepath.Join(dist, "x86_64", "luci-app-9router-go_1.9.14_all.ipk")
	luciBlob, err := os.ReadFile(luciPath)
	if err != nil {
		t.Fatalf("read luci ipk: %v", err)
	}

	checked := 0
	for _, blob := range [][]byte{blob, luciBlob} {
		members, err := parseAr(blob)
		if err != nil {
			t.Fatalf("parse ar: %v", err)
		}
		for _, m := range members {
			for name, body := range m {
				if name != "control.tar.gz" && name != "data.tar.gz" {
					continue
				}
				for member, data := range untarGz(t, body) {
					if bytes.Contains(data, []byte("\r\n")) {
						t.Errorf("%s ships with CRLF line endings", member)
					}
					if strings.HasSuffix(member, ".sh") || strings.HasSuffix(member, ".init") ||
						strings.Contains(member, "init.d/") || strings.HasPrefix(member, "usr/bin/") ||
						member == "postinst" || member == "prerm" {
						checked++
					}
				}
			}
		}
	}
	if checked < 8 {
		t.Fatalf("expected at least 8 shell scripts across both packages, checked %d", checked)
	}
}

// TestBackupCoversUploads pins the part of the backup contract an operator only
// notices after a restore onto a new router: the database is not the only state.
// The test extracts the shipped script and greps it, because the behaviour
// itself needs a target file system; the assertion here is that the script
// still archives data_dir/uploads and restores it beside the database.
func TestBackupCoversUploads(t *testing.T) {
	dist := buildOnce(t)
	blob, err := os.ReadFile(filepath.Join(dist, "x86_64", "9router-go_1.9.14_x86_64.ipk"))
	if err != nil {
		t.Fatalf("read ipk: %v", err)
	}
	members, err := parseAr(blob)
	if err != nil {
		t.Fatalf("parse ar: %v", err)
	}
	script, ok := untarGz(t, members[2]["data.tar.gz"])["usr/bin/9router-go-backup"]
	if !ok {
		t.Fatal("backup helper missing from the package")
	}
	for _, want := range []string{
		`uploads="$data_dir/uploads"`,
		`arch="$out.uploads.tar"`,
		`tar -C "$data_dir" -cf "$arch" uploads`,
		`arch="$in.uploads.tar"`,
		`tar -C "$data_dir" -xf "$arch"`,
	} {
		if !bytes.Contains(script, []byte(want)) {
			t.Errorf("backup helper no longer carries %q", want)
		}
	}
}

// TestPrermKeepsUserData pins the safe half of the uninstall: the program's own
// artifacts are removed, the operator's database and uploads are not. Deleting
// the database on removal would destroy every provider connection and API key,
// and a mistaken `opkg remove` would be unrecoverable on a device with no other
// copy.
func TestPrermKeepsUserData(t *testing.T) {
	dist := buildOnce(t)
	blob, err := os.ReadFile(filepath.Join(dist, "x86_64", "9router-go_1.9.14_x86_64.ipk"))
	if err != nil {
		t.Fatalf("read ipk: %v", err)
	}
	members, err := parseAr(blob)
	if err != nil {
		t.Fatalf("parse ar: %v", err)
	}
	script, ok := untarGz(t, members[1]["control.tar.gz"])["prerm"]
	if !ok {
		t.Fatal("prerm missing from the control archive")
	}
	if !bytes.Contains(script, []byte(`rm -f "$data_dir/bin/9router-go"`)) {
		t.Error("prerm no longer removes the runtime binary copy")
	}
	for _, forbidden := range []string{
		`rm -rf "$data_dir"`,
		`rm -f "$data_dir/db/data.sqlite"`,
		`rm -rf "$data_dir/db"`,
		`rm -rf "$data_dir/uploads"`,
	} {
		if bytes.Contains(script, []byte(forbidden)) {
			t.Errorf("prerm must not destroy user state, found %q", forbidden)
		}
	}
}

// ---------------------------------------------------------------- helpers

// parseAr decodes the archive writeAr produces. Reading it back with an
// independent parser is the point: a member name that overflows its 16-byte
// field or a wrong size in the header would otherwise only surface inside opkg.
func parseAr(blob []byte) ([]map[string][]byte, error) {
	const magic = "!<arch>\n"
	if !bytes.HasPrefix(blob, []byte(magic)) {
		return nil, io.ErrUnexpectedEOF
	}
	rest := blob[len(magic):]
	var out []map[string][]byte
	for len(rest) > 0 {
		if len(rest) < 60 {
			return nil, io.ErrUnexpectedEOF
		}
		hdr := rest[:60]
		name := strings.TrimSuffix(strings.TrimSpace(string(hdr[0:16])), "/")
		size, err := strconv.Atoi(strings.TrimSpace(string(hdr[48:58])))
		if err != nil {
			return nil, err
		}
		body := rest[60:]
		if len(body) < size {
			return nil, io.ErrUnexpectedEOF
		}
		out = append(out, map[string][]byte{name: body[:size]})
		advance := size
		if size%2 == 1 {
			advance++
		}
		if advance > len(body) {
			return nil, io.ErrUnexpectedEOF
		}
		rest = body[advance:]
	}
	return out, nil
}

func untarGz(t *testing.T, blob []byte) map[string][]byte {
	t.Helper()
	zr, err := gzip.NewReader(bytes.NewReader(blob))
	if err != nil {
		t.Fatalf("open gzip member: %v", err)
	}
	tr := tar.NewReader(zr)
	out := make(map[string][]byte)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("read tar: %v", err)
		}
		if h.Typeflag != tar.TypeReg {
			continue
		}
		data, err := io.ReadAll(tr)
		if err != nil {
			t.Fatalf("read tar member %s: %v", h.Name, err)
		}
		out[strings.TrimPrefix(h.Name, "./")] = data
	}
	return out
}

func indexStanzas(t *testing.T, index string) []map[string]string {
	t.Helper()
	var stanzas []map[string]string
	for _, block := range strings.Split(strings.TrimSpace(index), "\n\n") {
		if strings.TrimSpace(block) == "" {
			continue
		}
		fields := make(map[string]string)
		for _, line := range strings.Split(block, "\n") {
			if strings.HasPrefix(line, " ") {
				continue
			}
			k, v, ok := strings.Cut(line, ": ")
			if !ok {
				continue
			}
			fields[k] = v
		}
		stanzas = append(stanzas, fields)
	}
	return stanzas
}

func keys(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
