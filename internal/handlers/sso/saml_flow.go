package sso

import (
	"compress/flate"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"encoding/xml"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/beevik/etree"
	dsig "github.com/russellhaering/goxmldsig"

	"9router/proxy/internal/auth"
	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/log"
)

// SAML 2.0 SP-initiated login.
//
// The settings panel could test a SAML configuration and serve SP metadata, but
// the browser flows the login page links to — `/api/auth/saml/start` and
// `/api/auth/saml/acs` — were never registered, so pressing "Sign in with SAML"
// answered 404.
//
// The signature is verified with goxmldsig rather than by hand. XML Signature
// Wrapping is the attack this flow exists to survive: an attacker who can move
// the signed element around inside the document makes a validator read a
// different assertion from the one that was signed. A hand-rolled verifier is
// how that bug ships.

const (
	samlStateCookie = "saml_state"
	samlFlowMaxAge  = 10 * 60
)

// samlSettings is the configuration the flow needs.
type samlSettings struct {
	EntryPoint string
	Issuer     string
	Cert       string
	// EmailAttribute and NameAttribute name the assertion attributes that
	// carry the identity; upstream defaults to the common OIDC-style names.
	EmailAttribute string
	NameAttribute  string
}

// readSAMLSettings returns the configured SAML client, or false when the
// install has not configured one.
func (h *Handler) readSAMLSettings() (samlSettings, bool) {
	raw, err := h.Repo.GetSettingsRaw()
	if err != nil || raw == nil {
		return samlSettings{}, false
	}
	cfg := samlSettings{
		EntryPoint:     strings.TrimSpace(handlerutil.GetString(raw, "samlEntryPoint")),
		Issuer:         strings.TrimSpace(handlerutil.GetString(raw, "samlIssuer")),
		Cert:           strings.TrimSpace(handlerutil.GetString(raw, "samlCert")),
		EmailAttribute: strings.TrimSpace(handlerutil.GetString(raw, "samlEmailAttribute")),
		NameAttribute:  strings.TrimSpace(handlerutil.GetString(raw, "samlNameAttribute")),
	}
	if cfg.EntryPoint == "" || cfg.Cert == "" {
		return samlSettings{}, false
	}
	if cfg.EmailAttribute == "" {
		cfg.EmailAttribute = "email"
	}
	if cfg.NameAttribute == "" {
		cfg.NameAttribute = "name"
	}
	return cfg, true
}

// HandleSAMLStart serves GET /api/auth/saml/start: build a signed AuthnRequest
// and redirect to the IdP.
func (h *Handler) HandleSAMLStart(w http.ResponseWriter, r *http.Request) {
	cfg, ok := h.readSAMLSettings()
	if !ok {
		redirectToLoginError(w, r, "saml_not_configured")
		return
	}
	acsURL := publicOrigin(r) + samlACSPath
	requestID := "_" + randomURLToken(24)
	issueInstant := time.Now().UTC().Format("2006-01-02T15:04:05Z")

	issuer := cfg.Issuer
	if issuer == "" {
		issuer = publicOrigin(r)
	}

	authnRequest := fmt.Sprintf(
		`<samlp:AuthnRequest xmlns:samlp="urn:oasis:names:tc:SAML:2.0:protocol" `+
			`xmlns:saml="urn:oasis:names:tc:SAML:2.0:assertion" `+
			`ID="%s" Version="2.0" IssueInstant="%s" `+
			`Destination="%s" ProtocolBinding="urn:oasis:names:tc:SAML:2.0:bindings:HTTP-POST" `+
			`AssertionConsumerServiceURL="%s">`+
			`<saml:Issuer>%s</saml:Issuer>`+
			`<samlp:NameIDPolicy Format="urn:oasis:names:tc:SAML:1.1:nameid-format:emailAddress" AllowCreate="true"/>`+
			`</samlp:AuthnRequest>`,
		xmlEscape(requestID), issueInstant, xmlEscape(cfg.EntryPoint),
		xmlEscape(acsURL), xmlEscape(issuer))

	// The redirect binding deflates, then base64-encodes.
	var deflated strings.Builder
	zw, err := newFlateWriter(&deflated)
	if err == nil {
		_, _ = zw.Write([]byte(authnRequest))
		_ = zw.Close()
	}
	if deflated.Len() == 0 {
		redirectToLoginError(w, r, "saml_request_build_failed")
		return
	}
	encoded := base64.StdEncoding.EncodeToString([]byte(deflated.String()))

	secure := isSecureRequest(r)
	http.SetCookie(w, &http.Cookie{
		Name: samlStateCookie, Value: requestID, Path: "/", HttpOnly: true,
		Secure: secure, SameSite: http.SameSiteLaxMode, MaxAge: samlFlowMaxAge,
	})

	target := cfg.EntryPoint
	sep := "?"
	if strings.Contains(target, "?") {
		sep = "&"
	}
	http.Redirect(w, r, target+sep+"SAMLRequest="+url.QueryEscape(encoded), http.StatusFound)
}

