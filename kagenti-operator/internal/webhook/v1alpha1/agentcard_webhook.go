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
	"fmt"

	agentv1alpha1 "github.com/kagenti/operator/api/v1alpha1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

var agentcardlog = ctrl.Log.WithName("agentcard-webhook")

// SetupAgentCardWebhookWithManager will setup the manager to manage the webhooks
func SetupAgentCardWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).
		For(&agentv1alpha1.AgentCard{}).
		WithValidator(&AgentCardValidator{}).
		Complete()
}

//+kubebuilder:webhook:path=/validate-agent-kagenti-dev-v1alpha1-agentcard,mutating=false,failurePolicy=fail,sideEffects=None,groups=agent.kagenti.dev,resources=agentcards,verbs=create;update,versions=v1alpha1,name=vagentcard.kb.io,admissionReviewVersions=v1

// AgentCardValidator implements validating webhook for AgentCard
type AgentCardValidator struct{}

// ValidateCreate implements webhook validation for create
func (v *AgentCardValidator) ValidateCreate(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	agentcard, ok := obj.(*agentv1alpha1.AgentCard)
	if !ok {
		return nil, fmt.Errorf("expected an AgentCard but got a %T", obj)
	}

	agentcardlog.Info("validate create", "name", agentcard.Name)

	return v.validateAgentCard(agentcard)
}

// ValidateUpdate implements webhook validation for update
func (v *AgentCardValidator) ValidateUpdate(ctx context.Context, oldObj, newObj runtime.Object) (admission.Warnings, error) {
	agentcard, ok := newObj.(*agentv1alpha1.AgentCard)
	if !ok {
		return nil, fmt.Errorf("expected an AgentCard but got a %T", newObj)
	}

	agentcardlog.Info("validate update", "name", agentcard.Name)

	return v.validateAgentCard(agentcard)
}

// ValidateDelete implements webhook validation for delete
func (v *AgentCardValidator) ValidateDelete(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	agentcard, ok := obj.(*agentv1alpha1.AgentCard)
	if !ok {
		return nil, fmt.Errorf("expected an AgentCard but got a %T", obj)
	}

	agentcardlog.Info("validate delete", "name", agentcard.Name)

	// Allow deletions
	return nil, nil
}

// validateAgentCard validates the AgentCard spec
func (v *AgentCardValidator) validateAgentCard(agentcard *agentv1alpha1.AgentCard) (admission.Warnings, error) {
	var warnings admission.Warnings

	// Validate that either targetRef or selector is specified
	if agentcard.Spec.TargetRef == nil && agentcard.Spec.Selector == nil {
		return nil, fmt.Errorf("either spec.targetRef or spec.selector must be specified")
	}

	// Field-level validation for targetRef (e.g., non-empty APIVersion/Kind/Name)
	// is enforced by the CRD schema (minLength constraints), so it is not repeated here.

	// Validate selector if specified
	if agentcard.Spec.Selector != nil {
		if len(agentcard.Spec.Selector.MatchLabels) == 0 {
			return nil, fmt.Errorf("spec.selector.matchLabels must not be empty")
		}

		// Add deprecation warning if only selector is used (no targetRef)
		if agentcard.Spec.TargetRef == nil {
			warnings = append(warnings,
				"spec.selector is deprecated; use spec.targetRef instead for explicit workload references")
		}
	}

	return warnings, nil
}
