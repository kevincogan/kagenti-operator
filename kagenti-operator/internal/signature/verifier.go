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
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"sort"

	agentv1alpha1 "github.com/kagenti/operator/api/v1alpha1"
)

// VerifyWithPublicKey verifies a signature using a public key (PEM format)
func VerifyWithPublicKey(cardData *agentv1alpha1.AgentCardData, signature *agentv1alpha1.CardSignature, publicKeyPEM []byte) (*VerificationResult, error) {
	if signature == nil {
		return &VerificationResult{
			Verified: false,
			Error:    fmt.Errorf("no signature provided"),
			Details:  "AgentCard does not contain a signature",
		}, nil
	}

	// Parse the public key
	block, _ := pem.Decode(publicKeyPEM)
	if block == nil {
		return &VerificationResult{
			Verified: false,
			Error:    fmt.Errorf("failed to decode PEM block"),
			Details:  "Invalid PEM format",
		}, fmt.Errorf("failed to decode PEM block")
	}

	publicKey, err := parsePublicKey(block.Bytes)
	if err != nil {
		return &VerificationResult{
			Verified: false,
			Error:    err,
			Details:  fmt.Sprintf("Failed to parse public key: %v", err),
		}, err
	}

	// Create canonical representation of card data for verification
	// According to A2A spec, the signature is over the card's JSON representation
	// excluding the signature field itself
	cardJSON, err := createCanonicalCardJSON(cardData)
	if err != nil {
		return &VerificationResult{
			Verified: false,
			Error:    err,
			Details:  fmt.Sprintf("Failed to create canonical JSON: %v", err),
		}, err
	}

	// Hash the data
	hash := sha256.Sum256(cardJSON)

	// Decode the signature
	signatureBytes, err := base64.StdEncoding.DecodeString(signature.Value)
	if err != nil {
		return &VerificationResult{
			Verified: false,
			Error:    err,
			Details:  "Failed to decode signature from base64",
		}, err
	}

	// Verify based on key type
	var verified bool
	switch pub := publicKey.(type) {
	case *rsa.PublicKey:
		err = rsa.VerifyPKCS1v15(pub, crypto.SHA256, hash[:], signatureBytes)
		verified = (err == nil)
	case *ecdsa.PublicKey:
		verified = ecdsa.VerifyASN1(pub, hash[:], signatureBytes)
	default:
		return &VerificationResult{
			Verified: false,
			Error:    fmt.Errorf("unsupported key type"),
			Details:  "Public key type not supported",
		}, fmt.Errorf("unsupported key type")
	}

	result := &VerificationResult{
		Verified: verified,
		KeyID:    signature.KeyID,
	}

	if !verified {
		result.Details = "Signature verification failed"
		result.Error = fmt.Errorf("signature verification failed")
	} else {
		result.Details = "Signature verified successfully"
	}

	return result, nil
}

// parsePublicKey parses a public key from DER format
func parsePublicKey(derBytes []byte) (crypto.PublicKey, error) {
	// Try parsing as PKIX first
	if key, err := x509.ParsePKIXPublicKey(derBytes); err == nil {
		return key, nil
	}

	// Try parsing as PKCS1 RSA public key
	if key, err := x509.ParsePKCS1PublicKey(derBytes); err == nil {
		return key, nil
	}

	return nil, fmt.Errorf("failed to parse public key")
}

