// Command ipkbuild assembles the OpenWrt .ipk packages for 9router-go and the
// opkg feed index that describes them.
//
// It exists because an ipk is an ar archive wrapping two gzipped tarballs, and
// the build has to run on a maintainer's machine as well as on CI. Writing that
// in Go needs no `ar` binary and keeps the package layout in one place that
// both `make ipk` and the release workflow use.
//
// Output, one directory per OpenWrt architecture so the same tree can be
// published as an opkg feed:
//
//	dist/ipk/<openwrt-arch>/9router-go_<version>_<openwrt-arch>.ipk
//	dist/ipk/<openwrt-arch>/luci-app-9router-go_<version>_all.ipk
//	dist/ipk/<openwrt-arch>/Packages
//	dist/ipk/<openwrt-arch>/Packages.gz
//
// One Go binary is published under every architecture name whose opkg would
// otherwise refuse it: a Go build with CGO disabled is generic for its GOARCH,
// so the same arm64 file is correct for aarch64_cortex-a53 (18.06 ipq807x) and
// for aarch64_generic (armvirt). opkg compares the Architecture field against
// the device's DISTRIB_ARCH and nothing else, so the aliases have to be spelled
// out per architecture name.
//
// Package layout:
//
//	/usr/libexec/9router-go/9router-go   package-owned binary (squashfs, read-only)
//	/usr/bin/9router-go                  wrapper that execs the runtime copy
//	/usr/bin/9router-go-{feed,backup,healthcheck}
//	/etc/init.d/9router-go               procd service
//	/etc/config/9router-go               UCI config (conffile)
//	/usr/lib/lua/luci/...                LuCI page (Lua CBI, in the luci-app package)
//
// Keeping the package binary separate from the runtime copy is what lets
// self-update work on a read-only squashfs: postinst copies the package binary
// to /opt/9router-go/bin, and the updater rewrites that copy in place.
package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// tarEpoch pins every member's mtime so two builds of the same input produce
// identical bytes. An identical repackage then has an identical digest, which
// makes feed diffs meaningful.
var tarEpoch = time.Unix(0, 0)

// archTarget is one OpenWrt architecture name and the binary that serves it.
type archTarget struct {
	arch string
	bin  string
}

// archList collects repeated -arch flags. `-arch aarch64_cortex-a53=path`
// repeated once per architecture name is the whole interface: it lets several
// names share one binary without inventing a second mapping table to keep in
// sync with the Makefile.
type archList []archTarget

func (a *archList) String() string {
	parts := make([]string, 0, len(*a))
	for _, t := range *a {
		parts = append(parts, t.arch+"="+t.bin)
	}
	return strings.Join(parts, ",")
}

func (a *archList) Set(v string) error {
	arch, bin, ok := strings.Cut(v, "=")
	if !ok || strings.TrimSpace(arch) == "" || strings.TrimSpace(bin) == "" {
		return fmt.Errorf("want <openwrt-arch>=<binary path>, got %q", v)
	}
	*a = append(*a, archTarget{arch: strings.TrimSpace(arch), bin: strings.TrimSpace(bin)})
	return nil
}

func main() {
	var (
		version = flag.String("version", "", "package version, e.g. 1.9.14 (required)")
		outDir  = flag.String("out", "dist/ipk", "output directory")
		srcDir  = flag.String("src", "openwrt", "packaging assets directory")
		baseURL = flag.String("base-url", "", "prefix for Packages Filename entries, e.g. a release download URL")
		arches  archList
	)
	flag.Var(&arches, "arch", "openwrt-arch=binary, repeatable (e.g. aarch64_cortex-a53=dist/bin/arm64)")
	flag.Parse()

	if err := run(*version, *outDir, *srcDir, *baseURL, arches); err != nil {
		fmt.Fprintln(os.Stderr, "ipkbuild:", err)
		os.Exit(1)
	}
}

