package aws

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/config"
)

// CredentialMode is how a Bedrock connection supplies credentials.
type CredentialMode string

const (
	// ModeStatic carries the keys on the connection itself. Nothing is refreshable: when
	// temporary keys expire the user must paste new ones.
	ModeStatic CredentialMode = "static"
	// ModeProfile defers to the local AWS config, so `aws sso login --profile X`
	// sessions are picked up and re-resolved after expiry.
	ModeProfile CredentialMode = "profile"
)

const (
	// RefreshLeadMS is how early a temporary credential stops being trusted, so an
	// in-flight request cannot be signed with a key that dies mid-stream.
	RefreshLeadMS = 5 * time.Minute

	// NoExpiryTTL bounds how long a credential with no declared expiry is cached.
	// `fromIni` returns none for a plain aws_access_key_id profile in
	// ~/.aws/credentials, which is common. Treating those as instantly stale re-read and
	// re-parsed ~/.aws on every single request; pinning them for the process lifetime
	// would hide a revoked profile forever. One minute bounds both.
	NoExpiryTTL = time.Minute

	// ResolveTimeout bounds a profile/SSO resolution. Without it, one hung
	// GetRoleCredentials or ~/.aws read blocks every request on that profile forever,
	// because they all await the same in-flight call.
	ResolveTimeout = 10 * time.Second

	// DefaultRegion is Bedrock's fallback when nothing else names one.
	DefaultRegion = "us-east-1"
)

