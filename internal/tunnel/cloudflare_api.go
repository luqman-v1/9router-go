package tunnel

import (
	"bytes"
	"context"
	json "encoding/json/v2"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"time"

	"9router/proxy/internal/log"
)

// cloudflareAPIBase is the Cloudflare REST API root. It is a variable rather
// than a constant so a test can point it at a local server: provisioning is
// only observable through the requests it makes, and asserting against the
// live API would make the suite depend on a real Cloudflare account.
var cloudflareAPIBase = "https://api.cloudflare.com/client/v4"

// cfAPITimeout bounds each Cloudflare API call. Provisioning runs inside the
// enable request, so a hanging call would hold the dashboard's spinner.
const cfAPITimeout = 20 * time.Second

// CloudflareOptions carries the credentials for auto-provisioning. The operator
// creates an API token scoped to Cloudflare Tunnel:Edit on the account and
// Zone:DNS:Edit on the zone; this package does the rest.
type CloudflareOptions struct {
	APIToken string
	// AccountID is the Cloudflare account that will own the tunnel. Optional:
	// when empty the first account the token can reach is used.
	AccountID string
	// Zone is the domain that will host the hostname, e.g. "example.com".
	Zone string
	// Hostname is the full subdomain to route, e.g. "router.example.com".
	Hostname string
}

// cfClient is shared so repeated provisioning calls reuse connections.
var cfClient = &http.Client{Timeout: cfAPITimeout}

// cfEnvelope is Cloudflare's standard response wrapper. Every v4 endpoint
// answers with this shape, so one decoder covers them all.
//
// Result is `any` rather than a raw byte slice: encoding/json/v2 decodes a
// []byte field from a base64 *string*, not from raw JSON, so the two-step
// "keep it raw, decode later" pattern does not carry over. Decoding into `any`
// costs a map the callers would have built anyway and keeps the field names
// intact.
type cfEnvelope struct {
	Success bool         `json:"success"`
	Errors  []cfError    `json:"errors"`
	Result  any          `json:"result"`
	Info    cfResultInfo `json:"result_info"`
}

// cfResultInfo carries pagination on list endpoints. It is decoded but unused
// by the provisioning paths, which only ever need the first page.
type cfResultInfo struct {
	Page       int `json:"page"`
	PerPage    int `json:"per_page"`
	TotalCount int `json:"total_count"`
}

type cfError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// tunnelCredentials is what a freshly created remotely-managed tunnel returns.
// The token is the connector credential: it is what `cloudflared tunnel run
// --token` authenticates with, and it is shown to the operator once.
type tunnelCredentials struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Token string `json:"token"`
}

// dnsRecord is a CNAME pointing the operator's hostname at the tunnel.
type dnsRecord struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Name    string `json:"name"`
	Content string `json:"content"`
}

