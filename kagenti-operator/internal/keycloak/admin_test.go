package keycloak

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAdmin_RegisterOrFetchClient(t *testing.T) {
	var tokenCalls, listCalls, createCalls, secretCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/realms/master/protocol/openid-connect/token":
			tokenCalls++
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "t"})
		case strings.HasPrefix(r.URL.Path, "/admin/realms/kagenti/clients") && r.Method == http.MethodGet && r.URL.Query().Get("clientId") != "":
			listCalls++
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode([]map[string]string{{"id": "uuid-1", "clientId": "ns/workload"}})
		case strings.HasPrefix(r.URL.Path, "/admin/realms/kagenti/clients/") && strings.HasSuffix(r.URL.Path, "/client-secret"):
			secretCalls++
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{"value": "topsecret"})
		case r.URL.Path == "/admin/realms/kagenti/clients" && r.Method == http.MethodPost:
			createCalls++
			t.Fatal("unexpected create when client exists")
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	a := Admin{BaseURL: srv.URL, HTTPClient: srv.Client()}
	id, sec, err := a.RegisterOrFetchClient(context.Background(), "admin", "pw", ClientRegistrationParams{
		Realm:      "kagenti",
		ClientID:   "ns/workload",
		ClientName: "ns/workload",
	})
	if err != nil {
		t.Fatal(err)
	}
	if id != "uuid-1" || sec != "topsecret" {
		t.Fatalf("got id=%q sec=%q", id, sec)
	}
	if tokenCalls != 1 || listCalls != 1 || secretCalls != 1 {
		t.Fatalf("calls token=%d list=%d create=%d secret=%d", tokenCalls, listCalls, createCalls, secretCalls)
	}
}
