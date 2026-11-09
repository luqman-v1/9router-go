package dashboard

import (
	"9router/proxy/internal/db"
	"9router/proxy/internal/handlerutil"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"regexp"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"
)

// GitHub import for the jailbreak preset library. An operator points the
// dashboard at a public repository of prompts — one file per model, the shape
// collections such as togg53192-cmd/jailbreaks use — and every file becomes a
// preset whose keywords are derived from its name, so it covers that model
// across providers and combo legs without arming each id by hand.
//
// The target is operator input, so only GitHub hosts are reachable: a fetcher
// that followed it anywhere would turn the dashboard into a proxy for the
// operator's own network. Redirects are re-checked against the same allowlist,
// every response is size-bounded, and the file count and total bytes are
// capped before anything is downloaded.

// Bounds for one import. A collection is a few dozen files of a few tens of
// kilobytes; the caps keep a mistyped repository from becoming a long download
// on a one-vCPU box.
const (
	maxJailbreakImportFiles      = 200
	maxJailbreakImportFileBytes  = 512 * 1024
	maxJailbreakImportTotalBytes = 8 * 1024 * 1024
	jailbreakImportParallel      = 4
	jailbreakImportTimeout       = 60 * time.Second
)

// maxJailbreakImportKeywords mirrors the storage bound in
// db.NormalizeJailbreakKeywords, which rejects a preset carrying more. The
// derivation caps itself so an import never produces a preset the store
// refuses.
const maxJailbreakImportKeywords = 20

// errJailbreakBadTarget marks a target the operator typed wrong. The handler
// answers 400 for it and 502 for anything the network did.
var errJailbreakBadTarget = errors.New("invalid github target")

var (
	githubNameRE = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
	// A ref is one or more slash-separated names: no leading, trailing or
	// empty segment, so "main", "feature/x" and a sha are fine while
	// "/etc/passwd" and "a//b" are not.
	githubRefRE = regexp.MustCompile(`^[A-Za-z0-9._-]+(/[A-Za-z0-9._-]+)*$`)
)

type jailbreakImporter struct {
	apiBase string
	rawBase string
	client  *http.Client
	// allowPrivate lifts the network guard for a URL on the gateway's own
	// network — an internal paste service, a LAN host. Off by default; the
	// production importer reads JAILBREAK_IMPORT_ALLOW_PRIVATE.
	allowPrivate bool
}

var defaultJailbreakImporter = newJailbreakImporter()

// newJailbreakImporter builds the production importer. One client serves the
// whole import, with a redirect policy and a dial guard that keep every hop
// where the operator is allowed to reach.
func newJailbreakImporter() *jailbreakImporter {
	imp := &jailbreakImporter{
		apiBase: "https://api.github.com",
		rawBase: "https://raw.githubusercontent.com",
		// An operator importing from their own network has to say so; the
		// default is public addresses only.
		allowPrivate: os.Getenv("JAILBREAK_IMPORT_ALLOW_PRIVATE") == "1",
	}
	imp.client = &http.Client{
		Timeout: jailbreakImportTimeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("too many redirects")
			}
			return imp.checkRedirect(req)
		},
		// Control runs after the name is resolved and immediately before the
		// connection is made, so the address that is dialled is the address
		// that was checked. Checking earlier leaves a window in which the
		// answer changes.
		Transport: &http.Transport{
			DialContext: (&net.Dialer{Control: imp.dialControl}).DialContext,
		},
	}
	return imp
}

// checkRedirect refuses a hop that leaves the allowlist. GitHub is always
// allowed; anything else must still be a well-formed http(s) URL, and the dial
// guard enforces the address rule on the next hop.
func (imp *jailbreakImporter) checkRedirect(req *http.Request) error {
	if isGithubHost(req.URL.Hostname()) {
		return nil
	}
	return validateJailbreakImportURL(req.URL)
}

// dialControl is the network guard. It sees the resolved address, so a
// hostname that resolves to a private address is refused here rather than
// after the connection exists.
func (imp *jailbreakImporter) dialControl(_, address string, _ syscall.RawConn) error {
	if imp.allowPrivate {
		return nil
	}
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("jailbreak import: %w", err)
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return fmt.Errorf("jailbreak import: unresolved address %q", host)
	}
	if !jailbreakImportPublicIP(ip) {
		return fmt.Errorf("jailbreak import: %s is not a public address", ip)
	}
	return nil
}