// cfRequest performs one authenticated Cloudflare API call and decodes the
// standard envelope. The token travels in a header rather than the query
// string so it never lands in an access log.
// cfRequestEnvelope performs one authenticated call and returns only the
// envelope, leaving the result payload for the caller to decode.
//
// It exists because cfRequest always decodes `result` into the destination it
// is given. Handing it an envelope struct would make it decode an array into a
// struct, which fails — so a caller that wants the raw payload needs this
// variant instead.
func cfRequestEnvelope(ctx context.Context, method, path, token string, body any) (cfEnvelope, error) {
	var reader *bytes.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return cfEnvelope{}, fmt.Errorf("tunnel.cfRequestEnvelope: marshal: %w", err)
		}
		reader = bytes.NewReader(raw)
	} else {
		reader = bytes.NewReader(nil)
	}

	req, err := http.NewRequestWithContext(ctx, method, cloudflareAPIBase+path, reader)
	if err != nil {
		return cfEnvelope{}, fmt.Errorf("tunnel.cfRequestEnvelope: build: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := cfClient.Do(req)
	if err != nil {
		return cfEnvelope{}, fmt.Errorf("tunnel.cfRequestEnvelope: %s %s: %w", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	var env cfEnvelope
	if err := json.Unmarshal(readLimited(resp.Body), &env); err != nil {
		return cfEnvelope{}, fmt.Errorf("tunnel.cfRequestEnvelope: decode %s %s: %w", method, path, err)
	}
	if !env.Success {
		if len(env.Errors) > 0 {
			return cfEnvelope{}, fmt.Errorf("cloudflare api %s %s: %s (code %d)", method, path, env.Errors[0].Message, env.Errors[0].Code)
		}
		return cfEnvelope{}, fmt.Errorf("cloudflare api %s %s: rejected without a reason", method, path)
	}
	return env, nil
}

func cfRequest(ctx context.Context, method, path, token string, body any, out any) error {
	var reader *bytes.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("tunnel.cfRequest: marshal: %w", err)
		}
		reader = bytes.NewReader(raw)
	} else {
		reader = bytes.NewReader(nil)
	}

	req, err := http.NewRequestWithContext(ctx, method, cloudflareAPIBase+path, reader)
	if err != nil {
		return fmt.Errorf("tunnel.cfRequest: build: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := cfClient.Do(req)
	if err != nil {
		return fmt.Errorf("tunnel.cfRequest: %s %s: %w", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw := readLimited(resp.Body)

	var env cfEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("tunnel.cfRequest: decode %s %s: %w", method, path, err)
	}
	if !env.Success {
		if len(env.Errors) > 0 {
			return fmt.Errorf("cloudflare api %s %s: %s (code %d)", method, path, env.Errors[0].Message, env.Errors[0].Code)
		}
		return fmt.Errorf("cloudflare api %s %s: rejected without a reason", method, path)
	}

	// The envelope is decoded twice: once for the success flag and the error
	// list, once for the caller's own type. A single pass cannot do both,
	// because encoding/json/v2 has no RawMessage to hold the undecoded payload
	// — a []byte field would be read as base64, not as JSON.
	if out != nil && env.Result != nil {
		resultJSON, err := json.Marshal(env.Result)
		if err != nil {
			return fmt.Errorf("tunnel.cfRequest: re-encode result of %s %s: %w", method, path, err)
		}
		if err := json.Unmarshal(resultJSON, out); err != nil {
			return fmt.Errorf("tunnel.cfRequest: decode result of %s %s: %w", method, path, err)
		}
	}
	return nil
}

// readLimited caps the response body so a misbehaving endpoint cannot exhaust
// memory while the enable request is waiting on it.
func readLimited(r interface{ Read([]byte) (int, error) }) []byte {
	var buf bytes.Buffer
	chunk := make([]byte, 32*1024)
	for buf.Len() < 4*1024*1024 {
		n, err := r.Read(chunk)
		if n > 0 {
			buf.Write(chunk[:n])
		}
		if err != nil {
			break
		}
	}
	return buf.Bytes()
}

// appendPage merges one page of results into the caller's slice pointer.
//
// The envelope's result arrives as `any`, so the page is re-encoded and decoded
// into a fresh slice of the destination's element type, then appended. That
// round trip is the cost of encoding/json/v2 having no RawMessage to hold an
// undecoded payload; it happens once per page, on a path that already made a
// network call.
func appendPage(page any, out any) error {
	if page == nil {
		return nil
	}
	raw, err := json.Marshal(page)
	if err != nil {
		return fmt.Errorf("tunnel.appendPage: re-encode: %w", err)
	}

	// out is a pointer to a slice. Reflect on it so the page is appended to
	// whatever is already there instead of replacing it.
	dst := reflect.ValueOf(out)
	if dst.Kind() != reflect.Pointer || dst.Elem().Kind() != reflect.Slice {
		// Not a slice destination: decode directly, which covers the
		// single-object case.
		return json.Unmarshal(raw, out)
	}

	pageVal := reflect.New(dst.Elem().Type())
	if err := json.Unmarshal(raw, pageVal.Interface()); err != nil {
		return fmt.Errorf("tunnel.appendPage: decode page: %w", err)
	}
	dst.Elem().Set(reflect.AppendSlice(dst.Elem(), pageVal.Elem()))
	return nil
}

// cfPageSize is the page size used for list lookups. Cloudflare's default is
// 25, which silently truncates an account with more zones than that: the zone
// the operator wants is on page 2 and the lookup reports it as missing.
const cfPageSize = 100

// cfMaxPages bounds the pagination walk. A malformed total_count must not turn
// into an unbounded loop against a rate-limited API.
const cfMaxPages = 100

// listAllPages walks a paginated Cloudflare list endpoint and returns every
// result. The envelope carries the total in result_info, so the loop stops when
// it has collected that many rather than guessing from an empty page.
func listAllPages(ctx context.Context, token, path string, out any) error {
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	for page := 1; page <= cfMaxPages; page++ {
		// The envelope is fetched on its own so the result payload stays
		// undecoded for appendPage to merge. Handing cfRequest an envelope
		// struct instead would make it decode an array into a struct.
		paged := fmt.Sprintf("%s%spage=%d&per_page=%d", path, sep, page, cfPageSize)
		env, err := cfRequestEnvelope(ctx, http.MethodGet, paged, token, nil)
		if err != nil {
			return err
		}
		if err := appendPage(env.Result, out); err != nil {
			return err
		}
		// Stop once every reported item has been collected. An empty result
		// also ends the walk, which covers a response with no total at all.
		if env.Result == nil {
			return nil
		}
		if env.Info.TotalCount > 0 && env.Info.Page*env.Info.PerPage >= env.Info.TotalCount {
			return nil
		}
	}
	return nil
}

// ResolveAccountID finds the account a token can act on. An explicit id is
// preferred; otherwise the first reachable account is used, because asking the
// operator for an id they have to look up is friction the API can remove.
func ResolveAccountID(ctx context.Context, opts CloudflareOptions) (string, error) {
	if id := strings.TrimSpace(opts.AccountID); id != "" {
		return id, nil
	}
	var accounts []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := listAllPages(ctx, opts.APIToken, "/accounts", &accounts); err != nil {
		return "", fmt.Errorf("tunnel.ResolveAccountID: %w", err)
	}
	if len(accounts) == 0 {
		return "", fmt.Errorf("tunnel.ResolveAccountID: token reaches no Cloudflare account — check that it carries Account:Read")
	}
	return accounts[0].ID, nil
}

// ResolveZoneID maps a domain onto the zone that serves it.
//
// The exact name is matched first, then the longest zone that is a suffix of
// the requested one. The suffix match is what makes a subdomain work: an
// operator who types "ai.helmiau.my.id" as the zone when the zone on their
// account is "helmiau.my.id" has named the hostname, not the zone, and an
// exact match alone would report it missing.
func ResolveZoneID(ctx context.Context, opts CloudflareOptions) (string, error) {
	var zones []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := listAllPages(ctx, opts.APIToken, "/zones", &zones); err != nil {
		return "", fmt.Errorf("tunnel.ResolveZoneID: %w", err)
	}
	if len(zones) == 0 {
		return "", fmt.Errorf("tunnel.ResolveZoneID: token reaches no Cloudflare zone — check that it carries Zone:Read")
	}

	want := strings.ToLower(strings.TrimSpace(opts.Zone))
	bestID, bestLen := "", 0
	for _, z := range zones {
		name := strings.ToLower(strings.TrimSpace(z.Name))
		if name == want {
			return z.ID, nil
		}
		// Longest suffix wins, so "ai.helmiau.my.id" resolves to
		// "helmiau.my.id" rather than to a shorter zone that also matches.
		if strings.HasSuffix(want, "."+name) && len(name) > bestLen {
			bestID, bestLen = z.ID, len(name)
		}
	}
	if bestID != "" {
		return bestID, nil
	}
	return "", fmt.Errorf("tunnel.ResolveZoneID: no zone on this account covers %q (token reaches %d zones)", opts.Zone, len(zones))
}

// ProvisionNamedTunnel creates a remotely-managed tunnel, routes hostname to
// it, and returns the connector token.
//
// "Remotely-managed" means the tunnel's configuration lives in Cloudflare
// rather than in a local config file: the origin is declared through the API,
// so this binary only has to run the connector. That is what keeps the
// operator from having to write and maintain an ingress rule by hand.
//
// The create step is an upsert on the tunnel name, not a blind POST. An
// operator switching from a quick tunnel, or re-running Auto Setup after a
// failed attempt, must land on the tunnel they already made instead of
// accumulating orphans that still hold a DNS record and a connector token.
func ProvisionNamedTunnel(ctx context.Context, opts CloudflareOptions, localPort int) (token, tunnelID string, err error) {
	if strings.TrimSpace(opts.APIToken) == "" {
		return "", "", fmt.Errorf("tunnel.ProvisionNamedTunnel: API token is required")
	}
	if strings.TrimSpace(opts.Zone) == "" || strings.TrimSpace(opts.Hostname) == "" {
		return "", "", fmt.Errorf("tunnel.ProvisionNamedTunnel: zone and hostname are required")
	}

	accountID, err := ResolveAccountID(ctx, opts)
	if err != nil {
		return "", "", err
	}

	tunnelName := tunnelNameFromHostname(opts.Hostname)
	existingID, findErr := findTunnelByName(ctx, opts.APIToken, accountID, tunnelName)
	if findErr != nil {
		// A lookup failure is not fatal: the create below is still correct, it
		// just may produce a second tunnel with the same name. Cloudflare
		// tolerates duplicate names, so the enable still succeeds.
		log.Warn("tunnel", "tunnel lookup failed; proceeding to create", "name", tunnelName, "error", findErr)
	}

	var creds tunnelCredentials
	if existingID != "" {
		log.Info("tunnel", "reusing existing cloudflare tunnel", "name", tunnelName, "id", existingID)
		creds = tunnelCredentials{ID: existingID, Name: tunnelName}
		// A tunnel that already exists has a token this binary never saw, so it
		// has to be minted again. Cloudflare issues one per request.
		minted, mintErr := mintTunnelToken(ctx, opts.APIToken, accountID, existingID)
		if mintErr != nil {
			return "", "", fmt.Errorf("tunnel.ProvisionNamedTunnel: token for existing tunnel: %w", mintErr)
		}
		creds.Token = minted
	} else {
		createPath := fmt.Sprintf("/accounts/%s/cfd_tunnel", accountID)
		if err := cfRequest(ctx, http.MethodPost, createPath, opts.APIToken, map[string]any{
			"name":       tunnelName,
			"config_src": "cloudflare",
		}, &creds); err != nil {
			return "", "", fmt.Errorf("tunnel.ProvisionNamedTunnel: create tunnel: %w", err)
		}
		log.Info("tunnel", "cloudflare tunnel created", "name", tunnelName, "id", creds.ID)
	}

	if creds.Token == "" {
		return "", "", fmt.Errorf("tunnel.ProvisionNamedTunnel: no connector token for tunnel %s", creds.ID)
	}

	// Point the tunnel at this gateway. A remotely-managed tunnel has no local
	// config, so the ingress rule is set through the API or the connector has
	// nothing to forward to.
	if err := configureTunnelIngress(ctx, opts.APIToken, accountID, creds.ID, opts.Hostname, localPort); err != nil {
		return "", "", err
	}

	// Route DNS. Without this the hostname does not resolve to the tunnel, and
	// the connector runs against a name nobody can reach.
	if err := routeDNS(ctx, opts, creds.ID); err != nil {
		return "", "", err
	}

	return creds.Token, creds.ID, nil
}

// findTunnelByName returns the id of a tunnel with this exact name, or "" when
// none exists. Cloudflare has no get-by-name, so the account's tunnels are
// listed and filtered.
//
// The list is walked page by page: an account with more than 25 tunnels would
// otherwise have its older ones invisible, and the lookup would create a
// duplicate of a tunnel that already exists.
func findTunnelByName(ctx context.Context, token, accountID, name string) (string, error) {
	var tunnels []struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		Deleted bool   `json:"deleted_at"`
	}
	path := fmt.Sprintf("/accounts/%s/cfd_tunnel?is_deleted=false", accountID)
	if err := listAllPages(ctx, token, path, &tunnels); err != nil {
		return "", err
	}
	for _, t := range tunnels {
		if strings.EqualFold(t.Name, name) {
			return t.ID, nil
		}
	}
	return "", nil
}

// mintTunnelToken issues a fresh connector token for an existing tunnel. The
// token is only returned at creation time, so re-using a tunnel means asking
// for a new one.
func mintTunnelToken(ctx context.Context, token, accountID, tunnelID string) (string, error) {
	path := fmt.Sprintf("/accounts/%s/cfd_tunnel/%s/token", accountID, tunnelID)
	var creds tunnelCredentials
	if err := cfRequest(ctx, http.MethodGet, path, token, nil, &creds); err != nil {
		return "", err
	}
	return creds.Token, nil
}

// zoneNameByID reads back a zone's name. routeDNS needs the canonical spelling
// to make the record name relative to it, and the operator's own input cannot
// be trusted for that: they may have typed the hostname where the zone belongs.
func zoneNameByID(ctx context.Context, token, zoneID string) (string, error) {
	var zone struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	path := fmt.Sprintf("/zones/%s", zoneID)
	if err := cfRequest(ctx, http.MethodGet, path, token, nil, &zone); err != nil {
		return "", fmt.Errorf("tunnel.zoneNameByID: %w", err)
	}
	if zone.Name == "" {
		return "", fmt.Errorf("tunnel.zoneNameByID: zone %s returned no name", zoneID)
	}
	return zone.Name, nil
}

// configureTunnelIngress declares the origin for a remotely-managed tunnel.
func configureTunnelIngress(ctx context.Context, token, accountID, tunnelID, hostname string, localPort int) error {
	path := fmt.Sprintf("/accounts/%s/cfd_tunnel/%s/configurations", accountID, tunnelID)
	body := map[string]any{
		"config": map[string]any{
			"ingress": []map[string]any{
				{
					"hostname": hostname,
					"service":  fmt.Sprintf("http://127.0.0.1:%d", localPort),
				},
				{
					// The catch-all is mandatory: Cloudflare rejects a
					// configuration whose last rule has a hostname.
					"service": "http_status:404",
				},
			},
		},
	}
	if err := cfRequest(ctx, http.MethodPut, path, token, body, nil); err != nil {
		return fmt.Errorf("tunnel.configureTunnelIngress: %w", err)
	}
	return nil
}

// FindTunnelIDByName resolves a hostname to the id of the tunnel serving it.
//
// The name is derived the same way ProvisionNamedTunnel derives it, so a repair
// lands on the tunnel the enable created rather than on a similarly named one
// the operator made by hand.
func FindTunnelIDByName(ctx context.Context, opts CloudflareOptions) (string, error) {
	accountID, err := ResolveAccountID(ctx, opts)
	if err != nil {
		return "", err
	}
	name := tunnelNameFromHostname(opts.Hostname)
	id, err := findTunnelByName(ctx, opts.APIToken, accountID, name)
	if err != nil {
		return "", fmt.Errorf("tunnel.FindTunnelIDByName: %w", err)
	}
	if id == "" {
		return "", fmt.Errorf("tunnel.FindTunnelIDByName: no tunnel named %q on this account", name)
	}
	return id, nil
}

// RepairTunnelIngress points an existing tunnel's ingress rule at localPort.
//
// It is the same write configureTunnelIngress performs during provisioning,
// exposed on its own because the mismatch it fixes appears long after the
// enable: the operator changes the gateway's port, or restores a configuration
// made on a different machine, and the tunnel keeps dialling the old origin.
func RepairTunnelIngress(ctx context.Context, opts CloudflareOptions, tunnelID string, localPort int) error {
	accountID, err := ResolveAccountID(ctx, opts)
	if err != nil {
		return err
	}
	return configureTunnelIngress(ctx, opts.APIToken, accountID, tunnelID, opts.Hostname, localPort)
}

// routeDNS creates the CNAME that sends the hostname to the tunnel.
//
// The record is upserted: an operator re-enabling a tunnel after a disable
// would otherwise get a "record already exists" error and be stuck, because
// the disable path deliberately leaves DNS alone so an existing deployment
// keeps resolving.
func routeDNS(ctx context.Context, opts CloudflareOptions, tunnelID string) error {
	zoneID, err := ResolveZoneID(ctx, opts)
	if err != nil {
		return err
	}

	// The zone name is needed again to make the record name relative, so it is
	// read back rather than trusting the operator's spelling. Those differ
	// whenever the operator named the hostname instead of the zone —
	// "ai.helmiau.my.id" as the zone, when the zone on the account is
	// "helmiau.my.id" — and deriving the name from the typed value leaves a
	// fully-qualified name the API rejects.
	zoneName, err := zoneNameByID(ctx, opts.APIToken, zoneID)
	if err != nil {
		return err
	}

	// The tunnel's stable target is <tunnelID>.cfargotunnel.com, which
	// Cloudflare resolves to the tunnel's current edge connections.
	content := tunnelID + ".cfargotunnel.com"
	host := strings.ToLower(strings.TrimSpace(opts.Hostname))
	zone := strings.ToLower(strings.TrimSpace(zoneName))

	// The API wants the record name relative to the zone, not fully qualified.
	name := "@"
	switch {
	case host == zone:
		// The hostname is the zone apex.
	case strings.HasSuffix(host, "."+zone):
		name = strings.TrimSuffix(host, "."+zone)
	default:
		// The hostname sits outside the resolved zone. Writing it as-is would
		// be rejected, so the mismatch is reported rather than guessed at.
		return fmt.Errorf("tunnel.routeDNS: hostname %q is not inside zone %q", opts.Hostname, zoneName)
	}

	// Look for an existing record first so a re-enable updates rather than
	// conflicts.
	var existing []dnsRecord
	listPath := fmt.Sprintf("/zones/%s/dns_records?type=CNAME&name=%s", zoneID, host)
	if err := cfRequest(ctx, http.MethodGet, listPath, opts.APIToken, nil, &existing); err == nil && len(existing) > 0 {
		updatePath := fmt.Sprintf("/zones/%s/dns_records/%s", zoneID, existing[0].ID)
		return cfRequest(ctx, http.MethodPatch, updatePath, opts.APIToken, map[string]any{
			"type":    "CNAME",
			"name":    name,
			"content": content,
			"proxied": true,
		}, nil)
	}

	createPath := fmt.Sprintf("/zones/%s/dns_records", zoneID)
	return cfRequest(ctx, http.MethodPost, createPath, opts.APIToken, map[string]any{
		"type":    "CNAME",
		"name":    name,
		"content": content,
		"proxied": true,
		"ttl":     1, // auto when proxied
	}, nil)
}

// tunnelNameFromHostname derives a Cloudflare-safe tunnel name from a hostname.
// Tunnel names allow lowercase letters, digits and hyphens, and a dot is not
// one of them.
func tunnelNameFromHostname(hostname string) string {
	name := strings.ToLower(strings.TrimSpace(hostname))
	name = strings.ReplaceAll(name, ".", "-")
	if len(name) > 63 {
		name = name[:63]
	}
	return name
}
