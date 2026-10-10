package aws

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type stubLoader struct {
	creds  Credentials
	expiry *time.Time
	err    error

	calls atomic.Int64
	block chan struct{}
}

func (s *stubLoader) load(_ context.Context, _ string) (Credentials, *time.Time, error) {
	s.calls.Add(1)
	if s.block != nil {
		<-s.block
	}
	return s.creds, s.expiry, s.err
}

func fixedNow(t time.Time) func() time.Time { return func() time.Time { return t } }

func noEnv(string) string { return "" }

func envFrom(pairs map[string]string) func(string) string {
	return func(key string) string { return pairs[key] }
}

func TestDetectMode(t *testing.T) {
	tests := []struct {
		name string
		in   ProfileInput
		want CredentialMode
	}{
		{
			name: "profile wins over stray static keys",
			in: ProfileInput{
				AccessKeyID:     "AKIAEXAMPLE",
				SecretAccessKey: "secret",
				Profile:         "prod",
			},
			want: ModeProfile,
		},
		{
			name: "a whitespace-only profile is not a profile",
			in:   ProfileInput{Profile: "   ", AccessKeyID: "AKIA", SecretAccessKey: "secret"},
			want: ModeStatic,
		},
		{
			name: "static keys alone",
			in:   ProfileInput{AccessKeyID: "AKIA", SecretAccessKey: "secret"},
			want: ModeStatic,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := DetectMode(tt.in); got != tt.want {
				t.Errorf("DetectMode() = %q, want %q", got, tt.want)
			}
		})
	}
}

