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
	"fmt"

	agentv1alpha1 "github.com/kagenti/operator/api/v1alpha1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

var agentruntimelog = ctrl.Log.WithName("agentruntime-webhook")

func SetupAgentRuntimeWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).
		For(&agentv1alpha1.AgentRuntime{}).
		WithValidator(&AgentRuntimeValidator{Reader: mgr.GetAPIReader()}).
		Complete()
}

// +kubebuilder:webhook:path=/validate-agent-kagenti-dev-v1alpha1-agentruntime,mutating=false,failurePolicy=fail,sideEffects=None,groups=agent.kagenti.dev,resources=agentruntimes,verbs=create;update,versions=v1alpha1,name=vagentruntime.kb.io,admissionReviewVersions=v1

type AgentRuntimeValidator struct {
	// Reader is an uncached client for authoritative reads from the API server.
	// Used for duplicate targetRef checks during admission. Nil-safe: the check
	// is skipped when Reader is nil (e.g., in unit tests without a real API server).
	Reader client.Reader
}

func (v *AgentRuntimeValidator) ValidateCreate(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	rt, ok := obj.(*agentv1alpha1.AgentRuntime)
	if !ok {
		return nil, fmt.Errorf("expected an AgentRuntime but got a %T", obj)
	}

	agentruntimelog.Info("validate create", "name", rt.Name)

	if err := v.checkDuplicateTargetRef(ctx, rt); err != nil {
		return nil, err
	}

	return nil, nil
}

func (v *AgentRuntimeValidator) ValidateUpdate(ctx context.Context, oldObj, newObj runtime.Object) (admission.Warnings, error) {
	rt, ok := newObj.(*agentv1alpha1.AgentRuntime)
	if !ok {
		return nil, fmt.Errorf("expected an AgentRuntime but got a %T", newObj)
	}

	agentruntimelog.Info("validate update", "name", rt.Name)

	if err := v.checkDuplicateTargetRef(ctx, rt); err != nil {
		return nil, err
	}

	return nil, nil
}

func (v *AgentRuntimeValidator) ValidateDelete(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	rt, ok := obj.(*agentv1alpha1.AgentRuntime)
	if !ok {
		return nil, fmt.Errorf("expected an AgentRuntime but got a %T", obj)
	}

	agentruntimelog.Info("validate delete", "name", rt.Name)

	return nil, nil
}

// checkDuplicateTargetRef rejects creation/update if another AgentRuntime already
// targets the same workload (apiVersion + kind + name) in the same namespace.
func (v *AgentRuntimeValidator) checkDuplicateTargetRef(ctx context.Context, rt *agentv1alpha1.AgentRuntime) error {
	if v.Reader == nil {
		return nil
	}

	ref := rt.Spec.TargetRef

	rtList := &agentv1alpha1.AgentRuntimeList{}
	// fail-open: allow creation if we can't verify uniqueness
	if err := v.Reader.List(ctx, rtList, client.InNamespace(rt.Namespace)); err != nil {
		agentruntimelog.Error(err, "failed to list AgentRuntimes for duplicate check")
		return nil
	}

	for i := range rtList.Items {
		existing := &rtList.Items[i]
		if existing.Name == rt.Name {
			continue
		}
		if existing.Spec.TargetRef.APIVersion == ref.APIVersion &&
			existing.Spec.TargetRef.Kind == ref.Kind &&
			existing.Spec.TargetRef.Name == ref.Name {
			return fmt.Errorf(
				"an AgentRuntime already targets %s %s in namespace %s: %s",
				ref.Kind, ref.Name, rt.Namespace, existing.Name,
			)
		}
	}

	return nil
}
