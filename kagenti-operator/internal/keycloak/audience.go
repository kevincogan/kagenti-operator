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
)

// AudienceParams configures audience client-scope management (mirrors AuthBridge client_registration.py).
type AudienceParams struct {
	Realm                string
	ClientName           string   // e.g. namespace/workload — used to derive scope name
	AudienceClientID     string   // OAuth clientId / SPIFFE ID used as custom audience in the mapper
	PlatformClientIDs    []string // Keycloak clientId strings (e.g. UI client), not internal UUIDs
	AudienceScopeEnabled bool     // when false, EnsureAudienceScope is a no-op
}

type clientScopeListItem struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type clientScopeCreateRep struct {
	Name       string            `json:"name"`
	Protocol   string            `json:"protocol"`
	Attributes map[string]string `json:"attributes,omitempty"`
}

type protocolMapperRep struct {
	Name            string            `json:"name"`
	Protocol        string            `json:"protocol"`
	ProtocolMapper  string            `json:"protocolMapper"`
	ConsentRequired bool              `json:"consentRequired"`
	Config          map[string]string `json:"config"`
}

// AudienceScopeName derives the realm client-scope name from CLIENT_NAME (same as Python).
func AudienceScopeName(clientName string) string {
	return "agent-" + strings.ReplaceAll(clientName, "/", "-") + "-aud"
}

// EnsureAudienceScope creates or reuses an audience client scope, adds the oidc-audience mapper,
// registers it as a realm default default client scope, and attaches it to each platform client.
// Missing platform clients are skipped (like Python). Realm / per-client attachment errors are ignored
// except they are swallowed (Python prints only); attachment uses best-effort PUT with 204/409 success.
func (a *Admin) EnsureAudienceScope(ctx context.Context, token string, p AudienceParams) error {
	if !p.AudienceScopeEnabled {
		return nil
	}
	scopeName := AudienceScopeName(p.ClientName)
	scopeID, err := a.getOrCreateAudienceClientScope(ctx, token, p.Realm, scopeName, p.AudienceClientID)
	if err != nil {
		return err
	}
	_ = a.putRealmDefaultDefaultClientScope(ctx, token, p.Realm, scopeID)
	for _, plat := range p.PlatformClientIDs {
		plat = strings.TrimSpace(plat)
		if plat == "" {
			continue
		}
		internal, err := a.findClientUUID(ctx, token, p.Realm, plat)
		if err != nil || internal == "" {
			continue
		}
		_ = a.putClientDefaultClientScope(ctx, token, p.Realm, internal, scopeID)
	}
	return nil
}

func (a *Admin) getOrCreateAudienceClientScope(ctx context.Context, token, realm, scopeName, audience string) (string, error) {
	scopeID, err := a.findClientScopeIDByName(ctx, token, realm, scopeName)
	if err != nil {
		return "", err
	}
	if scopeID != "" {
		_ = a.ensureAudienceMapper(ctx, token, realm, scopeID, scopeName, audience)
		return scopeID, nil
	}

	scopeID, err = a.createClientScope(ctx, token, realm, clientScopeCreateRep{
		Name:     scopeName,
		Protocol: "openid-connect",
		Attributes: map[string]string{
			"include.in.token.scope":    "true",
			"display.on.consent.screen": "true",
		},
	})
	if err != nil {
		return "", err
	}
	if scopeID == "" {
		return "", fmt.Errorf("create client scope %q returned empty id", scopeName)
	}
	_ = a.ensureAudienceMapper(ctx, token, realm, scopeID, scopeName, audience)
	return scopeID, nil
}

func (a *Admin) findClientScopeIDByName(ctx context.Context, token, realm, name string) (string, error) {
	base := trimBaseURL(a.BaseURL)
	endpoint := base + "/admin/realms/" + url.PathEscape(realm) + "/client-scopes"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := a.httpc().Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("keycloak list client-scopes: status %d: %s", resp.StatusCode, truncate(body, 512))
	}
	var list []clientScopeListItem
	if err := json.Unmarshal(body, &list); err != nil {
		return "", fmt.Errorf("keycloak list client-scopes decode: %w", err)
	}
	for i := range list {
		if list[i].Name == name {
			return list[i].ID, nil
		}
	}
	return "", nil
}

func (a *Admin) createClientScope(ctx context.Context, token, realm string, rep clientScopeCreateRep) (string, error) {
	base := trimBaseURL(a.BaseURL)
	payload, err := json.Marshal(rep)
	if err != nil {
		return "", err
	}
	endpoint := base + "/admin/realms/" + url.PathEscape(realm) + "/client-scopes"
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
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusCreated {
		if loc := resp.Header.Get("Location"); loc != "" {
			if id := pathLastSegment(loc); id != "" {
				return id, nil
			}
		}
		return a.findClientScopeIDByName(ctx, token, realm, rep.Name)
	}
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusConflict {
		return a.findClientScopeIDByName(ctx, token, realm, rep.Name)
	}
	return "", fmt.Errorf("keycloak create client-scope: status %d: %s", resp.StatusCode, truncate(body, 512))
}

func (a *Admin) ensureAudienceMapper(ctx context.Context, token, realm, scopeID, scopeName, audience string) error {
	mapper := protocolMapperRep{
		Name:            scopeName,
		Protocol:        "openid-connect",
		ProtocolMapper:  "oidc-audience-mapper",
		ConsentRequired: false,
		Config: map[string]string{
			"included.custom.audience": audience,
			"id.token.claim":           "false",
			"access.token.claim":       "true",
			"userinfo.token.claim":     "false",
		},
	}
	payload, err := json.Marshal(mapper)
	if err != nil {
		return err
	}
	base := trimBaseURL(a.BaseURL)
	endpoint := base + "/admin/realms/" + url.PathEscape(realm) + "/client-scopes/" + url.PathEscape(scopeID) + "/protocol-mappers/models"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := a.httpc().Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusCreated || resp.StatusCode == http.StatusConflict {
		return nil
	}
	body, _ := io.ReadAll(resp.Body)
	// Mapper may already exist — treat other errors as non-fatal (Python logs and continues).
	if resp.StatusCode >= 400 {
		return fmt.Errorf("keycloak add audience mapper: status %d: %s", resp.StatusCode, truncate(body, 256))
	}
	return nil
}

func (a *Admin) putRealmDefaultDefaultClientScope(ctx context.Context, token, realm, scopeID string) error {
	base := trimBaseURL(a.BaseURL)
	endpoint := base + "/admin/realms/" + url.PathEscape(realm) + "/default-default-client-scopes/" + url.PathEscape(scopeID)
	return a.putNoBodyExpectSuccess(ctx, token, endpoint)
}

func (a *Admin) putClientDefaultClientScope(ctx context.Context, token, realm, clientInternalUUID, scopeID string) error {
	base := trimBaseURL(a.BaseURL)
	endpoint := base + "/admin/realms/" + url.PathEscape(realm) + "/clients/" + url.PathEscape(clientInternalUUID) + "/default-client-scopes/" + url.PathEscape(scopeID)
	return a.putNoBodyExpectSuccess(ctx, token, endpoint)
}

func (a *Admin) putNoBodyExpectSuccess(ctx context.Context, token, endpoint string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := a.httpc().Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	// 204 success; 409 often means already linked; 404 can mean already removed / wrong id — ignore like Python prints.
	if resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusConflict {
		return nil
	}
	if resp.StatusCode == http.StatusNotFound {
		return nil
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	body, _ := io.ReadAll(resp.Body)
	return fmt.Errorf("keycloak PUT %s: status %d: %s", endpoint, resp.StatusCode, truncate(body, 256))
}
