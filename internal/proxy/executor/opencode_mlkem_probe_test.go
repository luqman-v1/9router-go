package executor

import (
	"net"
	"testing"
	"time"

	tls "github.com/bogdanfinn/utls"
)

// Does an explicit KeyShareExtension carrying X25519MLKEM768 produce usable key
// material, or does its nil Data overwrite the share utls generated from
// SupportedCurves[0]?
//
// KeyShareExtension.writeToUConn assigns e.KeyShares straight onto
// Hello.KeyShares *after* makeClientHello has generated real key material from
// SupportedCurves[0], so an entry with nil Data would replace a valid share with
// an empty one — which no server accepts. This decides whether the reference
// queue (MLKEM + X25519) is expressible through this spec at all.
func TestBunSpec_MLKEMKeyShareIsMaterialized(t *testing.T) {
	spec := tls.ClientHelloSpec{
		CipherSuites: []uint16{
			tls.TLS_AES_128_GCM_SHA256,
			tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
		},
		CompressionMethods: []uint8{0},
		Extensions: []tls.TLSExtension{
			&tls.SNIExtension{},
			&tls.SupportedCurvesExtension{Curves: []tls.CurveID{
				tls.X25519MLKEM768,
				tls.X25519,
				tls.CurveP256,
				tls.CurveP384,
			}},
			&tls.KeyShareExtension{KeyShares: []tls.KeyShare{
				{Group: tls.X25519MLKEM768},
				{Group: tls.X25519},
			}},
			&tls.SupportedVersionsExtension{Versions: []uint16{tls.VersionTLS13, tls.VersionTLS12}},
		},
	}

	// BuildHandshakeState only needs a config; no socket is dialled.
	uconn := tls.UClient(&nopConn{}, &tls.Config{ServerName: "opencode.ai"}, tls.HelloCustom, false, false, false)
	if err := uconn.ApplyPreset(&spec); err != nil {
		t.Skipf("ApplyPreset rejected the spec: %v", err)
	}
	if err := uconn.BuildHandshakeState(); err != nil {
		t.Fatalf("BuildHandshakeState: %v", err)
	}

	shares := uconn.HandshakeState.Hello.KeyShares
	if len(shares) == 0 {
		t.Fatal("no key shares were produced")
	}
	for _, share := range shares {
		t.Logf("keyshare group=%d name=%s dataLen=%d", share.Group, share.Group, len(share.Data))
		if len(share.Data) == 0 {
			t.Errorf("keyshare group %d has no key material; the extension overwrote the generated share", share.Group)
		}
	}
	seen := map[tls.CurveID]bool{}
	for _, share := range shares {
		seen[share.Group] = true
	}
	if !seen[tls.X25519MLKEM768] {
		t.Errorf("MLKEM keyshare absent; groups=%v", seen)
	}
	if !seen[tls.X25519] {
		t.Errorf("X25519 fallback keyshare absent; groups=%v", seen)
	}
}

// nopConn is the minimum net.Conn BuildHandshakeState needs: it never reads or
// writes, because the ClientHello is built before any byte leaves.
type nopConn struct{}

func (nopConn) Read([]byte) (int, error)         { return 0, nil }
func (nopConn) Write(b []byte) (int, error)      { return len(b), nil }
func (nopConn) Close() error                     { return nil }
func (nopConn) LocalAddr() net.Addr              { return nopAddr{} }
func (nopConn) RemoteAddr() net.Addr             { return nopAddr{} }
func (nopConn) SetDeadline(time.Time) error      { return nil }
func (nopConn) SetReadDeadline(time.Time) error  { return nil }
func (nopConn) SetWriteDeadline(time.Time) error { return nil }

type nopAddr struct{}

func (nopAddr) Network() string { return "nop" }
func (nopAddr) String() string  { return "nop" }