func run(version, outDir, srcDir, baseURL string, targets []archTarget) error {
	if strings.TrimSpace(version) == "" {
		return fmt.Errorf("-version is required")
	}
	if len(targets) == 0 {
		return fmt.Errorf("no -arch given")
	}
	assets, err := loadAssets(srcDir)
	if err != nil {
		return err
	}
	maintainer, license := readMetadata(assets.controlTemplate)

	sorted := make([]archTarget, len(targets))
	copy(sorted, targets)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].arch < sorted[j].arch })

	// One read per distinct binary, however many architecture names point at
	// it: the same 40 MB file is packed under several names, and re-reading it
	// per alias would dominate the build.
	binCache := make(map[string][]byte)
	for _, t := range sorted {
		if _, ok := binCache[t.bin]; ok {
			continue
		}
		data, err := os.ReadFile(t.bin)
		if err != nil {
			return fmt.Errorf("read binary %s: %w", t.bin, err)
		}
		if len(data) == 0 {
			return fmt.Errorf("binary %s is empty", t.bin)
		}
		binCache[t.bin] = data
	}

	luci, err := buildLuciPackage(version, assets, maintainer, license)
	if err != nil {
		return err
	}

	for _, t := range sorted {
		dir := filepath.Join(outDir, t.arch)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create %s: %w", dir, err)
		}
		main, err := buildMainPackage(version, t.arch, binCache[t.bin], assets, maintainer, license)
		if err != nil {
			return err
		}
		var built []builtPkg
		for _, p := range []pkg{main, luci} {
			b, err := p.write(dir)
			if err != nil {
				return err
			}
			built = append(built, b)
		}
		if err := writeIndex(dir, built, baseURL); err != nil {
			return err
		}
		for _, b := range built {
			fmt.Printf("%-24s %-24s %8.1f KiB  %s\n",
				b.meta.Name, b.meta.Arch, float64(b.size)/1024, filepath.Join(dir, b.file))
		}
	}
	return nil
}

// ---------------------------------------------------------------- metadata

// pkgMeta is everything both the control file and an opkg feed stanza need, so
// the two can never disagree about a version or a dependency.
type pkgMeta struct {
	Name          string
	Version       string
	Arch          string
	Section       string
	Priority      string
	Maintainer    string
	License       string
	Depends       string
	InstalledSize int // KiB
	Description   string
}

func (m pkgMeta) control() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Package: %s\n", m.Name)
	fmt.Fprintf(&b, "Version: %s\n", m.Version)
	if m.Depends != "" {
		fmt.Fprintf(&b, "Depends: %s\n", m.Depends)
	}
	fmt.Fprintf(&b, "Source: feeds/packages/net/%s\n", m.Name)
	fmt.Fprintf(&b, "SourceName: %s\n", m.Name)
	fmt.Fprintf(&b, "License: %s\n", m.License)
	fmt.Fprintf(&b, "Section: %s\n", m.Section)
	if m.Priority != "" {
		fmt.Fprintf(&b, "Priority: %s\n", m.Priority)
	}
	fmt.Fprintf(&b, "Maintainer: %s\n", m.Maintainer)
	fmt.Fprintf(&b, "Architecture: %s\n", m.Arch)
	fmt.Fprintf(&b, "Installed-Size: %d\n", m.InstalledSize)
	fmt.Fprintf(&b, "Description: %s\n", strings.ReplaceAll(m.Description, "\n", "\n "))
	return b.String()
}

// stanza is the feed entry: the control fields opkg indexes plus the download
// facts that only exist once the file is on disk. Filename stays relative so
// one index works from any mirror; opkg resolves it against the feed URL.
func (m pkgMeta) stanza(file string, size int64, sum string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Package: %s\n", m.Name)
	fmt.Fprintf(&b, "Version: %s\n", m.Version)
	if m.Depends != "" {
		fmt.Fprintf(&b, "Depends: %s\n", m.Depends)
	}
	fmt.Fprintf(&b, "Architecture: %s\n", m.Arch)
	fmt.Fprintf(&b, "Installed-Size: %d\n", m.InstalledSize)
	fmt.Fprintf(&b, "Filename: %s\n", file)
	fmt.Fprintf(&b, "Size: %d\n", size)
	fmt.Fprintf(&b, "SHA256sum: %s\n", sum)
	fmt.Fprintf(&b, "Section: %s\n", m.Section)
	fmt.Fprintf(&b, "Description: %s\n", strings.ReplaceAll(m.Description, "\n", "\n "))
	return b.String()
}

// readMetadata takes the two fields the build must not invent: a placeholder
// maintainer is a review failure, so both come from the hand-written template.
func readMetadata(template string) (maintainer, license string) {
	maintainer = "https://github.com/luqman-v1/9router-go"
	license = "MIT"
	for _, line := range strings.Split(template, "\n") {
		switch {
		case strings.HasPrefix(line, "Maintainer:"):
			if v := strings.TrimSpace(strings.TrimPrefix(line, "Maintainer:")); v != "" {
				maintainer = v
			}
		case strings.HasPrefix(line, "License:"):
			if v := strings.TrimSpace(strings.TrimPrefix(line, "License:")); v != "" {
				license = v
			}
		}
	}
	return maintainer, license
}