// The region lands in the hostname, so an unvalidated one resolves the host to
// bedrock-runtime.evil.com and ships the signed request, its body and the session token
// to an attacker-chosen origin. That is the whole reason this validates.
func TestResolveRegion(t *testing.T) {
	tests := []struct {
		name    string
		in      ProfileInput
		env     map[string]string
		want    string
		wantErr string
	}{
		{
			name: "connection setting wins over the environment",
			in:   ProfileInput{Region: "ap-southeast-3"},
			env:  map[string]string{"AWS_REGION": "eu-west-1", "AWS_DEFAULT_REGION": "us-west-2"},
			want: "ap-southeast-3",
		},
		{
			name: "AWS_REGION beats AWS_DEFAULT_REGION",
			env:  map[string]string{"AWS_REGION": "eu-west-1", "AWS_DEFAULT_REGION": "us-west-2"},
			want: "eu-west-1",
		},
		{
			name: "falls back to the built-in default",
			want: DefaultRegion,
		},
		{
			name: "trims surrounding whitespace rather than failing",
			in:   ProfileInput{Region: "  us-east-2  "},
			want: "us-east-2",
		},
		{
			// An unvalidated region would resolve the host to
			// bedrock-runtime.evil.com and ship the signed request, its body and the
			// session token to an attacker-chosen origin. That is why it validates.
			name:    "rejects a host-shaped region",
			in:      ProfileInput{Region: "evil.com/x"},
			wantErr: "build the Bedrock hostname",
		},
		{
			name:    "rejects uppercase, which no real region uses",
			in:      ProfileInput{Region: "US-EAST-1"},
			wantErr: "lowercase letters, digits and hyphens",
		},
		{
			name:    "rejects an over-long region",
			in:      ProfileInput{Region: strings.Repeat("a", 40)},
			wantErr: "invalid region",
		},
		{
			// Naming the source matters: a bad AWS_REGION in the operator's shell
			// otherwise reads as a dashboard problem and sends them to the wrong place.
			name:    "names the environment as the source",
			env:     map[string]string{"AWS_REGION": "not a region"},
			wantErr: "AWS_REGION environment variable",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			loader := &stubLoader{}
			r := NewResolverWithLoader(loader.load, envFrom(tt.env), fixedNow(time.Now()))
			got, err := r.ResolveRegion(tt.in)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("ResolveRegion() = %q, want error containing %q", got, tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("error = %v, want it to contain %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ResolveRegion() error = %v", err)
			}
			if got != tt.want {
				t.Errorf("ResolveRegion() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestResolveStatic(t *testing.T) {
	tests := []struct {
		name    string
		in      ProfileInput
		want    Credentials
		wantErr string
	}{
		{
			name: "long-lived IAM key",
			in: ProfileInput{
				AccessKeyID:     "AKIAIOSFODNN7EXAMPLE",
				SecretAccessKey: "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY",
			},
			want: Credentials{
				AccessKeyID:     "AKIAIOSFODNN7EXAMPLE",
				SecretAccessKey: "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY",
			},
		},
		{
			name: "STS key with its session token",
			in: ProfileInput{
				AccessKeyID:     "ASIAIOSFODNN7EXAMPLE",
				SecretAccessKey: "secret",
				SessionToken:    "FwoGZXIvYXdzEExample",
			},
			want: Credentials{
				AccessKeyID:     "ASIAIOSFODNN7EXAMPLE",
				SecretAccessKey: "secret",
				SessionToken:    "FwoGZXIvYXdzEExample",
			},
		},
		{
			name:    "temporary key without a session token is unusable",
			in:      ProfileInput{AccessKeyID: "ASIAIOSFODNN7EXAMPLE", SecretAccessKey: "secret"},
			wantErr: "temporary AWS access key",
		},
		{
			name:    "missing secret",
			in:      ProfileInput{AccessKeyID: "AKIAIOSFODNN7EXAMPLE"},
			wantErr: "static credentials are incomplete",
		},
		{
			name:    "missing key id",
			in:      ProfileInput{SecretAccessKey: "secret"},
			wantErr: "static credentials are incomplete",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			loader := &stubLoader{err: errors.New("the static path must not reach the profile loader")}
			r := NewResolverWithLoader(loader.load, noEnv, fixedNow(time.Now()))

			got, err := r.Resolve(context.Background(), tt.in)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("Resolve() = %+v, want error containing %q", got, tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("error = %v, want it to contain %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Resolve() error = %v", err)
			}
			if got.Credentials != tt.want {
				t.Errorf("Credentials = %+v, want %+v", got.Credentials, tt.want)
			}
			if got.Mode != ModeStatic {
				t.Errorf("Mode = %q, want %q", got.Mode, ModeStatic)
			}
			if got.ExpiresAt != nil {
				t.Errorf("ExpiresAt = %v, want nil for static keys", got.ExpiresAt)
			}
			if loader.calls.Load() != 0 {
				t.Errorf("profile loader called %d times for static credentials", loader.calls.Load())
			}
		})
	}
}

// Static resolution is deliberately uncached: a rotated secret must be picked up on the
// next request, and two connections sharing an access key id must never see each other's
// secret.
func TestResolveStaticIsNotCached(t *testing.T) {
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	loader := &stubLoader{}
	r := NewResolverWithLoader(loader.load, noEnv, fixedNow(now))

	first, err := r.Resolve(context.Background(), ProfileInput{AccessKeyID: "AKIA1", SecretAccessKey: "old"})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	second, err := r.Resolve(context.Background(), ProfileInput{AccessKeyID: "AKIA1", SecretAccessKey: "new"})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if first.Credentials.SecretAccessKey == second.Credentials.SecretAccessKey {
		t.Error("Resolve() served a rotated static secret from cache")
	}
}

func TestResolveProfile(t *testing.T) {
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	expiry := now.Add(2 * time.Hour)
	loader := &stubLoader{
		creds:  Credentials{AccessKeyID: "ASIAFROMSSO", SecretAccessKey: "secret", SessionToken: "token"},
		expiry: &expiry,
	}
	r := NewResolverWithLoader(loader.load, noEnv, fixedNow(now))

	got, err := r.Resolve(context.Background(), ProfileInput{Profile: "prod", Region: "eu-central-1"})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if got.Mode != ModeProfile {
		t.Errorf("Mode = %q, want %q", got.Mode, ModeProfile)
	}
	if got.Credentials.AccessKeyID != "ASIAFROMSSO" || got.Credentials.SessionToken != "token" {
		t.Errorf("Credentials = %+v, want the loader's profile credentials", got.Credentials)
	}
	if got.Region != "eu-central-1" {
		t.Errorf("Region = %q, want eu-central-1", got.Region)
	}
	// Temporary credentials stop being trusted RefreshLeadMS early, so an in-flight
	// request cannot outlive the key that signed it.
	if want := expiry.Add(-RefreshLeadMS); !got.ExpiresAt.Equal(want) {
		t.Errorf("ExpiresAt = %v, want %v", got.ExpiresAt, want)
	}
}

func TestResolveProfileCachesUntilExpiry(t *testing.T) {
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	loader := &stubLoader{
		creds: Credentials{AccessKeyID: "ASIA1", SecretAccessKey: "secret"},
	}
	current := now
	r := NewResolverWithLoader(loader.load, noEnv, func() time.Time { return current })

	for range 3 {
		if _, err := r.Resolve(context.Background(), ProfileInput{Profile: "prod"}); err != nil {
			t.Fatalf("Resolve() error = %v", err)
		}
	}
	if loader.calls.Load() != 1 {
		t.Errorf("loader called %d times, want 1: a profile credential with no declared expiry gets a short cache floor", loader.calls.Load())
	}

	// Past the NoExpiryTTL floor the entry must re-resolve, so a revoked profile stops
	// working within the minute rather than at the end of the process.
	current = now.Add(NoExpiryTTL + time.Second)
	if _, err := r.Resolve(context.Background(), ProfileInput{Profile: "prod"}); err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if loader.calls.Load() != 2 {
		t.Errorf("loader called %d times, want 2 after the no-expiry TTL", loader.calls.Load())
	}
}

func TestResolveProfileRejectsBadInput(t *testing.T) {
	tests := []struct {
		name    string
		profile string
		wantErr string
	}{
		{
			name:    "path traversal is never a real profile",
			profile: "../../etc/passwd",
			wantErr: "invalid AWS profile name",
		},
		{
			name:    "a shell metacharacter never names a profile",
			profile: "prod; rm -rf /",
			wantErr: "invalid AWS profile name",
		},
		{
			name:    "over 64 characters",
			profile: strings.Repeat("a", 65),
			wantErr: "invalid AWS profile name",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			loader := &stubLoader{err: errors.New("must not be reached")}
			r := NewResolverWithLoader(loader.load, noEnv, fixedNow(time.Now()))

			_, err := r.Resolve(context.Background(), ProfileInput{Profile: tt.profile})
			if err == nil {
				t.Fatalf("Resolve() succeeded, want error containing %q", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %v, want it to contain %q", err, tt.wantErr)
			}
			if loader.calls.Load() != 0 {
				t.Errorf("loader called %d times for a rejected profile name", loader.calls.Load())
			}
		})
	}
}

// A failure must never be cached: the operator's next attempt should retry, not replay
// the error for the length of the cache window.
func TestResolveProfileDoesNotCacheFailures(t *testing.T) {
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	loader := &stubLoader{err: errors.New("expired SSO token")}
	r := NewResolverWithLoader(loader.load, noEnv, fixedNow(now))

	_, err := r.Resolve(context.Background(), ProfileInput{Profile: "prod"})
	if err == nil {
		t.Fatal("Resolve() succeeded, want the loader error")
	}
	if !strings.Contains(err.Error(), "aws sso login --profile prod") {
		t.Errorf("error = %v, want it to point at the SSO login fix", err)
	}
	if _, err := r.Resolve(context.Background(), ProfileInput{Profile: "prod"}); err == nil {
		t.Fatal("second Resolve() succeeded, want the loader error replayed from cache")
	}
	if loader.calls.Load() != 2 {
		t.Errorf("loader called %d times, want 2: failures must be retried", loader.calls.Load())
	}
}

// A burst of concurrent requests on one profile must collapse into a single SSO round
// trip, or an SSO portal login becomes a thundering herd.
func TestResolveProfileCollapsesConcurrentRequests(t *testing.T) {
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	loader := &stubLoader{
		creds: Credentials{AccessKeyID: "ASIA1", SecretAccessKey: "secret"},
		block: make(chan struct{}),
	}
	r := NewResolverWithLoader(loader.load, noEnv, fixedNow(now))

	const concurrency = 12
	var wg sync.WaitGroup
	errs := make([]error, concurrency)
	for i := range concurrency {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = r.Resolve(context.Background(), ProfileInput{Profile: "prod"})
		}()
	}
	// Let every goroutine reach the resolver before the single load completes.
	for loader.calls.Load() < 1 {
		time.Sleep(time.Millisecond)
	}
	close(loader.block)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d: Resolve() error = %v", i, err)
		}
	}
	if loader.calls.Load() != 1 {
		t.Errorf("loader called %d times, want 1 for %d concurrent requests", loader.calls.Load(), concurrency)
	}
}

