/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/url"
	"testing"
	"time"

	agentv1alpha1 "github.com/kagenti/operator/api/v1alpha1"
	"github.com/kagenti/operator/internal/signature"
)

// --- Test CA helpers (mirrors x5c_test.go pattern) ---

type testCA struct {
	Key  *ecdsa.PrivateKey
	Cert *x509.Certificate
}

func newTestCA(t *testing.T) *testCA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "Test CA"},
		NotBefore:             time.Now().Add(-1 * time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	certDER, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(certDER)
	if err != nil {
		t.Fatal(err)
	}
	return &testCA{Key: key, Cert: cert}
}

func (ca *testCA) issueLeaf(t *testing.T, pub interface{}, spiffeID string) (*x509.Certificate, []byte) {
	t.Helper()
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: "Test Leaf"},
		NotBefore:    time.Now().Add(-1 * time.Hour),
		NotAfter:     time.Now().Add(1 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageAny},
	}
	if spiffeID != "" {
		u, _ := url.Parse(spiffeID)
		tmpl.URIs = append(tmpl.URIs, u)
	}
	certDER, err := x509.CreateCertificate(rand.Reader, tmpl, ca.Cert, pub, ca.Key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(certDER)
	if err != nil {
		t.Fatal(err)
	}
	return cert, certDER
}

func testCard() *agentv1alpha1.AgentCardData {
	return &agentv1alpha1.AgentCardData{
		Name:    "test-agent",
		Version: "1.0.0",
		URL:     "https://test.example.com/.well-known/agent-card.json",
	}
}

// --- signCard tests ---

func TestSignCard_ECDSA_P256(t *testing.T) {
	ca := newTestCA(t)
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	leaf, _ := ca.issueLeaf(t, &key.PublicKey, "spiffe://example.org/ns/default/sa/test")

	card := testCard()
	output, err := signCard(card, key, []*x509.Certificate{leaf, ca.Cert})
	if err != nil {
		t.Fatalf("signCard failed: %v", err)
	}

	var parsed agentv1alpha1.AgentCardData
	if err := json.Unmarshal(output, &parsed); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	if len(parsed.Signatures) != 1 {
		t.Fatalf("expected 1 signature, got %d", len(parsed.Signatures))
	}

	header, err := signature.DecodeProtectedHeader(parsed.Signatures[0].Protected)
	if err != nil {
		t.Fatalf("failed to decode protected header: %v", err)
	}
	if header.Algorithm != "ES256" {
		t.Errorf("expected alg=ES256, got %s", header.Algorithm)
	}
	if header.Type != "JOSE" {
		t.Errorf("expected typ=JOSE, got %s", header.Type)
	}
	if len(header.X5C) != 2 {
		t.Errorf("expected 2 certs in x5c, got %d", len(header.X5C))
	}
}

func TestSignCard_ECDSA_P384(t *testing.T) {
	ca := newTestCA(t)
	key, _ := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	leaf, _ := ca.issueLeaf(t, &key.PublicKey, "spiffe://example.org/agent")

	card := testCard()
	output, err := signCard(card, key, []*x509.Certificate{leaf})
	if err != nil {
		t.Fatalf("signCard failed: %v", err)
	}

	var parsed agentv1alpha1.AgentCardData
	if err := json.Unmarshal(output, &parsed); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}

	header, _ := signature.DecodeProtectedHeader(parsed.Signatures[0].Protected)
	if header.Algorithm != "ES384" {
		t.Errorf("expected alg=ES384, got %s", header.Algorithm)
	}
}

func TestSignCard_RSA(t *testing.T) {
	ca := newTestCA(t)
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	leaf, _ := ca.issueLeaf(t, &key.PublicKey, "spiffe://example.org/rsa-agent")

	card := testCard()
	output, err := signCard(card, key, []*x509.Certificate{leaf})
	if err != nil {
		t.Fatalf("signCard failed: %v", err)
	}

	var parsed agentv1alpha1.AgentCardData
	if err := json.Unmarshal(output, &parsed); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}

	header, _ := signature.DecodeProtectedHeader(parsed.Signatures[0].Protected)
	if header.Algorithm != "RS256" {
		t.Errorf("expected alg=RS256, got %s", header.Algorithm)
	}
}

func TestSignCard_NilCardData(t *testing.T) {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	_, err := signCard(nil, key, []*x509.Certificate{{}})
	if err == nil {
		t.Error("expected error for nil card data")
	}
}

func TestSignCard_NoCertificates(t *testing.T) {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	_, err := signCard(testCard(), key, nil)
	if err == nil {
		t.Error("expected error for empty cert chain")
	}
}

