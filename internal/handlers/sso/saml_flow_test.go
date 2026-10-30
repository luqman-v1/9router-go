package sso

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/beevik/etree"
	dsig "github.com/russellhaering/goxmldsig"
)

// testSAMLIdP mints a certificate and signs assertions with it, so the
// verification path runs against real crypto.
type testSAMLIdP struct {
	key     *rsa.PrivateKey
	cert    *x509.Certificate
	certPEM string
}

func newTestSAMLIdP(t *testing.T) *testSAMLIdP {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "test-idp"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return &testSAMLIdP{
		key:     key,
		cert:    cert,
		certPEM: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
	}
}

// assertionXML builds an unsigned assertion whose identity the tests control.
func (i *testSAMLIdP) assertionXML(t *testing.T, email, name, audience string, notAfter time.Time) string {
	t.Helper()
	now := time.Now().UTC()
	return `<saml:Assertion xmlns:saml="urn:oasis:names:tc:SAML:2.0:assertion" ` +
		`ID="_assert1" Version="2.0" IssueInstant="` + now.Format(time.RFC3339) + `">` +
		`<saml:Issuer>https://idp.example.com</saml:Issuer>` +
		`<saml:Subject><saml:NameID>` + email + `</saml:NameID></saml:Subject>` +
		`<saml:Conditions NotBefore="` + now.Add(-time.Minute).Format(time.RFC3339) +
		`" NotOnOrAfter="` + notAfter.Format(time.RFC3339) + `">` +
		`<saml:AudienceRestriction><saml:Audience>` + audience + `</saml:Audience></saml:AudienceRestriction>` +
		`</saml:Conditions>` +
		`<saml:AttributeStatement>` +
		`<saml:Attribute Name="email"><saml:AttributeValue>` + email + `</saml:AttributeValue></saml:Attribute>` +
		`<saml:Attribute Name="name"><saml:AttributeValue>` + name + `</saml:AttributeValue></saml:Attribute>` +
		`</saml:AttributeStatement>` +
		`</saml:Assertion>`
}

