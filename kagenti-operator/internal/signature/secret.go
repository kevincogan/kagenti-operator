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
	"fmt"

	agentv1alpha1 "github.com/kagenti/operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var (
	secretLogger = ctrl.Log.WithName("signature").WithName("secret")
)

// SecretProvider verifies signatures using public keys stored in Kubernetes Secrets
type SecretProvider struct {
	client          client.Client
	secretName      string
	secretNamespace string
	secretKey       string
	auditMode       bool
}

// NewSecretProvider creates a new Secret-based signature verification provider
func NewSecretProvider(config *Config) (Provider, error) {
	if config.SecretName == "" {
		return nil, fmt.Errorf("secret name is required")
	}
	if config.SecretNamespace == "" {
		return nil, fmt.Errorf("secret namespace is required")
	}

	// Note: client will be injected via SetClient method
	return &SecretProvider{
		secretName:      config.SecretName,
		secretNamespace: config.SecretNamespace,
		secretKey:       config.SecretKey,
		auditMode:       config.AuditMode,
	}, nil
}

// SetClient sets the Kubernetes client (called after provider creation)
func (p *SecretProvider) SetClient(c client.Client) {
	p.client = c
}

// VerifySignature verifies a signature using public keys from a Kubernetes Secret
func (p *SecretProvider) VerifySignature(ctx context.Context, cardData *agentv1alpha1.AgentCardData, signature *agentv1alpha1.CardSignature) (*VerificationResult, error) {
	secretLogger.Info("Verifying signature using Kubernetes Secret",
		"secret", p.secretName,
		"namespace", p.secretNamespace)

	if p.client == nil {
		return &VerificationResult{
			Verified: false,
			Error:    fmt.Errorf("kubernetes client not initialized"),
			Details:  "Internal error: client not set",
		}, fmt.Errorf("kubernetes client not initialized")
	}

	if signature == nil {
		result := &VerificationResult{
			Verified: false,
			Error:    fmt.Errorf("no signature provided"),
			Details:  "AgentCard does not contain a signature",
		}
		if p.auditMode {
			secretLogger.Info("Audit mode: AgentCard has no signature", "card", cardData.Name)
			result.Verified = true // In audit mode, allow unsigned cards
		}
		return result, nil
	}

	// Fetch the secret containing public keys
	secret := &corev1.Secret{}
	err := p.client.Get(ctx, types.NamespacedName{
		Name:      p.secretName,
		Namespace: p.secretNamespace,
	}, secret)
	if err != nil {
		result := &VerificationResult{
			Verified: false,
			Error:    err,
			Details:  fmt.Sprintf("Failed to fetch secret: %v", err),
		}
		if p.auditMode {
			secretLogger.Error(err, "Audit mode: Failed to fetch secret, allowing anyway")
			result.Verified = true
		}
		return result, err
	}

	// Determine which key to use
	keyData, err := p.getKeyFromSecret(secret, signature.KeyID)
	if err != nil {
		result := &VerificationResult{
			Verified: false,
			Error:    err,
			Details:  fmt.Sprintf("Failed to get key from secret: %v", err),
			KeyID:    signature.KeyID,
		}
		if p.auditMode {
			secretLogger.Error(err, "Audit mode: Key not found in secret, allowing anyway",
				"keyID", signature.KeyID)
			result.Verified = true
		}
		return result, err
	}

	// Verify the signature
	result, err := VerifyWithPublicKey(cardData, signature, keyData)
	if err != nil && p.auditMode {
		secretLogger.Error(err, "Audit mode: Signature verification failed, allowing anyway",
			"keyID", signature.KeyID)
		result.Verified = true
	}

	return result, err
}

// getKeyFromSecret retrieves the appropriate public key from the secret
func (p *SecretProvider) getKeyFromSecret(secret *corev1.Secret, keyID string) ([]byte, error) {
	// If a specific key is configured, use that
	if p.secretKey != "" {
		if data, ok := secret.Data[p.secretKey]; ok {
			return data, nil
		}
		return nil, fmt.Errorf("key %s not found in secret", p.secretKey)
	}

	// If keyID is specified in the signature, try to find it
	if keyID != "" {
		if data, ok := secret.Data[keyID]; ok {
			return data, nil
		}
		// Also try with common extensions
		for _, ext := range []string{".pem", ".pub", ".key"} {
			if data, ok := secret.Data[keyID+ext]; ok {
				return data, nil
			}
		}
		return nil, fmt.Errorf("key with ID %s not found in secret", keyID)
	}

	// Try common key names
	for _, keyName := range []string{"public.pem", "publickey.pem", "key.pem", "public-key"} {
		if data, ok := secret.Data[keyName]; ok {
			return data, nil
		}
	}

	// If only one key in secret, use that
	if len(secret.Data) == 1 {
		for _, data := range secret.Data {
			return data, nil
		}
	}

	return nil, fmt.Errorf("no suitable public key found in secret")
}

// Name returns the provider name
func (p *SecretProvider) Name() string {
	return "secret"
}