// createCanonicalCardJSON creates a canonical JSON representation of the card
// This is used for signature verification and MUST match the Python implementation:
// json.dumps(card_data, sort_keys=True, separators=(',', ':'), ensure_ascii=False)
func createCanonicalCardJSON(cardData *agentv1alpha1.AgentCardData) ([]byte, error) {
	// Create a map from the card data, excluding fields that shouldn't be signed
	cardMap := make(map[string]interface{})

	if cardData.Name != "" {
		cardMap["name"] = cardData.Name
	}
	if cardData.Description != "" {
		cardMap["description"] = cardData.Description
	}
	if cardData.Version != "" {
		cardMap["version"] = cardData.Version
	}
	if cardData.URL != "" {
		cardMap["url"] = cardData.URL
	}
	if cardData.Capabilities != nil {
		cardMap["capabilities"] = cardData.Capabilities
	}
	if len(cardData.DefaultInputModes) > 0 {
		cardMap["defaultInputModes"] = cardData.DefaultInputModes
	}
	if len(cardData.DefaultOutputModes) > 0 {
		cardMap["defaultOutputModes"] = cardData.DefaultOutputModes
	}
	if len(cardData.Skills) > 0 {
		cardMap["skills"] = cardData.Skills
	}
	if cardData.SupportsAuthenticatedExtendedCard != nil {
		cardMap["supportsAuthenticatedExtendedCard"] = cardData.SupportsAuthenticatedExtendedCard
	}

	// Marshal with sorted keys for canonical representation
	// Go's json.Marshal doesn't guarantee key order, so we use a custom encoder
	return marshalCanonical(cardMap)
}

// marshalCanonical marshals a map to JSON with sorted keys and no whitespace
// This ensures deterministic output matching Python's json.dumps(sort_keys=True, separators=(',', ':'))
func marshalCanonical(data map[string]interface{}) ([]byte, error) {
	var buf bytes.Buffer

	// Create encoder with no HTML escaping and no indentation
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)

	// Sort keys and build JSON manually to guarantee order
	keys := make([]string, 0, len(data))
	for k := range data {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	buf.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			buf.WriteByte(',')
		}

		// Write key
		keyJSON, err := json.Marshal(k)
		if err != nil {
			return nil, err
		}
		buf.Write(keyJSON)
		buf.WriteByte(':')

		// Write value
		valueJSON, err := marshalValue(data[k])
		if err != nil {
			return nil, err
		}
		buf.Write(valueJSON)
	}
	buf.WriteByte('}')

	return buf.Bytes(), nil
}

// marshalValue marshals a value with sorted keys if it's a map
// For structs and other complex types, we first convert to map[string]interface{}
// via JSON roundtrip to ensure consistent key ordering
func marshalValue(v interface{}) ([]byte, error) {
	switch val := v.(type) {
	case map[string]interface{}:
		return marshalCanonical(val)
	case nil:
		return []byte("null"), nil
	case bool:
		if val {
			return []byte("true"), nil
		}
		return []byte("false"), nil
	case string:
		return json.Marshal(val)
	case float64, float32, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return json.Marshal(val)
	case []interface{}:
		return marshalArray(val)
	default:
		// For structs and other complex types, convert to generic structure
		// via JSON roundtrip to ensure we get map[string]interface{} for objects
		// This is necessary because Go struct marshaling uses field declaration order,
		// not alphabetical order, which would break signature verification with Python
		genericValue, err := toGenericValue(val)
		if err != nil {
			return nil, err
		}
		return marshalValue(genericValue)
	}
}

// toGenericValue converts any value to a generic JSON-compatible type
// (map[string]interface{}, []interface{}, or primitive types)
// This ensures structs are converted to maps that can be sorted alphabetically
func toGenericValue(v interface{}) (interface{}, error) {
	// Marshal to JSON
	jsonBytes, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal value: %w", err)
	}

	// Unmarshal to generic interface{}
	var generic interface{}
	if err := json.Unmarshal(jsonBytes, &generic); err != nil {
		return nil, fmt.Errorf("failed to unmarshal to generic: %w", err)
	}

	return generic, nil
}

// marshalArray marshals an array with proper handling of nested objects
func marshalArray(arr []interface{}) ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('[')
	for i, item := range arr {
		if i > 0 {
			buf.WriteByte(',')
		}
		itemJSON, err := marshalValue(item)
		if err != nil {
			return nil, err
		}
		buf.Write(itemJSON)
	}
	buf.WriteByte(']')
	return buf.Bytes(), nil
}