// ---------------------------------------------------------------- assets

// assetFile is one file from openwrt/ and where it lands in the package.
type assetFile struct {
	src  string // relative to the packaging directory
	dest string // absolute path inside the package
	mode int64
}

// assets holds every packaging file, already read.
type assets struct {
	controlTemplate string
	scripts         []assetFile
	content         map[string][]byte // dest -> bytes
}

func loadAssets(srcDir string) (assets, error) {
	// CRLF is normalised away rather than trusted to .gitattributes: a
	// checkout on Windows with core.autocrlf=true rewrites these files, and a
	// shipped "/etc/init.d/9router-go" whose shebang is "#!/bin/sh\r" fails
	// with "not found" on the router, after install, with no useful message.
	read := func(rel string) ([]byte, error) {
		b, err := os.ReadFile(filepath.Join(srcDir, filepath.FromSlash(rel)))
		if err != nil {
			return nil, fmt.Errorf("read packaging asset %s: %w", rel, err)
		}
		return bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n")), nil
	}
	var a assets
	a.content = make(map[string][]byte)

	ctl, err := read("control.in")
	if err != nil {
		return a, err
	}
	a.controlTemplate = string(ctl)

	// Every file the package ships other than the binary. Listing them here
	// means a file added to openwrt/ reaches the package by adding one line,
	// rather than by remembering to edit a tar entry as well.
	files := []assetFile{
		{src: "9router-go.init", dest: "/etc/init.d/9router-go", mode: 0o755},
		{src: "9router-go.config", dest: "/etc/config/9router-go", mode: 0o644},
		// Without this drop-in sysupgrade discards the database on every flash:
		// OpenWrt's default keep set (/lib/upgrade/keep.d/base-files-essential
		// plus a commented-out /etc/sysupgrade.conf) names nothing this package
		// owns. See the file for what is deliberately excluded.
		{src: "9router-go.keep.d", dest: "/lib/upgrade/keep.d/9router-go", mode: 0o644},
		{src: "postinst", dest: "/control/postinst", mode: 0o755},
		{src: "prerm", dest: "/control/prerm", mode: 0o755},
		{src: "9router-go.wrapper", dest: "/usr/bin/9router-go", mode: 0o755},
		{src: "9router-go-feed", dest: "/usr/bin/9router-go-feed", mode: 0o755},
		{src: "9router-go-backup", dest: "/usr/bin/9router-go-backup", mode: 0o755},
		{src: "9router-go-healthcheck", dest: "/usr/bin/9router-go-healthcheck", mode: 0o755},
		{src: "luci-app-9router-go/luasrc/controller/9router-go.lua", dest: "/usr/lib/lua/luci/controller/9router-go.lua", mode: 0o644},
		{src: "luci-app-9router-go/luasrc/model/cbi/9router-go/main.lua", dest: "/usr/lib/lua/luci/model/cbi/9router-go/main.lua", mode: 0o644},
		{src: "luci-app-9router-go/luasrc/view/9router-go/overview.htm", dest: "/usr/lib/lua/luci/view/9router-go/overview.htm", mode: 0o644},
		{src: "luci-app-9router-go/acl.json", dest: "/usr/share/rpcd/acl.d/luci-app-9router-go.json", mode: 0o644},
		{src: "luci-app-9router-go/postinst", dest: "/control/luci-postinst", mode: 0o755},
	}
	for _, f := range files {
		b, err := read(f.src)
		if err != nil {
			return a, err
		}
		a.content[f.dest] = b
	}
	a.scripts = files
	return a, nil
}

// ---------------------------------------------------------------- packages