// HandleSAMLACS serves POST /api/auth/saml/acs: verify the assertion and open a
// dashboard session.
func (h *Handler) HandleSAMLACS(w http.ResponseWriter, r *http.Request) {
	storedRequestID, _ := readCookie(r, samlStateCookie)
	http.SetCookie(w, &http.Cookie{
		Name: samlStateCookie, Value: "", Path: "/", HttpOnly: true,
		Secure: isSecureRequest(r), SameSite: http.SameSiteLaxMode, MaxAge: -1,
	})

	if err := r.ParseForm(); err != nil {
		redirectToLoginError(w, r, "saml_bad_form")
		return
	}
	encoded := r.PostFormValue("SAMLResponse")
	if encoded == "" {
		redirectToLoginError(w, r, "saml_missing_response")
		return
	}
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		redirectToLoginError(w, r, "saml_bad_response")
		return
	}

	cfg, ok := h.readSAMLSettings()
	if !ok {
		redirectToLoginError(w, r, "saml_not_configured")
		return
	}

	profile, err := verifySAMLAssertion(raw, cfg, publicOrigin(r)+samlACSPath, storedRequestID)
	if err != nil {
		log.Warn("sso", "saml assertion rejected", "error", err)
		redirectToLoginError(w, r, "saml_invalid_response")
		return
	}

	session, err := auth.SignWith(auth.Secret(), time.Now(), auth.SessionClaims{
		Authenticated: true,
		Saml:          true,
		SamlEmail:     profile.Email,
		SamlName:      profile.Name,
	})
	if err != nil {
		redirectToLoginError(w, r, "saml_session_failed")
		return
	}
	auth.SetCookie(w, r, session)
	http.Redirect(w, r, "/dashboard", http.StatusFound)
}

// samlProfile is what the assertion says about the user.
type samlProfile struct {
	NameID string
	Email  string
	Name   string
}

// samlAssertionDoc is the subset of the assertion the flow reads after the
// signature has already been verified against the document.
type samlAssertionDoc struct {
	XMLName xml.Name `xml:"Assertion"`
	ID      string   `xml:"ID,attr"`
	Subject struct {
		NameID string `xml:"NameID"`
	} `xml:"Subject"`
	Conditions struct {
		NotBefore    string `xml:"NotBefore,attr"`
		NotOnOrAfter string `xml:"NotOnOrAfter,attr"`
	} `xml:"Conditions"`
	AttributeStatements []struct {
		Attributes []struct {
			Name   string   `xml:"Name,attr"`
			Values []string `xml:"AttributeValue"`
		} `xml:"Attribute"`
	} `xml:"AttributeStatement"`
}

// verifySAMLAssertion validates the signature and reads the identity out of the
// assertion.
//
// The order matters: the document is parsed and its signature verified before
// any claim is read, and the verified element is the one the claims come from.
// Reading a claim and then checking a signature would be the XML Signature
// Wrapping bug in miniature.
func verifySAMLAssertion(raw []byte, cfg samlSettings, acsURL, expectedRequestID string) (*samlProfile, error) {
	cert, err := parseSAMLcert(cfg.Cert)
	if err != nil {
		return nil, err
	}

	doc := etree.NewDocument()
	if err := doc.ReadFromBytes(raw); err != nil {
		return nil, fmt.Errorf("saml: parse response: %w", err)
	}

	root := doc.Root()
	if root == nil {
		return nil, fmt.Errorf("saml: empty document")
	}

	certStore := &dsig.MemoryX509CertificateStore{Roots: []*x509.Certificate{cert}}
	ctx := dsig.NewDefaultValidationContext(certStore)

	// goxmldsig validates every signed element it finds and refuses a document
	// whose signature does not cover the element it claims to. A response can
	// carry a signed Response wrapping an unsigned Assertion, or the reverse;
	// both are handled by validating what is actually signed and then reading
	// from the validated tree.
	validated, err := ctx.Validate(root)
	if err != nil {
		return nil, fmt.Errorf("saml: signature does not verify: %w", err)
	}

	assertionEl := findAssertion(validated)
	if assertionEl == nil {
		return nil, fmt.Errorf("saml: response carries no assertion")
	}

	var assertion samlAssertionDoc
	// etree's Element.WriteTo dereferences the settings pointer, so the element
	// is serialized through a Document instead.
	tmp := etree.NewDocument()
	tmp.SetRoot(assertionEl.Copy())
	assertionXML, err := tmp.WriteToString()
	if err != nil {
		return nil, fmt.Errorf("saml: re-encode assertion: %w", err)
	}
	if err := xml.Unmarshal([]byte(assertionXML), &assertion); err != nil {
		return nil, fmt.Errorf("saml: parse assertion: %w", err)
	}

	// Replay: an assertion from a different login attempt is not this login.
	if expectedRequestID != "" {
		if inResponseTo := findInResponseTo(root); inResponseTo != "" && inResponseTo != expectedRequestID {
			return nil, fmt.Errorf("saml: InResponseTo does not match the request that started this login")
		}
	}
	if err := checkConditions(assertion); err != nil {
		return nil, err
	}
	if err := checkAudience(assertionEl, acsURL); err != nil {
		return nil, err
	}

	profile := &samlProfile{NameID: strings.TrimSpace(assertion.Subject.NameID)}
	for _, stmt := range assertion.AttributeStatements {
		for _, attr := range stmt.Attributes {
			name := strings.ToLower(strings.TrimSpace(attr.Name))
			// IdPs publish the same fact under several spellings; the configured
			// name wins, then the usual aliases.
			switch {
			case name == strings.ToLower(cfg.EmailAttribute),
				profile.Email == "" && (name == "email" || name == "mail" ||
					name == "emailaddress" || name == "urn:oid:0.9.2342.19200300.100.1.3"):
				if profile.Email == "" && len(attr.Values) > 0 {
					profile.Email = strings.TrimSpace(attr.Values[0])
				}
			case name == strings.ToLower(cfg.NameAttribute),
				profile.Name == "" && (name == "name" || name == "displayname" ||
					name == "cn" || name == "urn:oid:2.16.840.1.113730.3.1.241"):
				if profile.Name == "" && len(attr.Values) > 0 {
					profile.Name = strings.TrimSpace(attr.Values[0])
				}
			}
		}
	}
	// A NameID in email format is the fallback identity: many IdPs send no
	// email attribute at all and put the address in the subject.
	if profile.Email == "" && strings.Contains(profile.NameID, "@") {
		profile.Email = profile.NameID
	}
	if profile.Name == "" {
		profile.Name = profile.Email
	}
	if profile.Email == "" && profile.NameID == "" {
		return nil, fmt.Errorf("saml: assertion carries no identity")
	}
	return profile, nil
}

