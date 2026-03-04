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
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spiffe/go-spiffe/v2/svid/x509svid"
	"github.com/spiffe/go-spiffe/v2/workloadapi"

	agentv1alpha1 "github.com/kagenti/operator/api/v1alpha1"
	"github.com/kagenti/operator/internal/signature"
)

const (
	defaultSocket       = "unix:///run/spire/sockets/agent.sock"
	defaultUnsignedPath = "/etc/agentcard/agent.json"
	defaultSignedPath   = "/app/.well-known/agent-card.json"
	defaultTimeout      = "30s"
)

func main() {
	if err := run(); err != nil {
		logJSON("error", "signing failed", "error", err.Error())
		os.Exit(1)
	}
}

func run() error {
	socketPath := envOrDefault("SPIFFE_ENDPOINT_SOCKET", defaultSocket)
	unsignedPath := envOrDefault("UNSIGNED_CARD_PATH", defaultUnsignedPath)
	signedPath := envOrDefault("AGENT_CARD_PATH", defaultSignedPath)
	timeoutStr := envOrDefault("SIGN_TIMEOUT", defaultTimeout)

	timeout, err := time.ParseDuration(timeoutStr)
	if err != nil {
		return fmt.Errorf("invalid SIGN_TIMEOUT %q: %w", timeoutStr, err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	logJSON("info", "starting agentcard signer",
		"socket", socketPath,
		"unsigned_path", unsignedPath,
		"signed_path", signedPath,
		"timeout", timeoutStr,
	)

	svid, err := fetchSVID(ctx, socketPath)
	if err != nil {
		return fmt.Errorf("failed to fetch X.509-SVID: %w", err)
	}
	defer zeroPrivateKey(svid.PrivateKey)

	spiffeID := svid.ID.String()
	logJSON("info", "fetched SVID", "spiffe_id", spiffeID)

	unsignedJSON, err := os.ReadFile(unsignedPath)
	if err != nil {
		return fmt.Errorf("failed to read unsigned card from %s: %w", unsignedPath, err)
	}

	var cardData agentv1alpha1.AgentCardData
	if err := json.Unmarshal(unsignedJSON, &cardData); err != nil {
		return fmt.Errorf("failed to parse unsigned card JSON: %w", err)
	}

	signedCard, err := signCard(&cardData, svid.PrivateKey, svid.Certificates)
	if err != nil {
		return fmt.Errorf("signing failed: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(signedPath), 0o755); err != nil {
		return fmt.Errorf("failed to create output directory: %w", err)
	}

	if err := os.WriteFile(signedPath, signedCard, 0o644); err != nil {
		return fmt.Errorf("failed to write signed card to %s: %w", signedPath, err)
	}

	logJSON("info", "signed card written successfully",
		"spiffe_id", spiffeID,
		"output_path", signedPath,
	)
	return nil
}

func fetchSVID(ctx context.Context, socketPath string) (*x509svid.SVID, error) {
	client, err := workloadapi.New(ctx, workloadapi.WithAddr(socketPath))
	if err != nil {
		return nil, fmt.Errorf("failed to create workload API client: %w", err)
	}
	defer client.Close()

	svid, err := client.FetchX509SVID(ctx)
	if err != nil {
		return nil, fmt.Errorf("FetchX509SVID failed: %w", err)
	}
	return svid, nil
}

// signCard signs AgentCard data and returns the signed JSON.
func signCard(cardData *agentv1alpha1.AgentCardData, privateKey crypto.Signer, certs []*x509.Certificate) ([]byte, error) {
	if cardData == nil {
		return nil, fmt.Errorf("card data is nil")
	}
	if len(certs) == 0 {
		return nil, fmt.Errorf("no certificates in SVID chain")
	}
	leaf := certs[0]

	alg, err := algorithmForKey(privateKey.Public())
	if err != nil {
		return nil, err
	}

	kid := computeKID(leaf)

	x5c := make([]string, len(certs))
	for i, cert := range certs {
		x5c[i] = base64.StdEncoding.EncodeToString(cert.Raw)
	}

	header := &signature.ProtectedHeader{
		Algorithm: alg,
		KeyID:     kid,
		Type:      "JOSE",
		X5C:       x5c,
	}

	protectedB64, err := signature.EncodeProtectedHeader(header)
	if err != nil {
		return nil, fmt.Errorf("failed to encode protected header: %w", err)
	}

	payload, err := signature.CreateCanonicalCardJSON(cardData)
	if err != nil {
		return nil, fmt.Errorf("failed to create canonical JSON: %w", err)
	}

	payloadB64 := base64.RawURLEncoding.EncodeToString(payload)
	signingInput := []byte(protectedB64 + "." + payloadB64)

	sigBytes, err := signInput(privateKey, alg, signingInput)
	if err != nil {
		return nil, fmt.Errorf("signing failed: %w", err)
	}

	sigB64 := base64.RawURLEncoding.EncodeToString(sigBytes)

	cardData.Signatures = []agentv1alpha1.AgentCardSignature{
		{
			Protected: protectedB64,
			Signature: sigB64,
		},
	}

	output, err := json.MarshalIndent(cardData, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("failed to marshal signed card: %w", err)
	}

	return output, nil
}

// algorithmForKey maps a public key type to its JWS algorithm.
func algorithmForKey(pub crypto.PublicKey) (string, error) {
	switch k := pub.(type) {
	case *rsa.PublicKey:
		if k.N.BitLen() < 2048 {
			return "", fmt.Errorf("RSA key too small: %d bits (minimum 2048)", k.N.BitLen())
		}
		return "RS256", nil
	case *ecdsa.PublicKey:
		switch k.Curve {
		case elliptic.P256():
			return "ES256", nil
		case elliptic.P384():
			return "ES384", nil
		case elliptic.P521():
			return "ES512", nil
		default:
			return "", fmt.Errorf("unsupported ECDSA curve: %s", k.Curve.Params().Name)
		}
	default:
		return "", fmt.Errorf("unsupported key type: %T", pub)
	}
}

// computeKID derives a key ID from the leaf cert's SHA-256 fingerprint (first 8 bytes).
func computeKID(leaf *x509.Certificate) string {
	fp := sha256.Sum256(leaf.Raw)
	return fmt.Sprintf("%x", fp[:8])
}

func signInput(signer crypto.Signer, alg string, input []byte) ([]byte, error) {
	hashFunc, err := signature.HashForAlgorithm(alg)
	if err != nil {
		return nil, err
	}

	h := hashFunc.New()
	h.Write(input)
	hashed := h.Sum(nil)

	switch alg {
	case "RS256", "RS384", "RS512":
		return signer.Sign(rand.Reader, hashed, hashFunc)
	case "ES256", "ES384", "ES512":
		return signECDSARaw(signer, hashed, alg)
	default:
		return nil, fmt.Errorf("unsupported algorithm: %s", alg)
	}
}

// signECDSARaw signs with ECDSA and encodes as fixed-width R||S (RFC 7518 §3.4).
func signECDSARaw(signer crypto.Signer, hashed []byte, alg string) ([]byte, error) {
	ecKey, ok := signer.(*ecdsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("expected *ecdsa.PrivateKey, got %T", signer)
	}

	r, s, err := ecdsa.Sign(rand.Reader, ecKey, hashed)
	if err != nil {
		return nil, fmt.Errorf("ECDSA sign failed: %w", err)
	}

	keySize := signature.CurveByteSize(ecKey.Curve)
	sig := make([]byte, 2*keySize)
	rBytes := r.Bytes()
	sBytes := s.Bytes()
	copy(sig[keySize-len(rBytes):keySize], rBytes)
	copy(sig[2*keySize-len(sBytes):], sBytes)

	return sig, nil
}

// zeroPrivateKey zeroes private key material in memory (best-effort).
func zeroPrivateKey(key crypto.Signer) {
	switch k := key.(type) {
	case *ecdsa.PrivateKey:
		if k.D != nil {
			k.D.SetInt64(0)
		}
	case *rsa.PrivateKey:
		if k.D != nil {
			k.D.SetInt64(0)
		}
		for _, p := range k.Primes {
			if p != nil {
				p.SetInt64(0)
			}
		}
	}
}

func envOrDefault(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}

func logJSON(level, msg string, kvs ...string) {
	entry := map[string]string{
		"level": level,
		"msg":   msg,
		"ts":    time.Now().UTC().Format(time.RFC3339),
	}
	for i := 0; i+1 < len(kvs); i += 2 {
		entry[kvs[i]] = kvs[i+1]
	}
	data, _ := json.Marshal(entry)
	fmt.Fprintln(os.Stderr, string(data))
}