// Two connections on one profile must not inherit each other's region: the region is
// part of the credential scope, so a shared one signs with the wrong scope.
func TestResolveProfileKeepsRegionPerConnection(t *testing.T) {
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	loader := &stubLoader{
		creds: Credentials{AccessKeyID: "ASIA1", SecretAccessKey: "secret"},
		block: make(chan struct{}),
	}
	r := NewResolverWithLoader(loader.load, noEnv, fixedNow(now))

	var wg sync.WaitGroup
	results := make([]Resolved, 2)
	inputs := []ProfileInput{
		{Profile: "prod", Region: "us-east-1"},
		{Profile: "prod", Region: "eu-west-1"},
	}
	for i, in := range inputs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := r.Resolve(context.Background(), in)
			if err != nil {
				t.Errorf("Resolve(%s) error = %v", in.Region, err)
				return
			}
			results[i] = got
		}()
	}
	for loader.calls.Load() < 1 {
		time.Sleep(time.Millisecond)
	}
	close(loader.block)
	wg.Wait()

	if results[0].Region != "us-east-1" || results[1].Region != "eu-west-1" {
		t.Errorf("regions = (%q, %q), want each connection to keep its own",
			results[0].Region, results[1].Region)
	}
}

// A hung GetRoleCredentials would otherwise block every request on that profile forever,
// because they all await the same in-flight call.
func TestResolveProfileTimeout(t *testing.T) {
	loader := &blockingLoader{release: make(chan struct{})}
	t.Cleanup(func() { close(loader.release) })
	r := NewResolverWithLoader(loader.load, noEnv, fixedNow(time.Now()))

	start := time.Now()
	_, err := r.Resolve(context.Background(), ProfileInput{Profile: "prod"})
	if err == nil {
		t.Fatal("Resolve() succeeded, want a timeout")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Errorf("error = %v, want a timeout message", err)
	}
	if elapsed := time.Since(start); elapsed < ResolveTimeout {
		t.Errorf("Resolve() returned after %v, before the %v bound", elapsed, ResolveTimeout)
	}
}

// A caller that gives up first must not have its result cached: the next request has to
// resolve again rather than inherit the abandoned credential.
type blockingLoader struct {
	release chan struct{}
}

func (b *blockingLoader) load(ctx context.Context, _ string) (Credentials, *time.Time, error) {
	select {
	case <-b.release:
	case <-ctx.Done():
	}
	return Credentials{}, nil, ctx.Err()
}

func TestResolveHonoursCallerCancellation(t *testing.T) {
	loader := &blockingLoader{release: make(chan struct{})}
	t.Cleanup(func() { close(loader.release) })
	r := NewResolverWithLoader(loader.load, noEnv, fixedNow(time.Now()))

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	if _, err := r.Resolve(ctx, ProfileInput{Profile: "prod"}); err == nil {
		t.Fatal("Resolve() succeeded, want the caller's cancellation")
	}
	if elapsed := time.Since(start); elapsed > ResolveTimeout {
		t.Errorf("Resolve() took %v; a cancelled caller must not wait out the %v bound", elapsed, ResolveTimeout)
	}
}
