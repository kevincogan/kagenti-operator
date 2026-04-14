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

package agentcard

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	agentv1alpha1 "github.com/kagenti/operator/api/v1alpha1"
)

func testScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	utilruntime.Must(corev1.AddToScheme(s))
	utilruntime.Must(agentv1alpha1.AddToScheme(s))
	return s
}

func testAgentCard() *agentv1alpha1.AgentCard {
	return &agentv1alpha1.AgentCard{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-card",
			Namespace: "test-ns",
			UID:       "test-uid-123",
		},
	}
}

func TestWriter_CreateConfigMap(t *testing.T) {
	scheme := testScheme()
	client := fake.NewClientBuilder().WithScheme(scheme).Build()
	writer := NewWriter(client, client, scheme)

	owner := testAgentCard()
	signedCard := []byte(`{"name":"test-agent","signatures":[{"protected":"xxx","signature":"yyy"}]}`)

	err := writer.WriteSignedCard(context.Background(), owner, "my-agent", "test-ns", signedCard)
	if err != nil {
		t.Fatalf("WriteSignedCard failed: %v", err)
	}

	cm := &corev1.ConfigMap{}
	err = client.Get(context.Background(), types.NamespacedName{
		Name:      "my-agent-card-signed",
		Namespace: "test-ns",
	}, cm)
	if err != nil {
		t.Fatalf("ConfigMap not created: %v", err)
	}

	if cm.Data[SignedCardConfigMapKey] != string(signedCard) {
		t.Errorf("ConfigMap data mismatch: got %q", cm.Data[SignedCardConfigMapKey])
	}
}

func TestWriter_UpdateConfigMap(t *testing.T) {
	scheme := testScheme()
	existing := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-agent-card-signed",
			Namespace: "test-ns",
		},
		Data: map[string]string{SignedCardConfigMapKey: `{"name":"old"}`},
	}
	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(existing).Build()
	writer := NewWriter(client, client, scheme)

	owner := testAgentCard()
	newCard := []byte(`{"name":"updated"}`)

	err := writer.WriteSignedCard(context.Background(), owner, "my-agent", "test-ns", newCard)
	if err != nil {
		t.Fatalf("WriteSignedCard update failed: %v", err)
	}

	cm := &corev1.ConfigMap{}
	if err = client.Get(context.Background(), types.NamespacedName{
		Name:      "my-agent-card-signed",
		Namespace: "test-ns",
	}, cm); err != nil {
		t.Fatalf("ConfigMap not found after update: %v", err)
	}

	if cm.Data[SignedCardConfigMapKey] != string(newCard) {
		t.Errorf("ConfigMap not updated: got %q", cm.Data[SignedCardConfigMapKey])
	}
}

func TestWriter_OwnerReference(t *testing.T) {
	scheme := testScheme()
	client := fake.NewClientBuilder().WithScheme(scheme).Build()
	writer := NewWriter(client, client, scheme)

	owner := testAgentCard()
	err := writer.WriteSignedCard(context.Background(), owner, "my-agent", "test-ns", []byte(`{}`))
	if err != nil {
		t.Fatalf("WriteSignedCard failed: %v", err)
	}

	cm := &corev1.ConfigMap{}
	if err = client.Get(context.Background(), types.NamespacedName{
		Name:      "my-agent-card-signed",
		Namespace: "test-ns",
	}, cm); err != nil {
		t.Fatalf("ConfigMap not found: %v", err)
	}

	if len(cm.OwnerReferences) == 0 {
		t.Error("expected owner reference on ConfigMap")
	} else {
		ownerRef := cm.OwnerReferences[0]
		if ownerRef.Name != "test-card" {
			t.Errorf("expected owner name 'test-card', got %q", ownerRef.Name)
		}
		if ownerRef.UID != "test-uid-123" {
			t.Errorf("expected owner UID 'test-uid-123', got %q", ownerRef.UID)
		}
	}
}
