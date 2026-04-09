/*
Copyright 2026.

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

package main

import "testing"

func TestAuthBridgeWebhooksEnabled(t *testing.T) {
	t.Run("enables when unset", func(t *testing.T) {
		t.Setenv("ENABLE_WEBHOOKS", "")
		if !authBridgeWebhooksEnabled() {
			t.Fatal("expected webhooks enabled when ENABLE_WEBHOOKS is unset")
		}
	})
	t.Run("enables when not false", func(t *testing.T) {
		t.Setenv("ENABLE_WEBHOOKS", "true")
		if !authBridgeWebhooksEnabled() {
			t.Fatal("expected webhooks enabled for non-false value")
		}
	})
	t.Run("disables when false", func(t *testing.T) {
		t.Setenv("ENABLE_WEBHOOKS", "false")
		if authBridgeWebhooksEnabled() {
			t.Fatal("expected webhooks disabled when ENABLE_WEBHOOKS=false")
		}
	})
}
