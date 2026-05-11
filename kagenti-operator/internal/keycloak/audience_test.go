package keycloak

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAudienceScopeName(t *testing.T) {
	if got := AudienceScopeName("team1/my-agent"); got != "agent-team1-my-agent-aud" {
		t.Fatalf("got %q", got)
	}
}

func TestEnsureAudienceScope(t *testing.T) {
	var listScopesCalls, postScopeCalls, postMapperCalls, putRealmCalls, listKagentiCalls, putClientCalls int
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case path == testMasterRealmTokenPath:
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "tok"})
		case path == "/admin/realms/kagenti/client-scopes" && r.Method == http.MethodGet:
			listScopesCalls++
			_ = json.NewEncoder(w).Encode([]clientScopeListItem{})
		case path == "/admin/realms/kagenti/client-scopes" && r.Method == http.MethodPost:
			postScopeCalls++
			w.Header().Set("Location", srv.URL+"/admin/realms/kagenti/client-scopes/new-scope-id")
			w.WriteHeader(http.StatusCreated)
		case strings.Contains(path, "/client-scopes/new-scope-id/protocol-mappers/models") && r.Method == http.MethodPost:
			postMapperCalls++
			w.WriteHeader(http.StatusCreated)
		case path == "/admin/realms/kagenti/default-default-client-scopes/new-scope-id" && r.Method == http.MethodPut:
			putRealmCalls++
			w.WriteHeader(http.StatusNoContent)
		case strings.HasPrefix(path, "/admin/realms/kagenti/clients") && r.Method == http.MethodGet && r.URL.Query().Get("clientId") == "kagenti":
			listKagentiCalls++
			_ = json.NewEncoder(w).Encode([]map[string]string{{"id": "plat-int", "clientId": "kagenti"}})
		case path == "/admin/realms/kagenti/clients/plat-int/default-client-scopes/new-scope-id" && r.Method == http.MethodPut:
			putClientCalls++
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected %s %s", r.Method, path)
		}
	}))
	defer srv.Close()

	a := Admin{BaseURL: srv.URL, HTTPClient: srv.Client()}
	token, err := a.PasswordGrantToken(context.Background(), "u", "p")
	if err != nil {
		t.Fatal(err)
	}
	err = a.EnsureAudienceScope(context.Background(), token, AudienceParams{
		Realm:                "kagenti",
		ClientName:           "ns/wl",
		AudienceClientID:     "ns/wl",
		PlatformClientIDs:    []string{"kagenti"},
		AudienceScopeEnabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if listScopesCalls != 1 {
		t.Fatalf("listScopesCalls=%d", listScopesCalls)
	}
	if postScopeCalls != 1 || postMapperCalls != 1 || putRealmCalls != 1 || listKagentiCalls != 1 || putClientCalls != 1 {
		t.Fatalf("calls scope=%d mapper=%d realm=%d listK=%d putC=%d", postScopeCalls, postMapperCalls, putRealmCalls, listKagentiCalls, putClientCalls)
	}
}

// TestEnsureAudienceScope_UpdatesStaleMapper verifies that when an audience scope mapper
// already exists with a different audience (e.g. short-form "ns/wl" instead of SPIFFE URI),
// ensureAudienceMapper detects the mismatch and updates it via PUT.
func TestEnsureAudienceScope_UpdatesStaleMapper(t *testing.T) {
	var getMapperCalls, putMapperCalls int
	var putMapperBody protocolMapperRep

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case path == testMasterRealmTokenPath:
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "tok"})

		// Scope already exists
		case path == "/admin/realms/kagenti/client-scopes" && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode([]clientScopeListItem{{ID: "scope-123", Name: "agent-ns-wl-aud"}})

		// POST mapper returns 409 (already exists)
		case strings.Contains(path, "/client-scopes/scope-123/protocol-mappers/models") && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusConflict)

		// GET mappers — returns mapper with stale audience
		case strings.Contains(path, "/client-scopes/scope-123/protocol-mappers/models") && r.Method == http.MethodGet:
			getMapperCalls++
			_ = json.NewEncoder(w).Encode([]protocolMapperRep{{
				ID:             "mapper-456",
				Name:           "agent-ns-wl-aud",
				Protocol:       "openid-connect",
				ProtocolMapper: "oidc-audience-mapper",
				Config: map[string]string{
					"included.custom.audience": "ns/wl", // stale short-form
					"id.token.claim":           "false",
					"access.token.claim":       "true",
					"userinfo.token.claim":     "false",
				},
			}})

		// PUT mapper — update with correct audience
		case strings.Contains(path, "/client-scopes/scope-123/protocol-mappers/models/mapper-456") && r.Method == http.MethodPut:
			putMapperCalls++
			body, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(body, &putMapperBody)
			w.WriteHeader(http.StatusNoContent)

		// Realm default scope
		case path == "/admin/realms/kagenti/default-default-client-scopes/scope-123" && r.Method == http.MethodPut:
			w.WriteHeader(http.StatusNoContent)

		default:
			t.Fatalf("unexpected %s %s", r.Method, path)
		}
	}))
	defer srv.Close()

	a := Admin{BaseURL: srv.URL, HTTPClient: srv.Client()}
	token, err := a.PasswordGrantToken(context.Background(), "u", "p")
	if err != nil {
		t.Fatal(err)
	}

	spiffeURI := "spiffe://example.org/ns/ns/sa/wl"
	err = a.EnsureAudienceScope(context.Background(), token, AudienceParams{
		Realm:                "kagenti",
		ClientName:           "ns/wl",
		AudienceClientID:     spiffeURI,
		AudienceScopeEnabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if getMapperCalls != 1 {
		t.Fatalf("expected 1 GET mapper call, got %d", getMapperCalls)
	}
	if putMapperCalls != 1 {
		t.Fatalf("expected 1 PUT mapper call, got %d", putMapperCalls)
	}
	if putMapperBody.Config["included.custom.audience"] != spiffeURI {
		t.Fatalf("expected audience %q, got %q", spiffeURI, putMapperBody.Config["included.custom.audience"])
	}
}

// TestEnsureAudienceScope_SkipsUpdateWhenCorrect verifies that when the existing mapper
// already has the correct audience, no PUT is issued.
func TestEnsureAudienceScope_SkipsUpdateWhenCorrect(t *testing.T) {
	var putMapperCalls int

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case path == testMasterRealmTokenPath:
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "tok"})

		case path == "/admin/realms/kagenti/client-scopes" && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode([]clientScopeListItem{{ID: "scope-123", Name: "agent-ns-wl-aud"}})

		case strings.Contains(path, "/client-scopes/scope-123/protocol-mappers/models") && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusConflict)

		case strings.Contains(path, "/client-scopes/scope-123/protocol-mappers/models") && r.Method == http.MethodGet:
			spiffeURI := "spiffe://example.org/ns/ns/sa/wl"
			_ = json.NewEncoder(w).Encode([]protocolMapperRep{{
				ID:             "mapper-456",
				Name:           "agent-ns-wl-aud",
				Protocol:       "openid-connect",
				ProtocolMapper: "oidc-audience-mapper",
				Config: map[string]string{
					"included.custom.audience": spiffeURI, // already correct
					"id.token.claim":           "false",
					"access.token.claim":       "true",
					"userinfo.token.claim":     "false",
				},
			}})

		case strings.Contains(path, "/protocol-mappers/models/mapper-456") && r.Method == http.MethodPut:
			putMapperCalls++
			w.WriteHeader(http.StatusNoContent)

		case path == "/admin/realms/kagenti/default-default-client-scopes/scope-123" && r.Method == http.MethodPut:
			w.WriteHeader(http.StatusNoContent)

		default:
			t.Fatalf("unexpected %s %s", r.Method, path)
		}
	}))
	defer srv.Close()

	a := Admin{BaseURL: srv.URL, HTTPClient: srv.Client()}
	token, err := a.PasswordGrantToken(context.Background(), "u", "p")
	if err != nil {
		t.Fatal(err)
	}

	err = a.EnsureAudienceScope(context.Background(), token, AudienceParams{
		Realm:                "kagenti",
		ClientName:           "ns/wl",
		AudienceClientID:     "spiffe://example.org/ns/ns/sa/wl",
		AudienceScopeEnabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if putMapperCalls != 0 {
		t.Fatalf("expected 0 PUT mapper calls (audience already correct), got %d", putMapperCalls)
	}
}

// TestEnsureAudienceScope_MapperFailurePropagated verifies that when the mapper POST
// returns a server error (e.g. 500), the error propagates to EnsureAudienceScope
// instead of being silently swallowed (regression test for #348).
func TestEnsureAudienceScope_MapperFailurePropagated(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case path == testMasterRealmTokenPath:
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "tok"})

		// Scope does not exist yet
		case path == "/admin/realms/kagenti/client-scopes" && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode([]clientScopeListItem{})

		// Scope creation succeeds
		case path == "/admin/realms/kagenti/client-scopes" && r.Method == http.MethodPost:
			w.Header().Set("Location", srv.URL+"/admin/realms/kagenti/client-scopes/new-scope-id")
			w.WriteHeader(http.StatusCreated)

		// Mapper POST returns 500 (server error)
		case strings.Contains(path, "/client-scopes/new-scope-id/protocol-mappers/models") && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":"internal"}`))

		default:
			t.Fatalf("unexpected %s %s", r.Method, path)
		}
	}))
	defer srv.Close()

	a := Admin{BaseURL: srv.URL, HTTPClient: srv.Client()}
	token, err := a.PasswordGrantToken(context.Background(), "u", "p")
	if err != nil {
		t.Fatal(err)
	}

	err = a.EnsureAudienceScope(context.Background(), token, AudienceParams{
		Realm:                "kagenti",
		ClientName:           "ns/wl",
		AudienceClientID:     "spiffe://example.org/ns/ns/sa/wl",
		AudienceScopeEnabled: true,
	})
	if err == nil {
		t.Fatal("expected error when mapper POST fails, got nil")
	}
	if !strings.Contains(err.Error(), "ensure audience mapper") {
		t.Fatalf("expected error to contain 'ensure audience mapper', got: %s", err.Error())
	}
}

func TestEnsureAudienceScope_Disabled(t *testing.T) {
	a := Admin{}
	err := a.EnsureAudienceScope(context.Background(), "t", AudienceParams{AudienceScopeEnabled: false})
	if err != nil {
		t.Fatal(err)
	}
}
