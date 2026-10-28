package translator

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"syscall"
	"testing"
)

// mockTimeoutError is a test fixture: a net.Error that always reports timeout.
type mockTimeoutError struct{}

func (m *mockTimeoutError) Error() string   { return "i/o timeout" }
func (m *mockTimeoutError) Timeout() bool   { return true }
func (m *mockTimeoutError) Temporary() bool { return true }

// ============================================================================
// Contract Test Suite: TestImagePrefetch_ClassifiedErrors
// ============================================================================

func TestImagePrefetch_ClassifiedErrors(t *testing.T) {
	t.Run("InvalidScheme", func(t *testing.T) {
		invalidURLs := []string{
			"ftp://example.com/assets/picture.png",
			"file:///etc/passwd",
			"file://c:/windows/system32/cmd.exe",
			"data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAUA",
			"javascript:alert(1)",
			"gopher://evil.com/1image",
			"blob:https://example.com/uuid-1234",
			"http//malformed.url/image.jpg",
			"://empty-scheme/pic.jpg",
			"",
		}

		for _, rawURL := range invalidURLs {
			status, err := ValidatePrefetchURL(rawURL)
			if status != StatusPrefetchBadRequest {
				t.Errorf("url %q: expected HTTP %d, got %d", rawURL, StatusPrefetchBadRequest, status)
			}
			if err == nil {
				t.Errorf("url %q: expected error, got nil", rawURL)
			}

			classifiedStatus, category, _ := ClassifyPrefetchError(err)
			if classifiedStatus != StatusPrefetchBadRequest {
				t.Errorf("classified status for %q: expected %d, got %d", rawURL, StatusPrefetchBadRequest, classifiedStatus)
			}
			if category != "invalid_scheme" && category != "ssrf_blocked" {
				t.Errorf("classified category for %q: expected invalid_scheme or ssrf_blocked, got %s", rawURL, category)
			}
		}
	})

	t.Run("SSRFBlocked", func(t *testing.T) {
		ssrfTargets := []string{
			"http://127.0.0.1/test.png",
			"https://127.0.0.254/image.jpg",
			"http://169.254.169.254/latest/meta-data/",
			"http://169.254.1.1/device.png",
			"http://10.0.0.1/internal-chart.png",
			"https://10.255.255.255/secret.webp",
			"http://192.168.1.1/admin-banner.png",
			"http://172.16.0.5/infra.png",
			"http://172.31.255.255/intranet.gif",
			"http://0.0.0.0/pic.jpg",
			"http://[::1]/image.png",
			"http://[fe80::1]/link-local.png",
			"http://localhost/private.png",
			"http://metadata.google.internal/computeMetadata/v1/",
		}

		for _, target := range ssrfTargets {
			status, err := ValidatePrefetchURL(target)
			if status != StatusPrefetchBadRequest {
				t.Errorf("target %q: expected HTTP %d, got %d", target, StatusPrefetchBadRequest, status)
			}
			if !errors.Is(err, ErrPrefetchSSRFBlocked) {
				t.Errorf("target %q: expected ErrPrefetchSSRFBlocked, got %v", target, err)
			}

			classifiedStatus, category, _ := ClassifyPrefetchError(err)
			if classifiedStatus != StatusPrefetchBadRequest {
				t.Errorf("classified status for SSRF %q: expected %d, got %d", target, StatusPrefetchBadRequest, classifiedStatus)
			}
			if category != "ssrf_blocked" {
				t.Errorf("category for SSRF %q: expected ssrf_blocked, got %s", target, category)
			}
		}

		// Also verify classification of errors originating from SafeDialer
		dialerErr := errors.New("dial tcp 127.0.0.1:80: request to private/reserved IP blocked by SSRF protection")
		status, category, mappedErr := ClassifyPrefetchError(dialerErr)
		if status != StatusPrefetchBadRequest {
			t.Errorf("dialer SSRF error: expected HTTP %d, got %d", StatusPrefetchBadRequest, status)
		}
		if category != "ssrf_blocked" {
			t.Errorf("dialer SSRF error category: expected ssrf_blocked, got %s", category)
		}
		if !errors.Is(mappedErr, ErrPrefetchSSRFBlocked) {
			t.Errorf("dialer SSRF mapped error: expected ErrPrefetchSSRFBlocked, got %v", mappedErr)
		}
	})

	t.Run("NetworkError", func(t *testing.T) {
		networkCases := []struct {
			name string
			err  error
		}{
			{
				name: "connection_refused",
				err:  syscall.ECONNREFUSED,
			},
			{
				name: "connection_reset",
				err:  syscall.ECONNRESET,
			},
			{
				name: "host_unreachable",
				err:  syscall.EHOSTUNREACH,
			},
			{
				name: "dns_nxdomain",
				err: &net.DNSError{
					Err:        "no such host",
					Name:       "nonexistent-image-cdn.invalid",
					IsNotFound: true,
				},
			},
			{
				name: "op_error_dial",
				err: &net.OpError{
					Op:  "dial",
					Net: "tcp",
					Err: syscall.ECONNREFUSED,
				},
			},
			{
				name: "wrapped_network_error",
				err:  fmt.Errorf("remote fetch failed: %w", syscall.ECONNREFUSED),
			},
		}

		for _, tc := range networkCases {
			status, category, mappedErr := ClassifyPrefetchError(tc.err)
			if status != StatusPrefetchBadGateway {
				t.Errorf("%s: expected HTTP %d, got %d", tc.name, StatusPrefetchBadGateway, status)
			}
			if category != "egress_network_error" {
				t.Errorf("%s: expected category egress_network_error, got %s", tc.name, category)
			}
			if !errors.Is(mappedErr, ErrPrefetchNetworkError) {
				t.Errorf("%s: expected ErrPrefetchNetworkError, got %v", tc.name, mappedErr)
			}
		}
	})

	t.Run("Timeout", func(t *testing.T) {
		timeoutCases := []struct {
			name string
			err  error
		}{
			{
				name: "context_deadline_exceeded",
				err:  context.DeadlineExceeded,
			},
			{
				name: "wrapped_context_deadline",
				err:  fmt.Errorf("http client timed out: %w", context.DeadlineExceeded),
			},
			{
				name: "net_timeout_interface",
				err:  &mockTimeoutError{},
			},
			{
				name: "op_error_timeout",
				err: &net.OpError{
					Op:  "read",
					Net: "tcp",
					Err: &mockTimeoutError{},
				},
			},
		}

		for _, tc := range timeoutCases {
			status, category, mappedErr := ClassifyPrefetchError(tc.err)
			if status != StatusPrefetchGatewayTimeout {
				t.Errorf("%s: expected HTTP %d, got %d", tc.name, StatusPrefetchGatewayTimeout, status)
			}
			if category != "gateway_timeout" {
				t.Errorf("%s: expected category gateway_timeout, got %s", tc.name, category)
			}
			if !errors.Is(mappedErr, ErrPrefetchTimeout) {
				t.Errorf("%s: expected ErrPrefetchTimeout, got %v", tc.name, mappedErr)
			}
		}
	})

	t.Run("DownstreamCancel", func(t *testing.T) {
		cancelCases := []struct {
			name string
			err  error
		}{
			{
				name: "direct_context_canceled",
				err:  context.Canceled,
			},
			{
				name: "wrapped_context_canceled",
				err:  fmt.Errorf("client closed request connection: %w", context.Canceled),
			},
		}

		for _, tc := range cancelCases {
			status, category, mappedErr := ClassifyPrefetchError(tc.err)
			// According to G03 standard, downstream cancellation must be classified as internal 499,
			// stopping the prefetch routine immediately without writing to a closed socket.
			if status != StatusPrefetchClientClosedRequest {
				t.Errorf("%s: expected status %d (499 Client Closed Request), got %d", tc.name, StatusPrefetchClientClosedRequest, status)
			}
			if category != "downstream_canceled" {
				t.Errorf("%s: expected category downstream_canceled, got %s", tc.name, category)
			}
			if !errors.Is(mappedErr, ErrPrefetchClientCanceled) {
				t.Errorf("%s: expected ErrPrefetchClientCanceled, got %v", tc.name, mappedErr)
			}
		}
	})

	t.Run("Base64OverheadCalculation", func(t *testing.T) {
		// Base64 expansion formula: ((rawLen + 2) / 3) * 4
		edgeCases := []struct {
			rawBytes       int64
			expectedBase64 int64
		}{
			{rawBytes: 0, expectedBase64: 0},
			{rawBytes: 1, expectedBase64: 4},       // 1 byte -> 4 chars (with ==)
			{rawBytes: 2, expectedBase64: 4},       // 2 bytes -> 4 chars (with =)
			{rawBytes: 3, expectedBase64: 4},       // 3 bytes -> 4 chars (no padding)
			{rawBytes: 4, expectedBase64: 8},       // 4 bytes -> 8 chars (with ==)
			{rawBytes: 5, expectedBase64: 8},       // 5 bytes -> 8 chars (with =)
			{rawBytes: 6, expectedBase64: 8},       // 6 bytes -> 8 chars (no padding)
			{rawBytes: 1024, expectedBase64: 1368}, // 1024 bytes -> 1368 chars
		}

		for _, tc := range edgeCases {
			actual := EstimateBase64PayloadSize(tc.rawBytes)
			if actual != tc.expectedBase64 {
				t.Errorf("rawBytes %d: expected base64 len %d, got %d", tc.rawBytes, tc.expectedBase64, actual)
			}
		}

		// Verify overhead ratio is ~1.33333333...
		largeRaw := int64(15 * 1024 * 1024) // 15MB
		largeBase64 := EstimateBase64PayloadSize(largeRaw)
		ratio := float64(largeBase64) / float64(largeRaw)
		if ratio < 1.33 || ratio > 1.34 {
			t.Errorf("expected expansion ratio ~1.3333, got %f", ratio)
		}

		// Verify MaxRawSizeForBase64Limit for typical downstream limits (5MB, 10MB, 20MB)
		limits := []int64{
			5 * 1024 * 1024,  // 5MB
			10 * 1024 * 1024, // 10MB
			20 * 1024 * 1024, // 20MB
		}

		for _, limit := range limits {
			maxRaw := MaxRawSizeForBase64Limit(limit)
			estAtMax := EstimateBase64PayloadSize(maxRaw)
			if estAtMax > limit {
				t.Errorf("limit %d: MaxRaw %d produces base64 size %d which exceeds limit", limit, maxRaw, estAtMax)
			}

			// Verify that exceeding maxRaw by 4 bytes will exceed or reach boundary
			estOverMax := EstimateBase64PayloadSize(maxRaw + 4)
			if estOverMax <= limit {
				t.Errorf("limit %d: MaxRaw %d was not optimal, maxRaw+4 still fits (%d <= %d)", limit, maxRaw, estOverMax, limit)
			}

			// Test payload exceed error classification
			overLimitErr := fmt.Errorf("%w: requested %d bytes", ErrPrefetchPayloadExceeded, estOverMax)
			status, category, mappedErr := ClassifyPrefetchError(overLimitErr)
			if status != StatusPrefetchBadRequest {
				t.Errorf("payload exceed error: expected HTTP %d, got %d", StatusPrefetchBadRequest, status)
			}
			if category != "payload_exceeded" {
				t.Errorf("payload exceed category: expected payload_exceeded, got %s", category)
			}
			if !errors.Is(mappedErr, ErrPrefetchPayloadExceeded) {
				t.Errorf("payload exceed mapped error: expected ErrPrefetchPayloadExceeded, got %v", mappedErr)
			}
		}
	})

	t.Run("InvalidMIME", func(t *testing.T) {
		invalidMIMEs := []string{
			"text/html",
			"text/html; charset=utf-8",
			"application/json",
			"application/x-executable",
			"application/octet-stream",
			"video/mp4",
			"audio/mpeg",
			"image/svg+xml", // SVG may contain embedded scripts (XSS/SSRF vector)
			"",
		}

		for _, mime := range invalidMIMEs {
			status, err := ValidatePrefetchMIME(mime)
			if status != StatusPrefetchBadRequest {
				t.Errorf("MIME %q: expected HTTP %d, got %d", mime, StatusPrefetchBadRequest, status)
			}
			if !errors.Is(err, ErrPrefetchUnsupportedMIME) {
				t.Errorf("MIME %q: expected ErrPrefetchUnsupportedMIME, got %v", mime, err)
			}

			classifiedStatus, category, _ := ClassifyPrefetchError(err)
			if classifiedStatus != StatusPrefetchBadRequest {
				t.Errorf("classified status for MIME %q: expected %d, got %d", mime, StatusPrefetchBadRequest, classifiedStatus)
			}
			if category != "unsupported_mime" {
				t.Errorf("category for MIME %q: expected unsupported_mime, got %s", mime, category)
			}
		}

		validMIMEs := []string{
			"image/jpeg",
			"image/jpeg; charset=binary",
			"IMAGE/JPEG",
			"image/png",
			"image/webp",
			"image/gif",
		}

		for _, mime := range validMIMEs {
			status, err := ValidatePrefetchMIME(mime)
			if status != http.StatusOK {
				t.Errorf("valid MIME %q: expected HTTP %d, got %d", mime, http.StatusOK, status)
			}
			if err != nil {
				t.Errorf("valid MIME %q: expected nil error, got %v", mime, err)
			}
		}
	})
}