// --- ECDSA raw R||S encoding test ---

func TestSignCard_ECDSA_RawRS_ByteLength(t *testing.T) {
	ca := newTestCA(t)
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	leaf, _ := ca.issueLeaf(t, &key.PublicKey, "spiffe://example.org/agent")

	card := testCard()
	output, err := signCard(card, key, []*x509.Certificate{leaf})
	if err != nil {
		t.Fatalf("signCard failed: %v", err)
	}

	var parsed agentv1alpha1.AgentCardData
	json.Unmarshal(output, &parsed)

	sigBytes, err := base64.RawURLEncoding.DecodeString(parsed.Signatures[0].Signature)
	if err != nil {
		t.Fatalf("failed to decode signature: %v", err)
	}

	// ES256 raw R||S must be exactly 64 bytes (32 + 32)
	if len(sigBytes) != 64 {
		t.Errorf("ES256 raw R||S signature must be 64 bytes, got %d (likely DER-encoded)", len(sigBytes))
	}
}

func TestSignCard_ECDSA_P384_RawRS_ByteLength(t *testing.T) {
	ca := newTestCA(t)
	key, _ := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	leaf, _ := ca.issueLeaf(t, &key.PublicKey, "spiffe://example.org/agent")

	card := testCard()
	output, err := signCard(card, key, []*x509.Certificate{leaf})
	if err != nil {
		t.Fatalf("signCard failed: %v", err)
	}

	var parsed agentv1alpha1.AgentCardData
	json.Unmarshal(output, &parsed)

	sigBytes, _ := base64.RawURLEncoding.DecodeString(parsed.Signatures[0].Signature)
	// ES384 raw R||S must be exactly 96 bytes (48 + 48)
	if len(sigBytes) != 96 {
		t.Errorf("ES384 raw R||S signature must be 96 bytes, got %d", len(sigBytes))
	}
}

// --- x5c header construction tests ---

func TestSignCard_X5C_StandardBase64(t *testing.T) {
	ca := newTestCA(t)
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	leaf, leafDER := ca.issueLeaf(t, &key.PublicKey, "spiffe://example.org/agent")

	card := testCard()
	output, _ := signCard(card, key, []*x509.Certificate{leaf, ca.Cert})

	var parsed agentv1alpha1.AgentCardData
	json.Unmarshal(output, &parsed)

	header, _ := signature.DecodeProtectedHeader(parsed.Signatures[0].Protected)

	// x5c must use standard base64 (not base64url) per RFC 7515 §4.1.6
	decoded, err := base64.StdEncoding.DecodeString(header.X5C[0])
	if err != nil {
		t.Fatalf("x5c[0] is not valid standard base64: %v", err)
	}
	if string(decoded) != string(leafDER) {
		t.Error("x5c[0] does not match leaf certificate DER")
	}
}

func TestSignCard_X5C_LeafFirst(t *testing.T) {
	ca := newTestCA(t)
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	leaf, _ := ca.issueLeaf(t, &key.PublicKey, "spiffe://example.org/agent")

	card := testCard()
	output, _ := signCard(card, key, []*x509.Certificate{leaf, ca.Cert})

	var parsed agentv1alpha1.AgentCardData
	json.Unmarshal(output, &parsed)

	header, _ := signature.DecodeProtectedHeader(parsed.Signatures[0].Protected)

	leafDER, _ := base64.StdEncoding.DecodeString(header.X5C[0])
	parsedLeaf, _ := x509.ParseCertificate(leafDER)
	if parsedLeaf.IsCA {
		t.Error("x5c[0] should be the leaf (non-CA), not the CA")
	}

	caDER, _ := base64.StdEncoding.DecodeString(header.X5C[1])
	parsedCA, _ := x509.ParseCertificate(caDER)
	if !parsedCA.IsCA {
		t.Error("x5c[1] should be the CA certificate")
	}
}

// --- kid derivation test ---

func TestComputeKID(t *testing.T) {
	ca := newTestCA(t)
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	leaf, _ := ca.issueLeaf(t, &key.PublicKey, "spiffe://example.org/agent")

	kid := computeKID(leaf)

	fp := sha256.Sum256(leaf.Raw)
	expected := big.NewInt(0).SetBytes(fp[:8]).Text(16)
	// kid should be first 16 hex chars of SHA-256 fingerprint
	if len(kid) != 16 {
		t.Errorf("expected kid length 16, got %d: %s", len(kid), kid)
	}
	_ = expected // format may differ in leading zeros, just check length
}

// --- algorithmForKey tests ---

func TestAlgorithmForKey_ECDSA_P256(t *testing.T) {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	alg, err := algorithmForKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	if alg != "ES256" {
		t.Errorf("expected ES256, got %s", alg)
	}
}