// jailbreakImportPublicIP reports whether ip is routable on the public
// internet. Everything a gateway can reach about itself or its neighbourhood
// is excluded: loopback, the private ranges, link-local (which is where cloud
// metadata lives), carrier-grade NAT, multicast, and the unspecified address.
func jailbreakImportPublicIP(ip net.IP) bool {
	if ip == nil || ip.IsUnspecified() || ip.IsLoopback() || ip.IsMulticast() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsInterfaceLocalMulticast() {
		return false
	}
	if ip4 := ip.To4(); ip4 != nil {
		switch {
		case ip4[0] == 10, ip4[0] == 127:
			return false
		case ip4[0] == 172 && ip4[1]&0xf0 == 16:
			return false
		case ip4[0] == 192 && ip4[1] == 168:
			return false
		case ip4[0] == 100 && ip4[1]&0xc0 == 64: // 100.64.0.0/10 CGNAT
			return false
		case ip4[0] == 169 && ip4[1] == 254: // includes 169.254.169.254 metadata
			return false
		case ip4[0] == 192 && ip4[1] == 0 && ip4[2] == 2: // TEST-NET-1
			return false
		case ip4[0] == 198 && ip4[1] == 51 && ip4[2] == 100: // TEST-NET-2
			return false
		case ip4[0] == 203 && ip4[1] == 0 && ip4[2] == 113: // TEST-NET-3
			return false
		case ip4[0] >= 240: // reserved, includes 255.255.255.255
			return false
		}
		return true
	}
	// Unique-local fc00::/7 is not global unicast, so it is excluded here and
	// IsGlobalUnicast covers the rest of the IPv6 space.
	if len(ip) == net.IPv6len && ip[0]&0xfe == 0xfc {
		return false
	}
	return ip.IsGlobalUnicast()
}

// validateJailbreakImportURL checks the scheme and host of a non-GitHub URL.
// The address itself is checked at dial time, where the resolved IP is known.
func validateJailbreakImportURL(u *url.URL) error {
	if u == nil {
		return fmt.Errorf("%w: empty url", errJailbreakBadTarget)
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
	default:
		return fmt.Errorf("%w: scheme %q is not http or https", errJailbreakBadTarget, u.Scheme)
	}
	if strings.TrimSpace(u.Host) == "" {
		return fmt.Errorf("%w: url has no host", errJailbreakBadTarget)
	}
	return nil
}

func isGithubHost(host string) bool {
	switch strings.ToLower(host) {
	case "github.com", "www.github.com", "raw.githubusercontent.com", "api.github.com":
		return true
	}
	return false
}

// githubJailbreakSource is a parsed import target. An empty Path means the
// whole repository; a non-empty one means a single file.
type githubJailbreakSource struct {
	Owner string
	Repo  string
	Ref   string
	Path  string
}

// githubFile is one prompt file to fetch.
type githubFile struct {
	Path string
	Size int
}

// githubTreeEntry is one blob in a repository tree.
type githubTreeEntry struct {
	Path string
	Size int
}

// jailbreakImportEntry is one preset the import wrote.
type jailbreakImportEntry struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Path     string   `json:"path"`
	Kind     string   `json:"kind,omitempty"`
	Keywords []string `json:"keywords"`
}

// jailbreakImportRefusal is one file the import skipped, with the reason, so a
// partial import never reads as a complete one.
type jailbreakImportRefusal struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

type jailbreakImportResult struct {
	Source   string                   `json:"source"`
	Imported []jailbreakImportEntry   `json:"imported"`
	Refused  []jailbreakImportRefusal `json:"refused"`
}

// jailbreakImportTarget is what the operator supplied: a GitHub repository or
// GitHub file, or one prompt file at any other http(s) URL.
type jailbreakImportTarget struct {
	// Github is non-nil when the target names a GitHub repository or file.
	Github *githubJailbreakSource
	// URL is a single prompt file at an arbitrary address.
	URL string
}

// parseJailbreakImportTarget accepts owner/repo, owner/repo@ref, the
// github.com, raw.githubusercontent.com and api.github.com URL forms, and any
// other absolute http(s) URL — which is read as one prompt file.
func parseJailbreakImportTarget(target string) (jailbreakImportTarget, error) {
	t := strings.TrimSpace(target)
	if t == "" {
		return jailbreakImportTarget{}, fmt.Errorf("%w: empty target", errJailbreakBadTarget)
	}
	if !strings.Contains(t, "://") {
		src, err := parseGithubShorthand(t)
		if err != nil {
			return jailbreakImportTarget{}, err
		}
		return jailbreakImportTarget{Github: &src}, nil
	}
	u, err := url.Parse(t)
	if err != nil {
		return jailbreakImportTarget{}, fmt.Errorf("%w: %w", errJailbreakBadTarget, err)
	}
	if isGithubHost(u.Hostname()) {
		src, err := parseGithubURL(u)
		if err != nil {
			return jailbreakImportTarget{}, err
		}
		return jailbreakImportTarget{Github: &src}, nil
	}
	if err := validateJailbreakImportURL(u); err != nil {
		return jailbreakImportTarget{}, err
	}
	return jailbreakImportTarget{URL: t}, nil
}

