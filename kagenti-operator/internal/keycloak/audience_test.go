package keycloak

import (
	"context"
	"encoding/json"
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

func TestEnsureAudienceScope_Disabled(t *testing.T) {
	a := Admin{}
	err := a.EnsureAudienceScope(context.Background(), "t", AudienceParams{AudienceScopeEnabled: false})
	if err != nil {
		t.Fatal(err)
	}
}
