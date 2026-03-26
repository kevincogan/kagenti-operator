/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
*/

package keycloak

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ClientRegistrationParams mirrors the AuthBridge client-registration sidecar contract.
type ClientRegistrationParams struct {
	Realm               string
	ClientID            string // Keycloak OAuth clientId (SPIFFE ID or namespace/name)
	ClientName          string // Human-readable name field in Keycloak
	ClientAuthType      string // "client-secret" or "federated-jwt"
	SpiffeIDPAlias      string // Keycloak SPIFFE IdP alias when using federated-jwt
	TokenExchangeEnable bool
}

type adminTokenResponse struct {
	AccessToken string `json:"access_token"`
}

type keycloakClientRep struct {
	ID       string `json:"id,omitempty"`
	ClientID string `json:"clientId"`
	Name     string `json:"name"`

	StandardFlowEnabled       bool              `json:"standardFlowEnabled"`
	DirectAccessGrantsEnabled bool              `json:"directAccessGrantsEnabled"`
	ServiceAccountsEnabled    bool              `json:"serviceAccountsEnabled"`
	FullScopeAllowed          bool              `json:"fullScopeAllowed"`
	PublicClient              bool              `json:"publicClient"`
	ClientAuthenticatorType   string            `json:"clientAuthenticatorType"`
	Attributes                map[string]string `json:"attributes"`
}

type clientSecretRep struct {
	Value string `json:"value"`
}

// Admin is a minimal Keycloak admin REST client (password grant, client CRUD, secret read).
type Admin struct {
	BaseURL    string // e.g. https://keycloak.example.com:8080 (no trailing path)
	HTTPClient *http.Client
}

func (a *Admin) httpc() *http.Client {
	if a.HTTPClient != nil {
		return a.HTTPClient
	}
	return http.DefaultClient
}

func trimBaseURL(base string) string {
	return strings.TrimRight(strings.TrimSpace(base), "/")
}

func (a *Admin) adminToken(ctx context.Context, username, password string) (string, error) {
	base := trimBaseURL(a.BaseURL)
	form := url.Values{}
	form.Set("grant_type", "password")
	form.Set("client_id", "admin-cli")
	form.Set("username", username)
	form.Set("password", password)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		base+"/realms/master/protocol/openid-connect/token",
		strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := a.httpc().Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("keycloak token: status %d: %s", resp.StatusCode, truncate(body, 512))
	}
	var tr adminTokenResponse
	if err := json.Unmarshal(body, &tr); err != nil {
		return "", fmt.Errorf("keycloak token decode: %w", err)
	}
	if tr.AccessToken == "" {
		return "", fmt.Errorf("keycloak token: empty access_token")
	}
	return tr.AccessToken, nil
}