// parseGithubURL dispatches a GitHub URL by host.
func parseGithubURL(u *url.URL) (githubJailbreakSource, error) {
	switch strings.ToLower(u.Hostname()) {
	case "github.com", "www.github.com":
		return parseGithubWebPath(u.Path)
	case "raw.githubusercontent.com":
		return parseGithubRawPath(u.Path)
	default:
		return parseGithubAPIPath(u.Path)
	}
}

// parseGithubShorthand reads "owner/repo" and "owner/repo@ref". A path needs a
// URL: three bare segments would be ambiguous with a branch name.
func parseGithubShorthand(t string) (githubJailbreakSource, error) {
	ref := ""
	if at := strings.Index(t, "@"); at >= 0 {
		ref = t[at+1:]
		t = t[:at]
	}
	parts := strings.Split(t, "/")
	if len(parts) != 2 {
		return githubJailbreakSource{}, fmt.Errorf("%w: want owner/repo, got %q", errJailbreakBadTarget, t)
	}
	src := githubJailbreakSource{Owner: parts[0], Repo: parts[1], Ref: ref}
	if err := src.validate(); err != nil {
		return githubJailbreakSource{}, err
	}
	return src, nil
}

// parseGithubWebPath reads /owner/repo, /owner/repo/tree/ref/path and
// /owner/repo/blob/ref/path.
func parseGithubWebPath(p string) (githubJailbreakSource, error) {
	segs := splitGithubPath(p)
	if len(segs) < 2 {
		return githubJailbreakSource{}, fmt.Errorf("%w: want github.com/owner/repo", errJailbreakBadTarget)
	}
	src := githubJailbreakSource{Owner: segs[0], Repo: segs[1]}
	switch {
	case len(segs) == 2:
	case len(segs) > 3 && (segs[2] == "tree" || segs[2] == "blob"):
		src.Ref = segs[3]
		src.Path = strings.Join(segs[4:], "/")
	default:
		return githubJailbreakSource{}, fmt.Errorf("%w: unsupported path %q", errJailbreakBadTarget, p)
	}
	if err := src.validate(); err != nil {
		return githubJailbreakSource{}, err
	}
	return src, nil
}

// parseGithubRawPath reads /owner/repo/ref/path, which names one file.
func parseGithubRawPath(p string) (githubJailbreakSource, error) {
	segs := splitGithubPath(p)
	if len(segs) < 4 {
		return githubJailbreakSource{}, fmt.Errorf("%w: want raw.githubusercontent.com/owner/repo/ref/path", errJailbreakBadTarget)
	}
	src := githubJailbreakSource{Owner: segs[0], Repo: segs[1], Ref: segs[2], Path: strings.Join(segs[3:], "/")}
	if err := src.validate(); err != nil {
		return githubJailbreakSource{}, err
	}
	return src, nil
}

// parseGithubAPIPath reads /repos/owner/repo and /repos/owner/repo/git/trees/ref.
func parseGithubAPIPath(p string) (githubJailbreakSource, error) {
	segs := splitGithubPath(p)
	if len(segs) > 0 && segs[0] == "repos" {
		segs = segs[1:]
	}
	if len(segs) < 2 {
		return githubJailbreakSource{}, fmt.Errorf("%w: want api.github.com/repos/owner/repo", errJailbreakBadTarget)
	}
	src := githubJailbreakSource{Owner: segs[0], Repo: segs[1]}
	if len(segs) > 3 && segs[2] == "git" && segs[3] == "trees" && len(segs) > 4 {
		src.Ref = segs[4]
	}
	if err := src.validate(); err != nil {
		return githubJailbreakSource{}, err
	}
	return src, nil
}

func splitGithubPath(p string) []string {
	out := make([]string, 0, 6)
	for _, seg := range strings.Split(p, "/") {
		if seg != "" {
			out = append(out, seg)
		}
	}
	return out
}

