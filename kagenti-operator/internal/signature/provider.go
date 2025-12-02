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
)

// VerificationResult contains the result of signature verification
type VerificationResult struct {
	Verified bool
	KeyID    string
	Error    error
	Details  string
}

// Provider defines the interface for A2A signature verification
// Implementations can support Kubernetes Secrets, JWKS servers, or other methods
type Provider interface {
	// VerifySignature verifies an AgentCard signature
	VerifySignature(ctx context.Context, cardData *agentv1alpha1.AgentCardData, signature *agentv1alpha1.CardSignature) (*VerificationResult, error)

	// Name returns the provider name for logging and metrics
	Name() string
}

// NewProvider creates a signature verification provider based on configuration
func NewProvider(config *Config) (Provider, error) {
	if config == nil {
		return nil, fmt.Errorf("provider config cannot be nil")
	}

	switch config.Type {
	case ProviderTypeSecret:
		return NewSecretProvider(config)
	case ProviderTypeJWKS:
		return NewJWKSProvider(config)
	case ProviderTypeNone:
		return NewNoOpProvider(), nil
	default:
		return nil, fmt.Errorf("unknown provider type: %s", config.Type)
	}
}

// ProviderType defines the type of signature verification provider
type ProviderType string

const (
	ProviderTypeSecret ProviderType = "secret"
	ProviderTypeJWKS   ProviderType = "jwks"
	ProviderTypeNone   ProviderType = "none"
)

// Config holds configuration for signature verification providers
type Config struct {
	Type ProviderType

	// For secret-based provider
	SecretName      string
	SecretNamespace string
	SecretKey       string

	// For JWKS provider
	JWKSURL string

	// Common settings
	AuditMode bool // If true, log verification failures but don't block
}
