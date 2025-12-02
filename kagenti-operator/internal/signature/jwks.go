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

package signature

import (
	"context"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"sync"
	"time"

	agentv1alpha1 "github.com/kagenti/operator/api/v1alpha1"
	ctrl "sigs.k8s.io/controller-runtime"
)

var (
	jwksLogger = ctrl.Log.WithName("signature").WithName("jwks")
)

// JWKSProvider verifies signatures using a JWKS (JSON Web Key Set) endpoint
type JWKSProvider struct {
	jwksURL    string
	httpClient *http.Client
	auditMode  bool

	// Cache for JWKS keys
	keysMutex sync.RWMutex
	keysCache map[string]*JWK
	lastFetch time.Time
	cacheTTL  time.Duration
}

// JWK represents a JSON Web Key
type JWK struct {
	Kid string `json:"kid"` // Key ID
	Kty string `json:"kty"` // Key Type (RSA, EC, etc.)
	Use string `json:"use"` // Use (sig, enc)
	Alg string `json:"alg"` // Algorithm
	N   string `json:"n"`   // RSA modulus
	E   string `json:"e"`   // RSA exponent
	X   string `json:"x"`   // EC x coordinate
	Y   string `json:"y"`   // EC y coordinate
	Crv string `json:"crv"` // EC curve
}

// JWKS represents a JSON Web Key Set
type JWKS struct {
	Keys []JWK `json:"keys"`
}

// NewJWKSProvider creates a new JWKS-based signature verification provider
func NewJWKSProvider(config *Config) (Provider, error) {
	if config.JWKSURL == "" {
		return nil, fmt.Errorf("JWKS URL is required")
	}

	return &JWKSProvider{
		jwksURL: config.JWKSURL,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
		auditMode: config.AuditMode,
		keysCache: make(map[string]*JWK),
		cacheTTL:  5 * time.Minute, // Cache keys for 5 minutes
	}, nil
}

// VerifySignature verifies a signature using JWKS
func (p *JWKSProvider) VerifySignature(ctx context.Context, cardData *agentv1alpha1.AgentCardData, signature *agentv1alpha1.CardSignature) (*VerificationResult, error) {
	jwksLogger.Info("Verifying signature using JWKS", "url", p.jwksURL)

	if signature == nil {
		result := &VerificationResult{
			Verified: false,
			Error:    fmt.Errorf("no signature provided"),
			Details:  "AgentCard does not contain a signature",
		}
		if p.auditMode {
			jwksLogger.Info("Audit mode: AgentCard has no signature", "card", cardData.Name)
			result.Verified = true
		}
		return result, nil
	}

	// Fetch JWKS keys
	if err := p.refreshKeysIfNeeded(ctx); err != nil {
		result := &VerificationResult{
			Verified: false,
			Error:    err,
			Details:  fmt.Sprintf("Failed to fetch JWKS: %v", err),
		}
		if p.auditMode {
			jwksLogger.Error(err, "Audit mode: Failed to fetch JWKS, allowing anyway")
			result.Verified = true
		}
		return result, err
	}

	// Find the key with matching kid
	jwk := p.findKey(signature.KeyID)
	if jwk == nil {
		err := fmt.Errorf("key with ID %s not found in JWKS", signature.KeyID)
		result := &VerificationResult{
			Verified: false,
			Error:    err,
			Details:  err.Error(),
			KeyID:    signature.KeyID,
		}
		if p.auditMode {
			jwksLogger.Error(err, "Audit mode: Key not found, allowing anyway")
			result.Verified = true
		}
		return result, err
	}

	// Convert JWK to public key
	publicKeyPEM, err := p.jwkToPublicKeyPEM(jwk)
	if err != nil {
		result := &VerificationResult{
			Verified: false,
			Error:    err,
			Details:  fmt.Sprintf("Failed to convert JWK to public key: %v", err),
			KeyID:    signature.KeyID,
		}
		if p.auditMode {
			jwksLogger.Error(err, "Audit mode: Failed to convert key, allowing anyway")
			result.Verified = true
		}
		return result, err
	}

	// Verify the signature
	result, err := VerifyWithPublicKey(cardData, signature, publicKeyPEM)
	if err != nil && p.auditMode {
		jwksLogger.Error(err, "Audit mode: Signature verification failed, allowing anyway",
			"keyID", signature.KeyID)
		result.Verified = true
	}

	return result, err
}

// refreshKeysIfNeeded fetches JWKS keys if cache is stale
func (p *JWKSProvider) refreshKeysIfNeeded(ctx context.Context) error {
	p.keysMutex.RLock()
	needsRefresh := time.Since(p.lastFetch) > p.cacheTTL
	p.keysMutex.RUnlock()

	if !needsRefresh {
		return nil
	}

	return p.fetchKeys(ctx)
}

// fetchKeys fetches keys from JWKS endpoint
func (p *JWKSProvider) fetchKeys(ctx context.Context) error {
	jwksLogger.Info("Fetching JWKS keys", "url", p.jwksURL)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.jwksURL, nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to fetch JWKS: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("unexpected status code %d: %s", resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read response: %w", err)
	}

	var jwks JWKS
	if err := json.Unmarshal(body, &jwks); err != nil {
		return fmt.Errorf("failed to parse JWKS: %w", err)
	}

	// Update cache
	p.keysMutex.Lock()
	defer p.keysMutex.Unlock()

	p.keysCache = make(map[string]*JWK)
	for i := range jwks.Keys {
		jwk := &jwks.Keys[i]
		if jwk.Kid != "" {
			p.keysCache[jwk.Kid] = jwk
		}
	}
	p.lastFetch = time.Now()

	jwksLogger.Info("Successfully fetched JWKS keys", "count", len(p.keysCache))
	return nil
}

// findKey finds a key by kid
func (p *JWKSProvider) findKey(kid string) *JWK {
	p.keysMutex.RLock()
	defer p.keysMutex.RUnlock()
	return p.keysCache[kid]
}

// jwkToPublicKeyPEM converts a JWK to PEM format
func (p *JWKSProvider) jwkToPublicKeyPEM(jwk *JWK) ([]byte, error) {
	switch jwk.Kty {
	case "RSA":
		return p.rsaJWKToPEM(jwk)
	default:
		return nil, fmt.Errorf("unsupported key type: %s", jwk.Kty)
	}
}

// rsaJWKToPEM converts an RSA JWK to PEM format
func (p *JWKSProvider) rsaJWKToPEM(jwk *JWK) ([]byte, error) {
	// Decode modulus
	nBytes, err := base64.RawURLEncoding.DecodeString(jwk.N)
	if err != nil {
		return nil, fmt.Errorf("failed to decode modulus: %w", err)
	}

	// Decode exponent
	eBytes, err := base64.RawURLEncoding.DecodeString(jwk.E)
	if err != nil {
		return nil, fmt.Errorf("failed to decode exponent: %w", err)
	}

	// Convert exponent bytes to int
	var eInt int
	for _, b := range eBytes {
		eInt = eInt<<8 + int(b)
	}

	// Create RSA public key
	publicKey := &rsa.PublicKey{
		N: new(big.Int).SetBytes(nBytes),
		E: eInt,
	}

	// Marshal to PKIX format
	pkixBytes, err := x509.MarshalPKIXPublicKey(publicKey)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal public key: %w", err)
	}

	// Encode to PEM
	pemBlock := &pem.Block{
		Type:  "PUBLIC KEY",
		Bytes: pkixBytes,
	}

	return pem.EncodeToMemory(pemBlock), nil
}

// Name returns the provider name
func (p *JWKSProvider) Name() string {
	return "jwks"
}