func truncate(b []byte, n int) string {
	s := string(b)
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// PasswordGrantToken returns an admin access token using the master realm password grant (admin-cli).
func (a *Admin) PasswordGrantToken(ctx context.Context, adminUser, adminPass string) (string, error) {
	return a.adminToken(ctx, adminUser, adminPass)
}

// RegisterOrFetchClient ensures an OAuth client exists and returns its internal UUID and client secret value.
func (a *Admin) RegisterOrFetchClient(ctx context.Context, adminUser, adminPass string, p ClientRegistrationParams) (internalID, secret string, err error) {
	token, err := a.adminToken(ctx, adminUser, adminPass)
	if err != nil {
		return "", "", err
	}
	return a.RegisterOrFetchClientWithToken(ctx, token, p)
}

// RegisterOrFetchClientWithToken is like RegisterOrFetchClient but reuses an existing admin token.
func (a *Admin) RegisterOrFetchClientWithToken(ctx context.Context, token string, p ClientRegistrationParams) (internalID, secret string, err error) {
	authType := p.ClientAuthType
	if authType == "" {
		authType = "client-secret"
	}

	attrs := map[string]string{
		"standard.token.exchange.enabled": fmt.Sprintf("%t", p.TokenExchangeEnable),
	}
	if authType == "federated-jwt" {
		alias := p.SpiffeIDPAlias
		if alias == "" {
			alias = "spire-spiffe"
		}
		attrs["jwt.credential.issuer"] = alias
		attrs["jwt.credential.sub"] = p.ClientID
	}

	rep := keycloakClientRep{
		ClientID:                  p.ClientID,
		Name:                      p.ClientName,
		StandardFlowEnabled:       true,
		DirectAccessGrantsEnabled: true,
		ServiceAccountsEnabled:    true,
		FullScopeAllowed:          false,
		PublicClient:              false,
		ClientAuthenticatorType:   authType,
		Attributes:                attrs,
	}

	internalID, err = a.findClientUUID(ctx, token, p.Realm, p.ClientID)
	if err != nil {
		return "", "", err
	}
	if internalID == "" {
		internalID, err = a.createClient(ctx, token, p.Realm, &rep)
		if err != nil {
			return "", "", err
		}
	}

	secret, err = a.readClientSecret(ctx, token, p.Realm, internalID)
	if err != nil {
		return "", "", err
	}
	return internalID, secret, nil
}

func (a *Admin) findClientUUID(ctx context.Context, token, realm, clientID string) (string, error) {
	base := trimBaseURL(a.BaseURL)
	u, err := url.Parse(base + "/admin/realms/" + url.PathEscape(realm) + "/clients")
	if err != nil {
		return "", err
	}
	q := u.Query()
	q.Set("clientId", clientID)
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := a.httpc().Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("keycloak list clients: status %d: %s", resp.StatusCode, truncate(body, 512))
	}
	var list []struct {
		ID       string `json:"id"`
		ClientID string `json:"clientId"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		return "", fmt.Errorf("keycloak list clients decode: %w", err)
	}
	for i := range list {
		if list[i].ClientID == clientID {
			return list[i].ID, nil
		}
	}
	return "", nil
}

func (a *Admin) createClient(ctx context.Context, token, realm string, rep *keycloakClientRep) (string, error) {
	base := trimBaseURL(a.BaseURL)
	payload, err := json.Marshal(rep)
	if err != nil {
		return "", err
	}
	endpoint := base + "/admin/realms/" + url.PathEscape(realm) + "/clients"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := a.httpc().Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusCreated {
		loc := resp.Header.Get("Location")
		if loc != "" {
			if id := pathLastSegment(loc); id != "" {
				return id, nil
			}
		}
		// Fall through to lookup
		return a.findClientUUID(ctx, token, realm, rep.ClientID)
	}
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusConflict {
		return a.findClientUUID(ctx, token, realm, rep.ClientID)
	}
	return "", fmt.Errorf("keycloak create client: status %d: %s", resp.StatusCode, truncate(body, 512))
}

func pathLastSegment(loc string) string {
	loc = strings.TrimRight(loc, "/")
	if idx := strings.LastIndex(loc, "/"); idx >= 0 {
		return loc[idx+1:]
	}
	return ""
}

func (a *Admin) readClientSecret(ctx context.Context, token, realm, internalUUID string) (string, error) {
	base := trimBaseURL(a.BaseURL)
	endpoint := base + "/admin/realms/" + url.PathEscape(realm) + "/clients/" + url.PathEscape(internalUUID) + "/client-secret"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := a.httpc().Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("keycloak client secret: status %d: %s", resp.StatusCode, truncate(body, 512))
	}
	var cs clientSecretRep
	if err := json.Unmarshal(body, &cs); err != nil {
		return "", fmt.Errorf("keycloak client secret decode: %w", err)
	}
	return cs.Value, nil
}

// DefaultHTTPClient returns an HTTP client suitable for Keycloak admin calls.
func DefaultHTTPClient() *http.Client {
	return &http.Client{Timeout: 60 * time.Second}
}