// validate keeps the parsed pieces inside the GitHub name grammar and rejects
// anything that could climb out of the repository once joined onto a URL.
func (s githubJailbreakSource) validate() error {
	if !githubNameRE.MatchString(s.Owner) || !githubNameRE.MatchString(s.Repo) {
		return fmt.Errorf("%w: bad owner/repo %q/%q", errJailbreakBadTarget, s.Owner, s.Repo)
	}
	if s.Ref != "" {
		if !githubRefRE.MatchString(s.Ref) || hasGithubDotSegment(s.Ref) {
			return fmt.Errorf("%w: bad ref %q", errJailbreakBadTarget, s.Ref)
		}
	}
	if s.Path != "" {
		if strings.HasPrefix(s.Path, "/") {
			return fmt.Errorf("%w: path %q must be relative", errJailbreakBadTarget, s.Path)
		}
		if hasGithubDotSegment(s.Path) {
			return fmt.Errorf("%w: path %q has a . or .. segment", errJailbreakBadTarget, s.Path)
		}
	}
	return nil
}

func hasGithubDotSegment(p string) bool {
	for _, seg := range strings.Split(p, "/") {
		if seg == "." || seg == ".." {
			return true
		}
	}
	return false
}

// importPresets parses an operator target and imports it. name is an optional
// custom file name the operator typed (e.g. "my-prompt.md"); when it is set it
// overrides the name derived from a URL's last path segment, so an operator can
// give a preset a meaningful name regardless of where the prompt lives.
func (imp *jailbreakImporter) importPresets(ctx context.Context, repo *db.Repo, target string, name string) (*jailbreakImportResult, error) {
	parsed, err := parseJailbreakImportTarget(target)
	if err != nil {
		return nil, err
	}
	if parsed.Github != nil {
		return imp.importSource(ctx, repo, *parsed.Github)
	}
	return imp.importURL(ctx, repo, parsed.URL, name)
}

// importURL fetches one prompt file from an arbitrary address and writes it as
// a preset. When the operator supplies a name, it overrides the derived filename;
// otherwise the last path segment (SOUL.md → SOUL) is used.
func (imp *jailbreakImporter) importURL(ctx context.Context, repo *db.Repo, rawURL string, overrideName string) (*jailbreakImportResult, error) {
	body, contentType, err := imp.fetchBytes(ctx, rawURL, maxJailbreakImportFileBytes)
	if err != nil {
		return nil, err
	}
	name := jailbreakURLName(rawURL)
	if overrideName != "" {
		name = overrideName
	}
	text := jailbreakImportText(body, contentType)
	entry, err := importJailbreakDocument(repo, name, "url:"+rawURL, text, nil)
	if err != nil {
		return nil, err
	}
	return &jailbreakImportResult{
		Source:   rawURL,
		Imported: []jailbreakImportEntry{*entry},
		Refused:  make([]jailbreakImportRefusal, 0),
	}, nil
}

// jailbreakURLName is the file name a URL points at, used as the preset name.
// A URL with no usable path segment falls back to the host, so
// https://paste.example.com/abc123 still names its preset something.
func jailbreakURLName(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "prompt.md"
	}
	base := path.Base(u.Path)
	if base == "" || base == "." || base == "/" {
		return u.Hostname()
	}
	return base
}

// jailbreakImportText turns a fetched body into prompt text. A page served as
// HTML is reduced to its readable text, because pasting a documentation URL
// should not store the markup as the prompt.
func jailbreakImportText(body []byte, contentType string) string {
	if strings.Contains(strings.ToLower(contentType), "html") {
		return htmlToText(string(body))
	}
	return string(body)
}

// importSource downloads a GitHub repository or file and writes its prompt
// files as presets. Every file is attempted on its own: a single refusal never
// aborts the rest, and the result names what was skipped and why.
func (imp *jailbreakImporter) importSource(ctx context.Context, repo *db.Repo, src githubJailbreakSource) (*jailbreakImportResult, error) {
	files, skipped, ref, err := imp.collectFiles(ctx, src)
	if err != nil {
		return nil, err
	}
	claimed := jailbreakClaimedStems(files)
	res := &jailbreakImportResult{
		Source:   fmt.Sprintf("%s/%s@%s", src.Owner, src.Repo, ref),
		Imported: make([]jailbreakImportEntry, 0, len(files)),
		Refused:  skipped,
	}
	if res.Refused == nil {
		res.Refused = make([]jailbreakImportRefusal, 0)
	}

	var (
		wg  sync.WaitGroup
		mu  sync.Mutex
		sem = make(chan struct{}, jailbreakImportParallel)
	)
	for _, f := range files {
		wg.Add(1)
		go func(f githubFile) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			entry, refusal := imp.importOne(ctx, repo, src, ref, f, claimed)
			mu.Lock()
			defer mu.Unlock()
			if refusal != nil {
				res.Refused = append(res.Refused, *refusal)
				return
			}
			res.Imported = append(res.Imported, *entry)
		}(f)
	}
	wg.Wait()

	sort.Slice(res.Imported, func(i, j int) bool { return res.Imported[i].Path < res.Imported[j].Path })
	sort.Slice(res.Refused, func(i, j int) bool { return res.Refused[i].Path < res.Refused[j].Path })
	return res, nil
}

