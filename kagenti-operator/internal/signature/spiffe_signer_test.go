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
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"fmt"
	"testing"

	"github.com/spiffe/go-spiffe/v2/spiffeid"
	"github.com/spiffe/go-spiffe/v2/svid/x509svid"
)

type mockX509Source struct {
	svid    *x509svid.SVID
	err     error
	closed  bool
	callCnt int
}

func (m *mockX509Source) GetX509SVID() (*x509svid.SVID, error) {
	m.callCnt++
	if m.err != nil {
		return nil, m.err
	}
	return m.svid, nil
}

func (m *mockX509Source) Close() error {
	m.closed = true
	return nil
}

func newMockSVID(t *testing.T, ca *testCA, spiffeIDStr string) *x509svid.SVID {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	leaf := ca.issueLeaf(t, key, leafOpts{
		spiffeIDs: []string{spiffeIDStr},
	})
	td, _ := spiffeid.TrustDomainFromString("example.org")
	id, _ := spiffeid.FromPath(td, "/ns/default/sa/test")
	return &x509svid.SVID{
		ID:           id,
		Certificates: []*x509.Certificate{leaf, ca.Cert},
		PrivateKey:   key,
	}
}

func TestSpiffeSigner_SignCard_Success(t *testing.T) {
	ca := newTestCA(t)
	svid := newMockSVID(t, ca, "spiffe://example.org/ns/default/sa/test")
	source := &mockX509Source{svid: svid}

	signer := NewSpiffeSignerWithSource(source)
	defer signer.Close() //nolint:errcheck // test cleanup

	card := testCard()
	output, err := signer.SignCard(context.Background(), card)
	if err != nil {
		t.Fatalf("SignCard failed: %v", err)
	}
	if len(output) == 0 {
		t.Error("expected non-empty signed output")
	}
}

func TestSpiffeSigner_NotReady(t *testing.T) {
	source := &mockX509Source{err: fmt.Errorf("no SVID available")}
	signer := NewSpiffeSignerWithSource(source)
	defer signer.Close() //nolint:errcheck // test cleanup

	if signer.Ready() {
		t.Error("expected Ready()=false when source returns error")
	}

	_, err := signer.SignCard(context.Background(), testCard())
	if err == nil {
		t.Error("expected error when signer not ready")
	}
}

func TestSpiffeSigner_SpiffeID(t *testing.T) {
	ca := newTestCA(t)
	svid := newMockSVID(t, ca, "spiffe://example.org/ns/default/sa/test")
	source := &mockX509Source{svid: svid}

	signer := NewSpiffeSignerWithSource(source)
	defer signer.Close() //nolint:errcheck // test cleanup

	id := signer.SpiffeID()
	if id != "spiffe://example.org/ns/default/sa/test" {
		t.Errorf("expected spiffe://example.org/ns/default/sa/test, got %q", id)
	}
}

func TestSpiffeSigner_SVIDRotation(t *testing.T) {
	ca := newTestCA(t)
	svid1 := newMockSVID(t, ca, "spiffe://example.org/ns/default/sa/test")
	source := &mockX509Source{svid: svid1}

	signer := NewSpiffeSignerWithSource(source)
	defer signer.Close() //nolint:errcheck // test cleanup

	card := testCard()
	output1, err := signer.SignCard(context.Background(), card)
	if err != nil {
		t.Fatalf("first SignCard failed: %v", err)
	}

	svid2 := newMockSVID(t, ca, "spiffe://example.org/ns/default/sa/test")
	source.svid = svid2

	card2 := testCard()
	output2, err := signer.SignCard(context.Background(), card2)
	if err != nil {
		t.Fatalf("second SignCard failed: %v", err)
	}

	if string(output1) == string(output2) {
		t.Error("expected different signatures after SVID rotation (different key)")
	}
}

func TestSpiffeSigner_Close(t *testing.T) {
	source := &mockX509Source{err: fmt.Errorf("not ready")}
	signer := NewSpiffeSignerWithSource(source)

	if err := signer.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}
	if !source.closed {
		t.Error("expected source to be closed")
	}

	if signer.Ready() {
		t.Error("expected Ready()=false after close")
	}

	_, err := signer.SignCard(context.Background(), testCard())
	if err == nil {
		t.Error("expected error after close")
	}
}