var (
	// regionPattern validates a region because it is interpolated into the request
	// hostname. "evil.com/x" would otherwise resolve the host to
	// "bedrock-runtime.evil.com" and ship the signed request, its body and the session
	// token to an attacker-chosen origin. Real regions are lowercase alphanumerics and
	// hyphens, e.g. "us-east-1", "ap-southeast-3".
	regionPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,30}$`)

	// profilePattern is applied for the same reason as the region: a profile name is
	// handed to a credential resolver that will follow `source_profile` role chains and
	// run a `credential_process` subprocess, and it arrives as unschema'd provider data.
	// The characters follow what the SDK's own ini parser resolves — word characters and
	// - @ + . % : / — plus spaces, because a plain [NAME] section in
	// ~/.aws/credentials may contain them. "/" is left out: the name is only a lookup
	// key, but a path-shaped one is never a real profile.
	profilePattern = regexp.MustCompile(`^[\w@+.%: -]{1,64}$`)
)

// StaticInput is the static-credential half of a connection's provider data.
type StaticInput struct {
	AccessKeyID  string
	SessionToken string
}

// ProfileInput is what the resolver needs to read one Bedrock connection.
type ProfileInput struct {
	// AccessKeyID is the AWS access key id from provider-specific data.
	AccessKeyID string
	// SecretAccessKey is the connection's API key — the AWS secret access key.
	SecretAccessKey string
	// SessionToken is set when the keys came from STS.
	SessionToken string
	// Profile, when set, wins over any stray static keys: a user who filled in a profile
	// meant to use it.
	Profile string
	// Region is the connection's explicit region setting.
	Region string
}

// Resolved is usable AWS credentials plus the region to sign for.
type Resolved struct {
	Credentials Credentials
	Region      string
	Mode        CredentialMode
	// ExpiresAt is when this entry stops being trusted, or nil for static keys.
	ExpiresAt *time.Time
}

// Loader resolves one AWS profile to credentials. It is the seam the tests drive so the
// suite needs no AWS account, no ~/.aws, and no network.
type Loader func(ctx context.Context, profile string) (Credentials, *time.Time, error)

type cacheEntry struct {
	// inflight is set while a resolution is running, so a burst of concurrent requests
	// on one profile collapses into a single SSO round trip.
	inflight chan struct{}
	creds    Credentials
	expires  time.Time
}

// Resolver caches resolved profile credentials. Static resolution is never cached: it
// performs no I/O, and caching it made a rotated secret invisible and let two
// connections sharing an access key id serve each other's secret.
type Resolver struct {
	load   Loader
	env    func(string) string
	now    func() time.Time
	mu     sync.Mutex
	cache  map[string]*cacheEntry
	region string
}

// NewResolver builds a Resolver using the AWS SDK's shared-config profile loader.
func NewResolver() *Resolver {
	return NewResolverWithLoader(sdkProfileLoader, os.Getenv, time.Now)
}

// NewResolverWithLoader builds a Resolver with injected seams, for tests.
func NewResolverWithLoader(load Loader, env func(string) string, now func() time.Time) *Resolver {
	return &Resolver{load: load, env: env, now: now, cache: make(map[string]*cacheEntry)}
}

func sdkProfileLoader(ctx context.Context, profile string) (Credentials, *time.Time, error) {
	opts := []func(*config.LoadOptions) error{config.WithSharedConfigProfile(profile)}
	cfg, err := config.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return Credentials{}, nil, fmt.Errorf("load AWS profile %q: %w", profile, err)
	}
	// Retrieve is lazy and does the SSO token read plus GetRoleCredentials here.
	awsCreds, err := cfg.Credentials.Retrieve(ctx)
	if err != nil {
		return Credentials{}, nil, fmt.Errorf("retrieve AWS credentials for profile %q: %w", profile, err)
	}
	creds := Credentials{
		AccessKeyID:     awsCreds.AccessKeyID,
		SecretAccessKey: awsCreds.SecretAccessKey,
		SessionToken:    awsCreds.SessionToken,
	}
	if !awsCreds.CanExpire {
		return creds, nil, nil
	}
	expiry := awsCreds.Expires
	return creds, &expiry, nil
}

// DetectMode reports which mode a connection is configured for.
func DetectMode(in ProfileInput) CredentialMode {
	if strings.TrimSpace(in.Profile) != "" {
		return ModeProfile
	}
	return ModeStatic
}

// ResolveRegion returns the region to sign for, honouring the connection setting first,
// then AWS_REGION, then AWS_DEFAULT_REGION, then Bedrock's default.
//
// It deliberately does not read the profile's own region — the SDK credential providers
// do not surface it, and silently sending traffic to a region the operator did not
// choose is worse than making them state it.
func (r *Resolver) ResolveRegion(in ProfileInput) (string, error) {
	candidates := []struct{ value, source string }{
		{in.Region, "the connection region setting"},
		{r.env("AWS_REGION"), "the AWS_REGION environment variable"},
		{r.env("AWS_DEFAULT_REGION"), "the AWS_DEFAULT_REGION environment variable"},
		{DefaultRegion, "the built-in default"},
	}
	for _, candidate := range candidates {
		if candidate.value == "" {
			continue
		}
		// Trim first: a region pasted into the dashboard with a trailing space is a typo
		// to absorb, not a reason to fail every request. Naming the source matters too —
		// a bad AWS_REGION in the operator's shell otherwise reads as a dashboard problem
		// and sends them looking in the wrong place.
		region := strings.TrimSpace(candidate.value)
		if !regionPattern.MatchString(region) {
			return "", fmt.Errorf("aws: invalid region %q from %s; a region looks like %q: "+
				"lowercase letters, digits and hyphens only. It is used to build the Bedrock "+
				"hostname, so anything else is rejected rather than sent to an unintended host",
				candidate.value, candidate.source, "us-east-1")
		}
		return region, nil
	}
	return "", errors.New("aws: no region could be resolved")
}

// readStatic validates and returns the keys carried on the connection.
func readStatic(in ProfileInput) (Credentials, error) {
	accessKeyID := strings.TrimSpace(in.AccessKeyID)
	secret := strings.TrimSpace(in.SecretAccessKey)
	if accessKeyID == "" || secret == "" {
		return Credentials{}, errors.New("aws: Bedrock static credentials are incomplete; " +
			"set the AWS secret access key as the provider API key and the AWS access key id " +
			"as the access key id, or set a profile to use a local AWS profile instead")
	}
	// Temporary keys (ASIA…) are useless without their session token; catching it here
	// turns a confusing upstream SignatureDoesNotMatch into a fixable message.
	if strings.HasPrefix(accessKeyID, "ASIA") && strings.TrimSpace(in.SessionToken) == "" {
		return Credentials{}, errors.New("aws: this looks like a temporary AWS access key (ASIA…) " +
			"but no session token was provided; add one, or use a profile so credentials are " +
			"resolved and refreshed automatically")
	}
	return Credentials{
		AccessKeyID:     accessKeyID,
		SecretAccessKey: secret,
		SessionToken:    strings.TrimSpace(in.SessionToken),
	}, nil
}

// Resolve returns usable AWS credentials plus the region to sign for.
//
// The region is resolved per call and never cached alongside the credentials, so two
// connections on one profile cannot inherit each other's region and sign with the wrong
// credential scope.
func (r *Resolver) Resolve(ctx context.Context, in ProfileInput) (Resolved, error) {
	region, err := r.ResolveRegion(in)
	if err != nil {
		return Resolved{}, err
	}

	mode := DetectMode(in)
	if mode == ModeStatic {
		creds, err := readStatic(in)
		if err != nil {
			return Resolved{}, err
		}
		return Resolved{Credentials: creds, Region: region, Mode: mode}, nil
	}

	profile := strings.TrimSpace(in.Profile)
	if !profilePattern.MatchString(profile) {
		return Resolved{}, fmt.Errorf("aws: invalid AWS profile name %q; use the profile's name as "+
			"it appears in ~/.aws/config or ~/.aws/credentials: letters, digits, spaces and "+
			"%%_@+.%%: and a hyphen, up to 64 characters", in.Profile)
	}

	creds, expiresAt, err := r.resolveProfile(ctx, profile)
	if err != nil {
		return Resolved{}, err
	}
	return Resolved{
		Credentials: creds,
		Region:      region,
		Mode:        mode,
		ExpiresAt:   expiresAt,
	}, nil
}

func (r *Resolver) resolveProfile(ctx context.Context, profile string) (Credentials, *time.Time, error) {
	key := "profile:" + profile
	now := r.now()

	r.mu.Lock()
	entry, cached := r.cache[key]
	if cached && entry.inflight == nil && entry.expires.After(now) {
		creds := entry.creds
		expiry := entry.expires
		r.mu.Unlock()
		return creds, &expiry, nil
	}
	if cached && entry.inflight != nil {
		// A resolution is already running for this profile; wait on it rather than
		// starting a second SSO round trip. The result is written by the goroutine that
		// owns the entry, never from inside the waiter.
		wait := entry.inflight
		r.mu.Unlock()
		select {
		case <-wait:
		case <-ctx.Done():
			return Credentials{}, nil, fmt.Errorf("aws: waiting for AWS profile %q: %w", profile, ctx.Err())
		}
		r.mu.Lock()
		entry, cached = r.cache[key]
		if cached && entry.inflight == nil && entry.expires.After(now) {
			creds := entry.creds
			expiry := entry.expires
			r.mu.Unlock()
			return creds, &expiry, nil
		}
		r.mu.Unlock()
		// The owning resolution failed, or this waiter raced a stale entry. Fall through
		// and try again; the failure is not cached, so the retry is a real re-resolve.
	}

	done := make(chan struct{})
	r.cache[key] = &cacheEntry{inflight: done}
	r.mu.Unlock()

	creds, err := r.loadProfile(ctx, profile)

	r.mu.Lock()
	delete(r.cache, key)
	if err == nil {
		r.cache[key] = &cacheEntry{creds: creds.value, expires: expiresAtFor(creds.expiry, now)}
	}
	r.mu.Unlock()
	close(done)
	if err != nil {
		return Credentials{}, nil, err
	}

	// The caller is told when the credential STOPS BEING TRUSTED, not when AWS says it
	// dies: the refresh lead is what keeps an in-flight request from outliving its key.
	trustedUntil := expiresAtFor(creds.expiry, now)
	return creds.value, &trustedUntil, nil
}

type loadedCredentials struct {
	value  Credentials
	expiry *time.Time
}

// expiresAtFor drops a temporary credential early enough that an in-flight request
// cannot outlive the key that signed it. A credential with no declared expiry gets a
// short floor rather than "now": returning "now" made the entry fail its own
// expires > currentTime check immediately, so every request re-read and re-parsed
// ~/.aws from disk on the hot path.
func expiresAtFor(expiry *time.Time, now time.Time) time.Time {
	if expiry == nil {
		return now.Add(NoExpiryTTL)
	}
	return expiry.Add(-RefreshLeadMS)
}

func (r *Resolver) loadProfile(ctx context.Context, profile string) (loadedCredentials, error) {
	loadCtx, cancel := context.WithTimeout(ctx, ResolveTimeout)
	defer cancel()

	creds, expiry, err := r.load(loadCtx, profile)
	if err != nil {
		if errors.Is(loadCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil {
			return loadedCredentials{}, fmt.Errorf("aws: resolving AWS profile %q timed out after %s: %w",
				profile, ResolveTimeout, err)
		}
		// The overwhelmingly common cause is an expired or absent SSO session, and the
		// SDK's own message rarely says so plainly. Point at the fix.
		return loadedCredentials{}, fmt.Errorf("aws: could not resolve AWS credentials for profile %q: %w; "+
			"if this profile uses IAM Identity Center, run: aws sso login --profile %s", profile, err, profile)
	}
	if creds.AccessKeyID == "" || creds.SecretAccessKey == "" {
		return loadedCredentials{}, fmt.Errorf("aws: AWS profile %q resolved without usable credentials; "+
			"check that the profile exists in ~/.aws/config and grants Bedrock access", profile)
	}
	return loadedCredentials{value: creds, expiry: expiry}, nil
}
