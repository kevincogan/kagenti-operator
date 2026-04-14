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
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	agentv1alpha1 "github.com/kagenti/operator/api/v1alpha1"
)

var writerLogger = ctrl.Log.WithName("agentcard").WithName("writer")

// Writer creates or updates signed AgentCard ConfigMaps.
type Writer struct {
	client client.Client
	reader client.Reader
	scheme *runtime.Scheme
}

// NewWriter creates a Writer for signed AgentCard ConfigMaps.
// The reader should be an uncached API reader (mgr.GetAPIReader()) to avoid
// cache-scoping issues when the ConfigMap is in a different namespace.
func NewWriter(c client.Client, reader client.Reader, scheme *runtime.Scheme) *Writer {
	return &Writer{client: c, reader: reader, scheme: scheme}
}

// ConfigMapName returns the canonical ConfigMap name for a signed agent card.
func ConfigMapName(agentName string) string {
	return agentName + SignedCardConfigMapSuffix
}

// WriteSignedCard creates or updates the signed card ConfigMap, setting an
// owner reference to the AgentCard CR for automatic garbage collection.
func (w *Writer) WriteSignedCard(ctx context.Context, owner *agentv1alpha1.AgentCard, agentName, namespace string, signedCard []byte) error {
	cmName := ConfigMapName(agentName)

	cm := &corev1.ConfigMap{}
	key := types.NamespacedName{Name: cmName, Namespace: namespace}
	err := w.reader.Get(ctx, key, cm)

	if apierrors.IsNotFound(err) {
		cm = &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      cmName,
				Namespace: namespace,
			},
			Data: map[string]string{
				SignedCardConfigMapKey: string(signedCard),
			},
		}
		if err := controllerutil.SetControllerReference(owner, cm, w.scheme); err != nil {
			return fmt.Errorf("failed to set owner reference on ConfigMap %s: %w", cmName, err)
		}
		if err := w.client.Create(ctx, cm); err != nil {
			return fmt.Errorf("failed to create ConfigMap %s: %w", cmName, err)
		}
		writerLogger.Info("Created signed card ConfigMap", "configMap", cmName, "namespace", namespace)
		return nil
	} else if err != nil {
		return fmt.Errorf("failed to get ConfigMap %s: %w", cmName, err)
	}

	cm.Data = map[string]string{
		SignedCardConfigMapKey: string(signedCard),
	}
	if err := controllerutil.SetControllerReference(owner, cm, w.scheme); err != nil {
		writerLogger.V(1).Info("Could not set owner reference on existing ConfigMap (may already be owned)",
			"configMap", cmName, "error", err.Error())
	}
	if err := w.client.Update(ctx, cm); err != nil {
		return fmt.Errorf("failed to update ConfigMap %s: %w", cmName, err)
	}
	writerLogger.Info("Updated signed card ConfigMap", "configMap", cmName, "namespace", namespace)
	return nil
}