// collectFiles lists the repository, or returns the single named file, and
// reports the candidates it dropped for size or count before any download.
func (imp *jailbreakImporter) collectFiles(ctx context.Context, src githubJailbreakSource) ([]githubFile, []jailbreakImportRefusal, string, error) {
	ref := src.Ref
	if ref == "" {
		def, err := imp.defaultBranch(ctx, src)
		if err != nil {
			return nil, nil, "", err
		}
		ref = def
	}
	if src.Path != "" {
		return []githubFile{{Path: src.Path}}, nil, ref, nil
	}

	entries, truncated, err := imp.tree(ctx, src, ref)
	if err != nil {
		return nil, nil, "", err
	}
	if truncated {
		return nil, nil, "", fmt.Errorf("repository %s/%s tree is too large to list", src.Owner, src.Repo)
	}

	files := make([]githubFile, 0, len(entries))
	skipped := make([]jailbreakImportRefusal, 0)
	total := 0
	for _, e := range entries {
		if !jailbreakImportCandidate(e.Path) {
			continue
		}
		switch {
		case e.Size > maxJailbreakImportFileBytes:
			skipped = append(skipped, jailbreakImportRefusal{Path: e.Path, Reason: fmt.Sprintf("file is %d bytes (max %d)", e.Size, maxJailbreakImportFileBytes)})
		case len(files) >= maxJailbreakImportFiles:
			skipped = append(skipped, jailbreakImportRefusal{Path: e.Path, Reason: fmt.Sprintf("import is capped at %d files", maxJailbreakImportFiles)})
		case total+e.Size > maxJailbreakImportTotalBytes:
			skipped = append(skipped, jailbreakImportRefusal{Path: e.Path, Reason: fmt.Sprintf("import is capped at %d bytes total", maxJailbreakImportTotalBytes)})
		default:
			total += e.Size
			files = append(files, githubFile(e))
		}
	}
	if len(files) == 0 {
		return nil, nil, "", fmt.Errorf("no .md or .txt prompt files found in %s/%s", src.Owner, src.Repo)
	}
	return files, skipped, ref, nil
}

// jailbreakImportCandidate keeps prompt files and drops repository furniture:
// dot-directories such as .github, and the README that describes a collection
// rather than being one of its prompts.
func jailbreakImportCandidate(p string) bool {
	for _, seg := range strings.Split(p, "/") {
		if strings.HasPrefix(seg, ".") {
			return false
		}
	}
	base := strings.ToLower(path.Base(p))
	if base == "readme.md" || base == "readme.markdown" || base == "readme.txt" {
		return false
	}
	return strings.HasSuffix(base, ".md") || strings.HasSuffix(base, ".txt")
}

// importOne downloads one file from GitHub and writes it as a preset.
func (imp *jailbreakImporter) importOne(ctx context.Context, repo *db.Repo, src githubJailbreakSource, ref string, f githubFile, claimed map[string]bool) (*jailbreakImportEntry, *jailbreakImportRefusal) {
	text, err := imp.fetchText(ctx, imp.rawURL(src, ref, f.Path), maxJailbreakImportFileBytes)
	if err != nil {
		return nil, &jailbreakImportRefusal{Path: f.Path, Reason: err.Error()}
	}
	entry, err := importJailbreakDocument(repo, f.Path, fmt.Sprintf("%s/%s@%s/%s", src.Owner, src.Repo, ref, f.Path), text, claimed)
	if err != nil {
		return nil, &jailbreakImportRefusal{Path: f.Path, Reason: err.Error()}
	}
	return entry, nil
}

// errJailbreakEmptyPrompt marks a file whose content is blank: the operator
// picked a file with nothing in it, which is their mistake rather than a
// server fault.
var errJailbreakEmptyPrompt = errors.New("prompt is empty")

