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
	"sync"

	"github.com/spiffe/go-spiffe/v2/svid/x509svid"
	"github.com/spiffe/go-spiffe/v2/workloadapi"

	agentv1alpha1 "github.com/kagenti/operator/api/v1alpha1"
)

// X509SVIDSource abstracts the SPIFFE X509Source for testability.
type X509SVIDSource interface {
	GetX509SVID() (*x509svid.SVID, error)
	Close() error
}

// SpiffeSigner implements the Signer interface using a SPIFFE X509Source
// that auto-rotates SVIDs as they approach expiry.
type SpiffeSigner struct {
	source X509SVIDSource
	mu     sync.RWMutex
	closed bool
}

// NewSpiffeSigner creates a SpiffeSigner backed by the SPIFFE Workload API.
// The X509Source automatically rotates SVIDs before expiry.
func NewSpiffeSigner(ctx context.Context, socketPath string) (*SpiffeSigner, error) {
	source, err := workloadapi.NewX509Source(ctx, workloadapi.WithClientOptions(workloadapi.WithAddr(socketPath)))
	if err != nil {
		return nil, fmt.Errorf("failed to create X509Source: %w", err)
	}

	return &SpiffeSigner{
		source: source,
	}, nil
}

// NewSpiffeSignerWithSource creates a SpiffeSigner with a custom X509SVIDSource (for testing).
func NewSpiffeSignerWithSource(source X509SVIDSource) *SpiffeSigner {
	return &SpiffeSigner{
		source: source,
	}
}

// SignCard signs the AgentCard data using the current SVID from the X509Source.
func (s *SpiffeSigner) SignCard(ctx context.Context, cardData *agentv1alpha1.AgentCardData) ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.closed {
		return nil, fmt.Errorf("signer is closed")
	}

	svid, err := s.source.GetX509SVID()
	if err != nil {
		return nil, fmt.Errorf("failed to get X509-SVID: %w", err)
	}

	signed, err := SignCard(cardData, svid.PrivateKey, svid.Certificates)
	if err != nil {
		return nil, fmt.Errorf("signing failed: %w", err)
	}

	// Do NOT zero svid.PrivateKey here: the X509Source owns the SVID and
	// returns a shared reference. Zeroing it would corrupt future calls.
	return signed, nil
}

// SpiffeID returns the SPIFFE ID from the current SVID, or empty string if unavailable.
func (s *SpiffeSigner) SpiffeID() string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.closed {
		return ""
	}

	svid, err := s.source.GetX509SVID()
	if err != nil {
		return ""
	}

	return svid.ID.String()
}

// Ready returns true when the signer has obtained at least one SVID.
func (s *SpiffeSigner) Ready() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.closed {
		return false
	}

	_, err := s.source.GetX509SVID()
	return err == nil
}

// Close shuts down the X509Source and releases resources.
func (s *SpiffeSigner) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return nil
	}

	s.closed = true
	return s.source.Close()
}