// signedResponse wraps the assertion in a Response and signs it, the way an IdP
// does.
func (i *testSAMLIdP) signedResponse(t *testing.T, email, name, audience, inResponseTo string, notAfter time.Time) []byte {
	t.Helper()
	now := time.Now().UTC()
	response := `<samlp:Response xmlns:samlp="urn:oasis:names:tc:SAML:2.0:protocol" ` +
		`xmlns:saml="urn:oasis:names:tc:SAML:2.0:assertion" ` +
		`ID="_resp1" Version="2.0" IssueInstant="` + now.Format(time.RFC3339) + `"` +
		func() string {
			if inResponseTo != "" {
				return ` InResponseTo="` + inResponseTo + `"`
			}
			return ""
		}() + `>` +
		i.assertionXML(t, email, name, audience, notAfter) +
		`</samlp:Response>`

	doc := etree.NewDocument()
	if err := doc.ReadFromString(response); err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	// Sign with the test key.
	// dsig embeds the bytes verbatim in <X509Certificate>, which must be the
	// DER body — a PEM armour there produces "malformed certificate".
	keyStore := &testKeyStore{key: i.key, certDER: i.cert.Raw}
	ctx := dsig.NewDefaultSigningContext(keyStore)
	if err := ctx.SetSignatureMethod(dsig.RSASHA256SignatureMethod); err != nil {
		t.Fatal(err)
	}
	signed, err := ctx.SignEnveloped(doc.Root())
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	out := etree.NewDocument()
	out.SetRoot(signed.Copy())
	encoded, err := out.WriteToBytes()
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

// testKeyStore satisfies dsig.X509KeyStore.
type testKeyStore struct {
	key     *rsa.PrivateKey
	certDER []byte
}

func (k *testKeyStore) GetKeyPair() (*rsa.PrivateKey, []byte, error) {
	return k.key, k.certDER, nil
}

func TestVerifySAMLAssertionAcceptsAValidResponse(t *testing.T) {
	idp := newTestSAMLIdP(t)
	const acs = "https://gateway.example.com/api/auth/saml/acs"
	raw := idp.signedResponse(t, "user@example.com", "Test User", acs, "_req1", time.Now().Add(5*time.Minute))

	profile, err := verifySAMLAssertion(raw, samlSettings{
		EntryPoint: "https://idp.example.com/sso",
		Cert:       idp.certPEM,
	}, acs, "_req1")
	if err != nil {
		t.Fatalf("a valid assertion was rejected: %v", err)
	}
	if profile.Email != "user@example.com" {
		t.Errorf("email = %q", profile.Email)
	}
	if profile.Name != "Test User" {
		t.Errorf("name = %q", profile.Name)
	}
}

// A response signed by a key the configured certificate does not name must be
// refused. This is the whole point of verifying.
func TestVerifySAMLAssertionRejectsAForeignSigner(t *testing.T) {
	real := newTestSAMLIdP(t)
	attacker := newTestSAMLIdP(t)
	const acs = "https://gateway.example.com/api/auth/saml/acs"

	// Signed by the attacker's key, but the install trusts the real cert.
	raw := attacker.signedResponse(t, "attacker@evil.example", "Attacker", acs, "_req1", time.Now().Add(5*time.Minute))

	_, err := verifySAMLAssertion(raw, samlSettings{
		EntryPoint: "https://idp.example.com/sso",
		Cert:       real.certPEM,
	}, acs, "_req1")
	if err == nil {
		t.Fatal("an assertion signed by an untrusted key was accepted")
	}
}

// An expired assertion must be refused even when the signature is good.
func TestVerifySAMLAssertionRejectsAnExpiredAssertion(t *testing.T) {
	idp := newTestSAMLIdP(t)
	const acs = "https://gateway.example.com/api/auth/saml/acs"
	raw := idp.signedResponse(t, "user@example.com", "Test User", acs, "_req1", time.Now().Add(-5*time.Minute))

	_, err := verifySAMLAssertion(raw, samlSettings{
		EntryPoint: "https://idp.example.com/sso",
		Cert:       idp.certPEM,
	}, acs, "_req1")
	if err == nil {
		t.Fatal("an expired assertion was accepted")
	}
}

// An assertion minted for another service must not open a session here.
func TestVerifySAMLAssertionRejectsWrongAudience(t *testing.T) {
	idp := newTestSAMLIdP(t)
	raw := idp.signedResponse(t, "user@example.com", "Test User",
		"https://other-service.example.com/acs", "_req1", time.Now().Add(5*time.Minute))

	_, err := verifySAMLAssertion(raw, samlSettings{
		EntryPoint: "https://idp.example.com/sso",
		Cert:       idp.certPEM,
	}, "https://gateway.example.com/api/auth/saml/acs", "_req1")
	if err == nil {
		t.Fatal("an assertion for another audience was accepted")
	}
}

// An assertion answering a different login attempt is a replay.
func TestVerifySAMLAssertionRejectsWrongInResponseTo(t *testing.T) {
	idp := newTestSAMLIdP(t)
	const acs = "https://gateway.example.com/api/auth/saml/acs"
	raw := idp.signedResponse(t, "user@example.com", "Test User", acs, "_a-different-request", time.Now().Add(5*time.Minute))

	_, err := verifySAMLAssertion(raw, samlSettings{
		EntryPoint: "https://idp.example.com/sso",
		Cert:       idp.certPEM,
	}, acs, "_req1")
	if err == nil {
		t.Fatal("an assertion for a different request was accepted")
	}
}

// An unsigned response is refused before any claim is read.
func TestVerifySAMLAssertionRejectsAnUnsignedResponse(t *testing.T) {
	idp := newTestSAMLIdP(t)
	const acs = "https://gateway.example.com/api/auth/saml/acs"
	unsigned := `<samlp:Response xmlns:samlp="urn:oasis:names:tc:SAML:2.0:protocol" ` +
		`xmlns:saml="urn:oasis:names:tc:SAML:2.0:assertion" ID="_r" Version="2.0">` +
		idp.assertionXML(t, "user@example.com", "Test User", acs, time.Now().Add(5*time.Minute)) +
		`</samlp:Response>`

	_, err := verifySAMLAssertion([]byte(unsigned), samlSettings{
		EntryPoint: "https://idp.example.com/sso",
		Cert:       idp.certPEM,
	}, acs, "_req1")
	if err == nil {
		t.Fatal("an unsigned assertion was accepted")
	}
}

// IdP consoles hand out a bare base64 body as often as a PEM block.
func TestParseSAMLcertAcceptsBothShapes(t *testing.T) {
	idp := newTestSAMLIdP(t)
	if _, err := parseSAMLcert(idp.certPEM); err != nil {
		t.Errorf("PEM rejected: %v", err)
	}
	bare := strings.ReplaceAll(strings.ReplaceAll(idp.certPEM, "-----BEGIN CERTIFICATE-----", ""), "-----END CERTIFICATE-----", "")
	bare = strings.ReplaceAll(bare, "\n", "")
	if _, err := parseSAMLcert(bare); err != nil {
		t.Errorf("bare base64 rejected: %v", err)
	}
	// A settings row pasted from JSON carries literal backslash-n.
	escaped := strings.ReplaceAll(idp.certPEM, "\n", `\n`)
	if _, err := parseSAMLcert(escaped); err != nil {
		t.Errorf("escaped newlines rejected: %v", err)
	}
	if _, err := parseSAMLcert("not a certificate"); err == nil {
		t.Error("garbage was accepted as a certificate")
	}
}

// The redirect binding deflates the request; a client that cannot decode it
// cannot start a login.
func TestSAMLRedirectRequestIsDeflated(t *testing.T) {
	var b strings.Builder
	w, err := newFlateWriter(&b)
	if err != nil {
		t.Fatal(err)
	}
	w.Write([]byte("<AuthnRequest/>"))
	w.Close()
	if b.Len() == 0 {
		t.Fatal("deflate produced nothing")
	}
	encoded := base64.StdEncoding.EncodeToString([]byte(b.String()))
	if encoded == "" {
		t.Error("base64 of the deflated request is empty")
	}
}