// importJailbreakDocument writes one prompt as a preset. It is the shared tail
// of every import path, so a file fetched from GitHub and a file uploaded from
// the dashboard derive the same id, name, source and keyword chain.
func importJailbreakDocument(repo *db.Repo, filePath, source, text string, claimed map[string]bool) (*jailbreakImportEntry, error) {
	if strings.TrimSpace(text) == "" {
		return nil, fmt.Errorf("%w: %s", errJailbreakEmptyPrompt, filePath)
	}
	stem := jailbreakFileStem(filePath)
	doc := &db.JailbreakDoc{
		ID:       slugify(stem),
		Name:     stem,
		Prompt:   text,
		Source:   source,
		Kind:     jailbreakImportKind(stem),
		Keywords: jailbreakImportKeywords(stem, claimed),
	}
	if err := repo.SaveJailbreakPreset(doc); err != nil {
		return nil, err
	}
	keywords := make([]string, 0, len(doc.Keywords))
	for _, kw := range doc.Keywords {
		keywords = append(keywords, kw.Text)
	}
	return &jailbreakImportEntry{ID: doc.ID, Name: doc.Name, Path: filePath, Kind: doc.Kind, Keywords: keywords}, nil
}

// jailbreakClaimedStems is the set of full normalized file names in the
// import, used to stop a shorter rule from stealing another file's model.
func jailbreakClaimedStems(files []githubFile) map[string]bool {
	claimed := make(map[string]bool, len(files))
	for _, f := range files {
		if stem := normalizeJailbreakStem(jailbreakFileStem(f.Path)); stem != "" {
			claimed[stem] = true
		}
	}
	return claimed
}

func jailbreakFileStem(p string) string {
	base := path.Base(p)
	return strings.TrimSuffix(base, path.Ext(base))
}

// jailbreakImportKeywords derives match rules from a file name. "deepseek v4"
// yields the chain deepseek, deepseek-v4: resolution prefers the longest
// matching rule, so the file named after a model beats a sibling that only
// shares the family token, while providerx/deepseek-v4 resolves exactly as
// deepseek-v4 does.
func jailbreakImportKeywords(stem string, claimed map[string]bool) []db.JailbreakKeyword {
	norm := normalizeJailbreakStem(stem)
	if norm == "" {
		return nil
	}
	segs := strings.Split(norm, "-")
	out := make([]db.JailbreakKeyword, 0, len(segs))
	for i := range segs {
		kw := strings.Join(segs[:i+1], "-")
		if len(kw) < 2 {
			continue
		}
		if kw != norm && claimed[kw] {
			continue
		}
		out = append(out, db.JailbreakKeyword{Text: kw})
	}
	if len(out) > maxJailbreakImportKeywords {
		out = out[len(out)-maxJailbreakImportKeywords:]
	}
	return out
}

// normalizeJailbreakStem lowercases a file name and turns separators into the
// hyphens model ids use, keeping dots: "Muse spark 1.3" becomes
// muse-spark-1.3, which is the id a client sends.
func normalizeJailbreakStem(stem string) string {
	var b strings.Builder
	pendingSep := false
	for _, r := range strings.ToLower(strings.TrimSpace(stem)) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r) || r == '.':
			if pendingSep && b.Len() > 0 {
				b.WriteByte('-')
			}
			pendingSep = false
			b.WriteRune(r)
		default:
			pendingSep = true
		}
	}
	return strings.Trim(b.String(), "-")
}

// jailbreakKnownKinds are the instruction-file conventions a preset can be
// labelled with. These are files a tool reads by name rather than prompts
// about a model, so the label is what tells them apart in the library; a
// prompt named after a model ("deepseek v4.md") carries no kind.
var jailbreakKnownKinds = map[string]bool{
	"AGENTS":      true,
	"CLAUDE":      true,
	"GEMINI":      true,
	"CURSORRULES": true,
	"WINDSURF":    true,
	"COPILOT":     true,
	"SOUL":        true,
	"PERSONA":     true,
	"IDENTITY":    true,
	"CHARACTER":   true,
	"LOAD":        true,
	"SYSTEM":      true,
	"RULES":       true,
	"POLICY":      true,
	"MODELFILE":   true,
	"CONFIG":      true,
}

// jailbreakImportKind labels a preset from its file name. "SOUL.md" is a SOUL
// preset; "deepseek v4.md" is unlabelled, because its name already says what
// it is for.
func jailbreakImportKind(stem string) string {
	up := strings.ToUpper(strings.TrimSpace(stem))
	if jailbreakKnownKinds[up] {
		return up
	}
	return ""
}