func buildMainPackage(version, ipkArch string, bin []byte, a assets, maintainer, license string) (pkg, error) {
	var entries []tarEntry
	entries = append(entries, tarEntry{name: "./usr/libexec/9router-go/9router-go", mode: 0o755, data: bin})
	for _, f := range a.scripts {
		if ownerOf(f.dest) != ownsMain {
			continue
		}
		entries = append(entries, tarEntry{name: "." + f.dest, mode: f.mode, data: a.content[f.dest]})
	}
	dataTar, size, err := tarGz(entries)
	if err != nil {
		return pkg{}, err
	}
	meta := pkgMeta{
		Name:          "9router-go",
		Version:       version,
		Arch:          ipkArch,
		Section:       "net",
		Priority:      "optional",
		Maintainer:    maintainer,
		License:       license,
		Depends:       "ca-bundle",
		InstalledSize: size,
		Description: "AI API proxy gateway (Go port of 9router) with token savers.\n" +
			"Routes OpenAI, Anthropic and Gemini compatible traffic to 120+ providers\n" +
			"with account rotation, combos and built-in Cloudflare/Tailscale tunnels.\n" +
			"The dashboard is embedded in the binary and served on the configured port.",
	}
	controlTar, _, err := tarGz([]tarEntry{
		{name: "./control", mode: 0o644, data: []byte(meta.control())},
		{name: "./postinst", mode: 0o755, data: withVersion(a.content["/control/postinst"], version)},
		{name: "./prerm", mode: 0o755, data: a.content["/control/prerm"]},
		{name: "./conffiles", mode: 0o644, data: []byte("/etc/config/9router-go\n")},
	})
	if err != nil {
		return pkg{}, err
	}
	return pkg{meta: meta, data: dataTar, control: controlTar}, nil
}

func buildLuciPackage(version string, a assets, maintainer, license string) (pkg, error) {
	var entries []tarEntry
	for _, f := range a.scripts {
		if ownerOf(f.dest) != ownsLuci {
			continue
		}
		entries = append(entries, tarEntry{name: "." + f.dest, mode: f.mode, data: a.content[f.dest]})
	}
	dataTar, size, err := tarGz(entries)
	if err != nil {
		return pkg{}, err
	}
	meta := pkgMeta{
		Name:       "luci-app-9router-go",
		Version:    version,
		Arch:       "all",
		Section:    "luci",
		Priority:   "optional",
		Maintainer: maintainer,
		License:    license,
		// luci-base only, deliberately: this is a Lua CBI app, which is what
		// OpenWrt 18.06 and 19.07 run natively. On 21.02 and later the Lua LuCI
		// compatibility layer is a separate package (luci-compat) that is not
		// present on 18.06, so depending on it would make the package
		// uninstallable on the oldest release this supports. The postinst names
		// the missing package on a stock 21.02+ image instead.
		Depends:       "luci-base",
		InstalledSize: size,
		Description: "LuCI support for 9router-go.\n" +
			"Status, start/stop/restart and the UCI settings for the 9router-go\n" +
			"gateway, without opening the gateway's own dashboard on another port.",
	}
	controlTar, _, err := tarGz([]tarEntry{
		{name: "./control", mode: 0o644, data: []byte(meta.control())},
		{name: "./postinst", mode: 0o755, data: a.content["/control/luci-postinst"]},
	})
	if err != nil {
		return pkg{}, err
	}
	return pkg{meta: meta, data: dataTar, control: controlTar}, nil
}

// pkgOwner says which package a packaging file belongs to. Without it the
// filter is an ad-hoc prefix check duplicated in two builders, and the ACL file
// (a LuCI file living under /usr/share/rpcd/) landed in the main package.
type pkgOwner int

const (
	ownsMain pkgOwner = iota
	ownsLuci
	ownsControl // control.tar.gz member, not a data.tar.gz file
)

// ownerOf is where a file's owner is decided, once.
func ownerOf(dest string) pkgOwner {
	switch {
	case strings.HasPrefix(dest, "/control/"):
		return ownsControl
	case strings.HasPrefix(dest, "/usr/lib/lua/"),
		strings.Contains(dest, "/rpcd/acl.d/"):
		return ownsLuci
	default:
		return ownsMain
	}
}

// withVersion substitutes the package version into a script. postinst needs it
// to tell an opkg upgrade (which must win) from a same-version reinstall.
func withVersion(script []byte, version string) []byte {
	return bytes.ReplaceAll(script, []byte("@VERSION@"), []byte(version))
}

// ---------------------------------------------------------------- archive

type pkg struct {
	meta    pkgMeta
	data    []byte
	control []byte
}

type builtPkg struct {
	meta pkgMeta
	file string
	size int64
	sum  string
}

// write emits the ipk and returns the facts the feed index needs.
func (p pkg) write(dir string) (builtPkg, error) {
	file := fmt.Sprintf("%s_%s_%s.ipk", p.meta.Name, p.meta.Version, p.meta.Arch)
	blob := writeAr([]arMember{
		{name: "debian-binary", data: []byte("2.0\n")},
		{name: "control.tar.gz", data: p.control},
		{name: "data.tar.gz", data: p.data},
	})
	if err := os.WriteFile(filepath.Join(dir, file), blob, 0o644); err != nil {
		return builtPkg{}, fmt.Errorf("write %s: %w", file, err)
	}
	sum := sha256.Sum256(blob)
	return builtPkg{meta: p.meta, file: file, size: int64(len(blob)), sum: hex.EncodeToString(sum[:])}, nil
}

