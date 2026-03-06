package agentcard

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDefaultFetcher_SuccessfulA2ACardFetch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != A2AAgentCardPath {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"name":"test-agent","version":"1.0","url":"http://example.com"}`))
	}))
	defer server.Close()

	result, err := NewFetcher().Fetch(context.Background(), A2AProtocol, server.URL)
	if err != nil {
		t.Fatalf("Fetch failed: %v", err)
	}
	if result.Name != "test-agent" {
		t.Errorf("Name: got %q, want %q", result.Name, "test-agent")
	}
	if result.Version != "1.0" {
		t.Errorf("Version: got %q, want %q", result.Version, "1.0")
	}
}

func TestDefaultFetcher_UnsupportedProtocol(t *testing.T) {
	_, err := NewFetcher().Fetch(context.Background(), "unsupported", "http://example.com")
	if err == nil {
		t.Fatal("expected error for unsupported protocol")
	}
	if !strings.Contains(err.Error(), "unsupported protocol") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestDefaultFetcher_HTTPErrors(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
	}{
		{"404", http.StatusNotFound},
		{"500", http.StatusInternalServerError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				w.Write([]byte("error body"))
			}))
			defer server.Close()

			_, err := NewFetcher().Fetch(context.Background(), A2AProtocol, server.URL)
			if err == nil {
				t.Fatalf("expected error for HTTP %d", tc.status)
			}
			if !strings.Contains(err.Error(), "unexpected status code") {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestDefaultFetcher_InvalidJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("not valid json"))
	}))
	defer server.Close()

	_, err := NewFetcher().Fetch(context.Background(), A2AProtocol, server.URL)
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
	if !strings.Contains(err.Error(), "failed to parse agent card JSON") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestGetServiceURL(t *testing.T) {
	got := GetServiceURL("my-agent", "default", 8080)
	want := "http://my-agent.default.svc.cluster.local:8080"
	if got != want {
		t.Errorf("GetServiceURL: got %q, want %q", got, want)
	}
}
