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

package v1alpha1

import (
	"context"
	"strings"
	"testing"

	agentv1alpha1 "github.com/kagenti/operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
)

func validAgentCard() *agentv1alpha1.AgentCard {
	return &agentv1alpha1.AgentCard{
		Spec: agentv1alpha1.AgentCardSpec{
			TargetRef: &agentv1alpha1.TargetRef{
				APIVersion: "apps/v1",
				Kind:       "Deployment",
				Name:       "test",
			},
		},
	}
}

func TestAgentCardValidator_ValidateCreate(t *testing.T) {
	v := &AgentCardValidator{}
	ctx := context.Background()

	t.Run("with targetRef succeeds", func(t *testing.T) {
		_, err := v.ValidateCreate(ctx, validAgentCard())
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("without targetRef returns error", func(t *testing.T) {
		_, err := v.ValidateCreate(ctx, &agentv1alpha1.AgentCard{})
		if err == nil {
			t.Fatal("expected error for missing targetRef")
		}
		if !strings.Contains(err.Error(), "targetRef is required") {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("wrong object type returns error", func(t *testing.T) {
		_, err := v.ValidateCreate(ctx, &corev1.Pod{})
		if err == nil {
			t.Fatal("expected error for wrong object type")
		}
		if !strings.Contains(err.Error(), "expected an AgentCard") {
			t.Errorf("unexpected error: %v", err)
		}
	})
}

func TestAgentCardValidator_ValidateUpdate(t *testing.T) {
	v := &AgentCardValidator{}
	ctx := context.Background()
	old := validAgentCard()

	t.Run("with targetRef succeeds", func(t *testing.T) {
		_, err := v.ValidateUpdate(ctx, old, validAgentCard())
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("without targetRef returns error", func(t *testing.T) {
		_, err := v.ValidateUpdate(ctx, old, &agentv1alpha1.AgentCard{})
		if err == nil {
			t.Fatal("expected error for missing targetRef")
		}
		if !strings.Contains(err.Error(), "targetRef is required") {
			t.Errorf("unexpected error: %v", err)
		}
	})
}

func TestAgentCardValidator_ValidateDelete(t *testing.T) {
	v := &AgentCardValidator{}
	ctx := context.Background()

	t.Run("with valid AgentCard succeeds", func(t *testing.T) {
		_, err := v.ValidateDelete(ctx, validAgentCard())
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("wrong object type returns error", func(t *testing.T) {
		_, err := v.ValidateDelete(ctx, &corev1.Pod{})
		if err == nil {
			t.Fatal("expected error for wrong object type")
		}
	})
}