func writeIndex(dir string, pkgs []builtPkg, baseURL string) error {
	var b strings.Builder
	for i, p := range pkgs {
		if i > 0 {
			b.WriteString("\n")
		}
		// The .ipk files are release assets, not siblings of this index: the
		// index is small enough to publish on Pages, while a 20 MB binary per
		// architecture per release is not. opkg accepts an absolute Filename,
		// so one index published once points at every release.
		filename := p.file
		if baseURL != "" {
			filename = strings.TrimSuffix(baseURL, "/") + "/" + p.file
		}
		b.WriteString(p.meta.stanza(filename, p.size, p.sum))
	}
	idx := []byte(b.String())
	if err := os.WriteFile(filepath.Join(dir, "Packages"), idx, 0o644); err != nil {
		return fmt.Errorf("write Packages: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Packages.gz"), gzipBytes(idx), 0o644); err != nil {
		return fmt.Errorf("write Packages.gz: %w", err)
	}
	return nil
}

type tarEntry struct {
	name string
	mode int64
	data []byte
}

// tarGz builds a gzipped tar and reports the uncompressed size in KiB, which is
// what Installed-Size means. Directory members are synthesised so the archive
// also extracts with a plain `tar -xf`, not only through opkg.
func tarGz(entries []tarEntry) ([]byte, int, error) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)

	seen := make(map[string]bool, len(entries))
	memorySize := 0
	for _, e := range entries {
		for _, dir := range missingParents(e.name, seen) {
			// Directory members carry the same "./" prefix as the files: an
			// archive that mixes "usr/" and "./usr/bin/x" extracts correctly
			// with GNU tar but produces a second, empty tree with a strict
			// reader, and opkg's extraction is the strict one that matters.
			name := dir + "/"
			if strings.HasPrefix(e.name, "./") {
				name = "./" + name
			}
			if err := tw.WriteHeader(&tar.Header{
				Name: name, Typeflag: tar.TypeDir, Mode: 0o755, ModTime: tarEpoch,
			}); err != nil {
				return nil, 0, fmt.Errorf("tar dir %s: %w", dir, err)
			}
			seen[dir] = true
		}
		if err := tw.WriteHeader(&tar.Header{
			Name: e.name, Typeflag: tar.TypeReg, Mode: e.mode, Size: int64(len(e.data)), ModTime: tarEpoch,
		}); err != nil {
			return nil, 0, fmt.Errorf("tar header %s: %w", e.name, err)
		}
		if _, err := tw.Write(e.data); err != nil {
			return nil, 0, fmt.Errorf("tar write %s: %w", e.name, err)
		}
		memorySize += len(e.data)
	}
	if err := tw.Close(); err != nil {
		return nil, 0, fmt.Errorf("close tar: %w", err)
	}
	if err := zw.Close(); err != nil {
		return nil, 0, fmt.Errorf("close gzip: %w", err)
	}
	return buf.Bytes(), (memorySize + 1023) / 1024, nil
}

// missingParents lists the directory members still absent for name, outermost
// first.
func missingParents(name string, seen map[string]bool) []string {
	var dirs []string
	for dir := path.Dir(name); dir != "." && dir != "/" && !seen[dir]; dir = path.Dir(dir) {
		dirs = append([]string{dir}, dirs...)
	}
	return dirs
}

type arMember struct {
	name string
	data []byte
}

// writeAr encodes the ar archive that wraps the two tarballs. GNU-style names
// carry a trailing slash, matching what OpenWrt's ipk tooling writes and what
// opkg's reader expects.
func writeAr(members []arMember) []byte {
	var b bytes.Buffer
	b.WriteString("!<arch>\n")
	for _, m := range members {
		name := m.name + "/"
		if len(name) > 16 {
			panic("ipkbuild: ar member name too long: " + m.name)
		}
		fmt.Fprintf(&b, "%-16s%-12d%-6d%-6d%-8s%-10d`\n",
			name, 0, 0, 0, "100644", len(m.data))
		b.Write(m.data)
		if len(m.data)%2 == 1 {
			b.WriteByte('\n')
		}
	}
	return b.Bytes()
}

func gzipBytes(in []byte) []byte {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	_, _ = zw.Write(in)
	_ = zw.Close()
	return buf.Bytes()
}