func (imp *jailbreakImporter) rawURL(src githubJailbreakSource, ref, p string) string {
	return fmt.Sprintf("%s/%s/%s/%s/%s", imp.rawBase,
		url.PathEscape(src.Owner), url.PathEscape(src.Repo),
		escapeGithubSegments(ref), escapeGithubSegments(p))
}

func escapeGithubSegments(s string) string {
	segs := strings.Split(s, "/")
	for i, seg := range segs {
		segs[i] = url.PathEscape(seg)
	}
	return strings.Join(segs, "/")
}

type githubTreeResponse struct {
	Tree []struct {
		Path string `json:"path"`
		Type string `json:"type"`
		Size int    `json:"size"`
	} `json:"tree"`
	Truncated bool `json:"truncated"`
}

func (imp *jailbreakImporter) defaultBranch(ctx context.Context, src githubJailbreakSource) (string, error) {
	var out struct {
		DefaultBranch string `json:"default_branch"`
	}
	u := fmt.Sprintf("%s/repos/%s/%s", imp.apiBase, url.PathEscape(src.Owner), url.PathEscape(src.Repo))
	if err := imp.getJSON(ctx, u, &out); err != nil {
		return "", err
	}
	if out.DefaultBranch == "" {
		return "", fmt.Errorf("repository %s/%s reports no default branch", src.Owner, src.Repo)
	}
	return out.DefaultBranch, nil
}

func (imp *jailbreakImporter) tree(ctx context.Context, src githubJailbreakSource, ref string) ([]githubTreeEntry, bool, error) {
	var out githubTreeResponse
	u := fmt.Sprintf("%s/repos/%s/%s/git/trees/%s?recursive=1", imp.apiBase,
		url.PathEscape(src.Owner), url.PathEscape(src.Repo), escapeGithubSegments(ref))
	if err := imp.getJSON(ctx, u, &out); err != nil {
		return nil, false, err
	}
	entries := make([]githubTreeEntry, 0, len(out.Tree))
	for _, e := range out.Tree {
		if e.Type == "blob" {
			entries = append(entries, githubTreeEntry{Path: e.Path, Size: e.Size})
		}
	}
	return entries, out.Truncated, nil
}

func (imp *jailbreakImporter) getJSON(ctx context.Context, u string, out any) error {
	text, err := imp.fetchText(ctx, u, 4*1024*1024)
	if err != nil {
		return err
	}
	if err := json.Unmarshal([]byte(text), out); err != nil {
		return fmt.Errorf("decode %s: %w", u, err)
	}
	return nil
}

// fetchText GETs u and returns at most limit bytes. A body past the limit is
// an error rather than a truncation: half a prompt would inject half a rule.
func (imp *jailbreakImporter) fetchText(ctx context.Context, u string, limit int64) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "9router-go")
	resp, err := imp.client.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return "", fmt.Errorf("GET %s: status %d", u, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return "", fmt.Errorf("GET %s: %w", u, err)
	}
	if int64(len(body)) > limit {
		return "", fmt.Errorf("GET %s: body exceeds %d bytes", u, limit)
	}
	return string(body), nil
}

// fetchBytes is fetchText with the content type kept, so the caller can tell a
// prompt from a page that happens to be at the end of a URL.
func (imp *jailbreakImporter) fetchBytes(ctx context.Context, u string, limit int64) ([]byte, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("User-Agent", "9router-go")
	resp, err := imp.client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, "", fmt.Errorf("GET %s: status %d", u, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, "", fmt.Errorf("GET %s: %w", u, err)
	}
	if int64(len(body)) > limit {
		return nil, "", fmt.Errorf("GET %s: body exceeds %d bytes", u, limit)
	}
	return body, resp.Header.Get("Content-Type"), nil
}