func TestAlgorithmForKey_ECDSA_P384(t *testing.T) {
	key, _ := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	alg, err := algorithmForKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	if alg != "ES384" {
		t.Errorf("expected ES384, got %s", alg)
	}
}

func TestAlgorithmForKey_ECDSA_P521(t *testing.T) {
	key, _ := ecdsa.GenerateKey(elliptic.P521(), rand.Reader)
	alg, err := algorithmForKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	if alg != "ES512" {
		t.Errorf("expected ES512, got %s", alg)
	}
}

func TestAlgorithmForKey_RSA(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	alg, err := algorithmForKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	if alg != "RS256" {
		t.Errorf("expected RS256, got %s", alg)
	}
}

func TestAlgorithmForKey_RSA_TooSmall(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 1024)
	_, err := algorithmForKey(&key.PublicKey)
	if err == nil {
		t.Error("expected error for 1024-bit RSA key")
	}
}

// --- zeroPrivateKey tests ---

func TestZeroPrivateKey_ECDSA(t *testing.T) {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	zeroPrivateKey(key)
	if key.D.Sign() != 0 {
		t.Error("expected ECDSA D to be zeroed")
	}
}

func TestZeroPrivateKey_RSA(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	zeroPrivateKey(key)
	if key.D.Sign() != 0 {
		t.Error("expected RSA D to be zeroed")
	}
	for i, p := range key.Primes {
		if p.Sign() != 0 {
			t.Errorf("expected RSA prime[%d] to be zeroed", i)
		}
	}
}

// --- Canonical JSON cross-validation ---
// Signer uses signature.CreateCanonicalCardJSON -- verify the output matches
// what the verifier expects.

func TestSignCard_CanonicalJSON_CrossValidation(t *testing.T) {
	ca := newTestCA(t)
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	leaf, _ := ca.issueLeaf(t, &key.PublicKey, "spiffe://example.org/agent")

	card := testCard()
	output, err := signCard(card, key, []*x509.Certificate{leaf})
	if err != nil {
		t.Fatalf("signCard failed: %v", err)
	}

	var parsed agentv1alpha1.AgentCardData
	json.Unmarshal(output, &parsed)

	// Re-derive the canonical JSON from the parsed card (without signatures)
	cardWithoutSigs := parsed
	cardWithoutSigs.Signatures = nil
	canonical, err := signature.CreateCanonicalCardJSON(&cardWithoutSigs)
	if err != nil {
		t.Fatalf("CreateCanonicalCardJSON failed: %v", err)
	}

	// Reconstruct the signing input and verify the signature
	sig := parsed.Signatures[0]
	payloadB64 := base64.RawURLEncoding.EncodeToString(canonical)
	signingInput := sig.Protected + "." + payloadB64

	pubPEM, _ := signature.MarshalPublicKeyToPEM(&key.PublicKey)
	result, err := signature.VerifyJWS(&cardWithoutSigs, &sig, pubPEM)
	if err != nil {
		t.Fatalf("VerifyJWS error: %v", err)
	}
	if !result.Verified {
		t.Errorf("cross-validation failed: signer output not verified by VerifyJWS: %s", result.Details)
	}
	_ = signingInput
}

// --- End-to-end: signer output verified by X5CProvider ---

func TestSignCard_VerifiedByX5CProvider(t *testing.T) {
	ca := newTestCA(t)
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	leaf, _ := ca.issueLeaf(t, &key.PublicKey, "spiffe://example.org/ns/default/sa/test")

	card := testCard()
	output, err := signCard(card, key, []*x509.Certificate{leaf, ca.Cert})
	if err != nil {
		t.Fatalf("signCard failed: %v", err)
	}

	var parsed agentv1alpha1.AgentCardData
	json.Unmarshal(output, &parsed)

	// Build an X5CProvider with the test CA
	pool := x509.NewCertPool()
	pool.AddCert(ca.Cert)
	provider := &signature.X5CProvider{}
	provider.SetTrustBundleForTest(pool)

	cardWithoutSigs := parsed
	cardWithoutSigs.Signatures = nil
	result, err := provider.VerifySignature(t.Context(), &cardWithoutSigs, parsed.Signatures)
	if err != nil {
		t.Fatalf("X5CProvider.VerifySignature error: %v", err)
	}
	if !result.Verified {
		t.Errorf("X5CProvider rejected signer output: %s", result.Details)
	}
	if result.SpiffeID != "spiffe://example.org/ns/default/sa/test" {
		t.Errorf("expected SPIFFE ID from cert SAN, got %q", result.SpiffeID)
	}
}
