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

package v1alpha1

import (
	"context"
	"strings"
	"testing"

	agentv1alpha1 "github.com/kagenti/operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func validAgentRuntime() *agentv1alpha1.AgentRuntime {
	return &agentv1alpha1.AgentRuntime{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-runtime",
			Namespace: "default",
		},
		Spec: agentv1alpha1.AgentRuntimeSpec{
			Type: agentv1alpha1.RuntimeTypeAgent,
			TargetRef: agentv1alpha1.TargetRef{
				APIVersion: "apps/v1",
				Kind:       "Deployment",
				Name:       "test",
			},
		},
	}
}

func fakeRuntimeReader(objs ...client.Object) client.Reader {
	return fake.NewClientBuilder().
		WithScheme(newScheme()).
		WithObjects(objs...).
		Build()
}

func TestAgentRuntimeValidator_ValidateCreate(t *testing.T) {
	ctx := context.Background()

	t.Run("valid runtime succeeds", func(t *testing.T) {
		v := &AgentRuntimeValidator{}
		_, err := v.ValidateCreate(ctx, validAgentRuntime())
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("wrong object type returns error", func(t *testing.T) {
		v := &AgentRuntimeValidator{}
		_, err := v.ValidateCreate(ctx, &corev1.Pod{})
		if err == nil {
			t.Fatal("expected error for wrong object type")
		}
		if !strings.Contains(err.Error(), "expected an AgentRuntime") {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("duplicate targetRef is rejected", func(t *testing.T) {
		existing := &agentv1alpha1.AgentRuntime{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "existing-runtime",
				Namespace: "default",
			},
			Spec: agentv1alpha1.AgentRuntimeSpec{
				Type: agentv1alpha1.RuntimeTypeTool,
				TargetRef: agentv1alpha1.TargetRef{
					APIVersion: "apps/v1",
					Kind:       "Deployment",
					Name:       "test",
				},
			},
		}
		v := &AgentRuntimeValidator{Reader: fakeRuntimeReader(existing)}

		_, err := v.ValidateCreate(ctx, validAgentRuntime())
		if err == nil {
			t.Fatal("expected error for duplicate targetRef")
		}
		if !strings.Contains(err.Error(), "an AgentRuntime already targets") {
			t.Errorf("unexpected error message: %v", err)
		}
		if !strings.Contains(err.Error(), "existing-runtime") {
			t.Errorf("error should reference the existing runtime name: %v", err)
		}
	})

	t.Run("no duplicate when targeting different workload", func(t *testing.T) {
		existing := &agentv1alpha1.AgentRuntime{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "other-runtime",
				Namespace: "default",
			},
			Spec: agentv1alpha1.AgentRuntimeSpec{
				Type: agentv1alpha1.RuntimeTypeAgent,
				TargetRef: agentv1alpha1.TargetRef{
					APIVersion: "apps/v1",
					Kind:       "Deployment",
					Name:       "other-workload",
				},
			},
		}
		v := &AgentRuntimeValidator{Reader: fakeRuntimeReader(existing)}

		_, err := v.ValidateCreate(ctx, validAgentRuntime())
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("no duplicate when targeting different kind", func(t *testing.T) {
		existing := &agentv1alpha1.AgentRuntime{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "sts-runtime",
				Namespace: "default",
			},
			Spec: agentv1alpha1.AgentRuntimeSpec{
				Type: agentv1alpha1.RuntimeTypeAgent,
				TargetRef: agentv1alpha1.TargetRef{
					APIVersion: "apps/v1",
					Kind:       "StatefulSet",
					Name:       "test",
				},
			},
		}
		v := &AgentRuntimeValidator{Reader: fakeRuntimeReader(existing)}

		_, err := v.ValidateCreate(ctx, validAgentRuntime())
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("nil reader skips duplicate check", func(t *testing.T) {
		v := &AgentRuntimeValidator{Reader: nil}
		_, err := v.ValidateCreate(ctx, validAgentRuntime())
		if err != nil {
			t.Errorf("unexpected error with nil reader: %v", err)
		}
	})

	t.Run("list error fails open", func(t *testing.T) {
		// Reader without AgentRuntime registered in scheme causes list to fail
		emptyScheme := runtime.NewScheme()
		brokenReader := fake.NewClientBuilder().WithScheme(emptyScheme).Build()
		v := &AgentRuntimeValidator{Reader: brokenReader}

		_, err := v.ValidateCreate(ctx, validAgentRuntime())
		if err != nil {
			t.Errorf("expected fail-open on list error, got: %v", err)
		}
	})

	t.Run("no duplicate when in different namespace", func(t *testing.T) {
		existing := &agentv1alpha1.AgentRuntime{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "other-runtime",
				Namespace: "other-ns",
			},
			Spec: agentv1alpha1.AgentRuntimeSpec{
				Type: agentv1alpha1.RuntimeTypeAgent,
				TargetRef: agentv1alpha1.TargetRef{
					APIVersion: "apps/v1",
					Kind:       "Deployment",
					Name:       "test",
				},
			},
		}
		v := &AgentRuntimeValidator{Reader: fakeRuntimeReader(existing)}

		_, err := v.ValidateCreate(ctx, validAgentRuntime())
		if err != nil {
			t.Errorf("unexpected error for different namespace: %v", err)
		}
	})
}

func TestAgentRuntimeValidator_ValidateUpdate(t *testing.T) {
	ctx := context.Background()
	old := validAgentRuntime()

	t.Run("wrong object type returns error", func(t *testing.T) {
		v := &AgentRuntimeValidator{}
		_, err := v.ValidateUpdate(ctx, old, &corev1.Pod{})
		if err == nil {
			t.Fatal("expected error for wrong object type")
		}
		if !strings.Contains(err.Error(), "expected an AgentRuntime") {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("valid update succeeds", func(t *testing.T) {
		v := &AgentRuntimeValidator{}
		_, err := v.ValidateUpdate(ctx, old, validAgentRuntime())
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("update to duplicate targetRef is rejected", func(t *testing.T) {
		existing := &agentv1alpha1.AgentRuntime{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "other-runtime",
				Namespace: "default",
			},
			Spec: agentv1alpha1.AgentRuntimeSpec{
				Type: agentv1alpha1.RuntimeTypeAgent,
				TargetRef: agentv1alpha1.TargetRef{
					APIVersion: "apps/v1",
					Kind:       "Deployment",
					Name:       "taken-workload",
				},
			},
		}
		v := &AgentRuntimeValidator{Reader: fakeRuntimeReader(existing)}

		updated := validAgentRuntime()
		updated.Spec.TargetRef.Name = "taken-workload"

		_, err := v.ValidateUpdate(ctx, old, updated)
		if err == nil {
			t.Fatal("expected error for duplicate targetRef on update")
		}
		if !strings.Contains(err.Error(), "an AgentRuntime already targets") {
			t.Errorf("unexpected error message: %v", err)
		}
	})

	t.Run("update same runtime same targetRef succeeds", func(t *testing.T) {
		self := validAgentRuntime()
		v := &AgentRuntimeValidator{Reader: fakeRuntimeReader(self)}

		_, err := v.ValidateUpdate(ctx, self, self)
		if err != nil {
			t.Errorf("unexpected error updating own targetRef: %v", err)
		}
	})
}

func TestAgentRuntimeValidator_ValidateDelete(t *testing.T) {
	v := &AgentRuntimeValidator{}
	ctx := context.Background()

	t.Run("with valid AgentRuntime succeeds", func(t *testing.T) {
		_, err := v.ValidateDelete(ctx, validAgentRuntime())
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