// htmlToText reduces a page to its readable text: script, style and the whole
// head are dropped, block boundaries become newlines, entities are decoded,
// and runs of blank lines collapse. It is deliberately not a parser — it is
// enough to turn a documentation URL into something usable as a prompt.
func htmlToText(s string) string {
	var b strings.Builder
	skip := 0 // depth inside script/style
	for i := 0; i < len(s); {
		if s[i] != '<' {
			if skip == 0 {
				b.WriteByte(s[i])
			}
			i++
			continue
		}
		end := strings.IndexByte(s[i:], '>')
		if end < 0 {
			break
		}
		tag := strings.ToLower(s[i+1 : i+end])
		i += end + 1
		switch {
		case strings.HasPrefix(tag, "script"), strings.HasPrefix(tag, "style"), strings.HasPrefix(tag, "head"):
			skip++
			continue
		case tag == "/script" || tag == "/style" || tag == "/head":
			if skip > 0 {
				skip--
			}
			continue
		}
		if skip > 0 {
			continue
		}
		if strings.HasPrefix(tag, "br") || strings.HasPrefix(tag, "/p") ||
			strings.HasPrefix(tag, "/div") || strings.HasPrefix(tag, "/li") ||
			strings.HasPrefix(tag, "/h") || strings.HasPrefix(tag, "/tr") {
			b.WriteByte('\n')
		}
	}
	out := htmlUnescape(b.String())
	// Collapse the blank runs the tag stripping leaves behind.
	lines := strings.Split(out, "\n")
	kept := make([]string, 0, len(lines))
	blank := false
	for _, ln := range lines {
		t := strings.TrimSpace(ln)
		if t == "" {
			if blank {
				continue
			}
			blank = true
			kept = append(kept, "")
			continue
		}
		blank = false
		kept = append(kept, t)
	}
	return strings.TrimSpace(strings.Join(kept, "\n"))
}

// htmlUnescape decodes the entities a page is likely to carry. It is a small
// table rather than a dependency, because the input is a prompt and not a
// document that needs the full set.
func htmlUnescape(s string) string {
	repl := strings.NewReplacer(
		"&amp;", "&", "&lt;", "<", "&gt;", ">", "&quot;", `"`, "&#39;", "'",
		"&apos;", "'", "&nbsp;", " ", "&#x2F;", "/", "&#47;", "/",
	)
	return repl.Replace(s)
}

// HandleImportJailbreak imports prompts as presets. POST /api/jailbreak/import
// with {"target":"owner/repo"} accepts a GitHub repository, a single file on
// GitHub, or any other http(s) URL pointing at one prompt file.
func (h *DashboardHandler) HandleImportJailbreak(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 4*1024))
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	var req struct {
		Target string `json:"target"`
		Name   string `json:"name,omitempty"` // optional custom file name
	}
	if err := json.Unmarshal(body, &req); err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if strings.TrimSpace(req.Target) == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "missing target: want owner/repo, a github.com URL, or any http(s) URL")
		return
	}
	res, err := h.importer().importPresets(r.Context(), h.Repo, req.Target, req.Name)
	if err != nil {
		status := http.StatusBadGateway
		if errors.Is(err, errJailbreakBadTarget) {
			status = http.StatusBadRequest
		}
		handlerutil.WriteJSONError(w, status, err.Error())
		return
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"ok":       true,
		"source":   res.Source,
		"imported": res.Imported,
		"refused":  res.Refused,
	})
}

// uploadOverheadBytes is the room a multipart envelope needs on top of the
// prompt itself, so the bound covers the whole request rather than the file.
const uploadOverheadBytes = 64 * 1024

// HandleUploadJailbreakPreset creates one preset from an uploaded .md or .txt
// file. POST /api/jailbreak/presets/upload, multipart/form-data, field "file".
// The file follows the same derivation as a GitHub import: its name becomes
// the preset name and the keyword chain comes from that name.
func (h *DashboardHandler) HandleUploadJailbreakPreset(w http.ResponseWriter, r *http.Request) {
	// Bound the request before parsing it. A multipart body otherwise streams
	// into memory while the limit is only checked afterwards.
	r.Body = http.MaxBytesReader(w, r.Body, maxJailbreakBodyBytes+uploadOverheadBytes)
	file, header, err := r.FormFile("file")
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "missing file field: "+err.Error())
		return
	}
	defer func() { _ = file.Close() }()
	body, err := io.ReadAll(io.LimitReader(file, maxJailbreakBodyBytes+1))
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(body) > maxJailbreakBodyBytes {
		handlerutil.WriteJSONError(w, http.StatusRequestEntityTooLarge,
			fmt.Sprintf("file exceeds %d bytes", maxJailbreakBodyBytes))
		return
	}
	// A client can send any path in the filename; only the base name is used,
	// so an upload cannot name a preset after a directory.
	name := path.Base(header.Filename)
	if name == "" || name == "." || name == ".." {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "file has no usable name")
		return
	}
	entry, err := importJailbreakDocument(h.Repo, name, "upload:"+name, string(body), nil)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, errJailbreakEmptyPrompt) {
			status = http.StatusBadRequest
		}
		handlerutil.WriteJSONError(w, status, err.Error())
		return
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "preset": entry})
}