// findAssertion returns the assertion element from the validated tree.
func findAssertion(el *etree.Element) *etree.Element {
	if el == nil {
		return nil
	}
	if el.Tag == "Assertion" {
		return el
	}
	for _, child := range el.ChildElements() {
		if found := findAssertion(child); found != nil {
			return found
		}
	}
	return nil
}

// findInResponseTo reads the response's InResponseTo attribute.
func findInResponseTo(el *etree.Element) string {
	if el == nil {
		return ""
	}
	if v := el.SelectAttrValue("InResponseTo", ""); v != "" {
		return v
	}
	for _, child := range el.ChildElements() {
		if v := findInResponseTo(child); v != "" {
			return v
		}
	}
	return ""
}

// checkConditions enforces the validity window.
func checkConditions(assertion samlAssertionDoc) error {
	now := time.Now().UTC()
	parse := func(s string) (time.Time, bool) {
		if s == "" {
			return time.Time{}, false
		}
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			return time.Time{}, false
		}
		return t, true
	}
	if notBefore, ok := parse(assertion.Conditions.NotBefore); ok && now.Before(notBefore.Add(-2*time.Minute)) {
		return fmt.Errorf("saml: assertion is not valid yet")
	}
	if notAfter, ok := parse(assertion.Conditions.NotOnOrAfter); ok && now.After(notAfter) {
		return fmt.Errorf("saml: assertion has expired")
	}
	return nil
}

// checkAudience enforces the AudienceRestriction: an assertion minted for
// another service must not open a session here.
func checkAudience(assertionEl *etree.Element, acsURL string) error {
	audiences := assertionEl.FindElements("//Audience")
	if len(audiences) == 0 {
		return nil
	}
	for _, a := range audiences {
		if strings.TrimSpace(a.Text()) == acsURL {
			return nil
		}
	}
	return fmt.Errorf("saml: audience does not include this service")
}

// parseSAMLcert accepts a PEM certificate, with or without the armour, and with
// literal \n from a settings row pasted from JSON.
func parseSAMLcert(raw string) (*x509.Certificate, error) {
	text := strings.ReplaceAll(raw, `\n`, "\n")
	if !strings.Contains(text, "BEGIN CERTIFICATE") {
		// A bare base64 body is common in IdP consoles.
		text = "-----BEGIN CERTIFICATE-----\n" + strings.Join(splitN(text, 64), "\n") + "\n-----END CERTIFICATE-----\n"
	}
	block, _ := pem.Decode([]byte(text))
	if block == nil {
		return nil, fmt.Errorf("saml: certificate is not PEM")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("saml: parse certificate: %w", err)
	}
	return cert, nil
}

// splitN breaks a base64 body into 64-character lines.
func splitN(s string, n int) []string {
	var out []string
	for len(s) > n {
		out = append(out, s[:n])
		s = s[n:]
	}
	if s != "" {
		out = append(out, s)
	}
	return out
}

// newFlateWriter wraps a string builder in a raw-deflate writer, which is what
// the SAML redirect binding specifies.
func newFlateWriter(w *strings.Builder) (*flate.Writer, error) {
	return flate.NewWriter(w, flate.DefaultCompression)
}
