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

package controller

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	netv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	agentv1alpha1 "github.com/kagenti/operator/api/v1alpha1"
)

const (
	// NetworkPolicyFinalizer is the finalizer for cleaning up network policies
	NetworkPolicyFinalizer = "agentcard.kagenti.dev/network-policy"

	// LabelSignatureVerified indicates if an agent's signature was verified
	LabelSignatureVerified = "agent.kagenti.dev/signature-verified"
)

var (
	networkPolicyLogger = ctrl.Log.WithName("controller").WithName("AgentCardNetworkPolicy")
)

// AgentCardNetworkPolicyReconciler reconciles AgentCard objects and manages NetworkPolicies
type AgentCardNetworkPolicyReconciler struct {
	client.Client
	Scheme                 *runtime.Scheme
	EnforceNetworkPolicies bool
}

// +kubebuilder:rbac:groups=networking.k8s.io,resources=networkpolicies,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=agent.kagenti.dev,resources=agentcards,verbs=get;list;watch
// +kubebuilder:rbac:groups=agent.kagenti.dev,resources=agents,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=core,resources=pods,verbs=get;list;watch;update;patch

func (r *AgentCardNetworkPolicyReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	networkPolicyLogger.Info("Reconciling AgentCard NetworkPolicy", "namespacedName", req.NamespacedName)

	// Skip if network policy enforcement is disabled
	if !r.EnforceNetworkPolicies {
		return ctrl.Result{}, nil
	}

	agentCard := &agentv1alpha1.AgentCard{}
	err := r.Get(ctx, req.NamespacedName, agentCard)
	if err != nil {
		if apierrors.IsNotFound(err) {
			// AgentCard deleted, cleanup handled by finalizer
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// Handle deletion
	if !agentCard.ObjectMeta.DeletionTimestamp.IsZero() {
		return r.handleDeletion(ctx, agentCard)
	}

	// Add finalizer
	if !controllerutil.ContainsFinalizer(agentCard, NetworkPolicyFinalizer) {
		controllerutil.AddFinalizer(agentCard, NetworkPolicyFinalizer)
		if err := r.Update(ctx, agentCard); err != nil {
			networkPolicyLogger.Error(err, "Unable to add finalizer to AgentCard")
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	// Find the matching Agent
	agent, err := r.findMatchingAgent(ctx, agentCard)
	if err != nil {
		networkPolicyLogger.Info("No matching agent found for AgentCard", "agentCard", agentCard.Name)
		// No agent yet, nothing to do
		return ctrl.Result{}, nil
	}

	// Update agent labels based on signature verification
	if err := r.updateAgentLabels(ctx, agent, agentCard); err != nil {
		networkPolicyLogger.Error(err, "Failed to update agent labels")
		return ctrl.Result{}, err
	}

	// Manage NetworkPolicy based on verification status
	if err := r.manageNetworkPolicy(ctx, agentCard, agent); err != nil {
		networkPolicyLogger.Error(err, "Failed to manage NetworkPolicy")
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// findMatchingAgent finds the Agent that matches the AgentCard selector
func (r *AgentCardNetworkPolicyReconciler) findMatchingAgent(ctx context.Context, agentCard *agentv1alpha1.AgentCard) (*agentv1alpha1.Agent, error) {
	agentList := &agentv1alpha1.AgentList{}
	listOpts := []client.ListOption{
		client.InNamespace(agentCard.Namespace),
		client.MatchingLabels(agentCard.Spec.Selector.MatchLabels),
	}

	if err := r.List(ctx, agentList, listOpts...); err != nil {
		return nil, fmt.Errorf("failed to list agents: %w", err)
	}

	if len(agentList.Items) == 0 {
		return nil, fmt.Errorf("no agents found matching selector")
	}

	return &agentList.Items[0], nil
}

// updateAgentLabels updates the agent's labels based on signature verification status
func (r *AgentCardNetworkPolicyReconciler) updateAgentLabels(ctx context.Context, agent *agentv1alpha1.Agent, agentCard *agentv1alpha1.AgentCard) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		// Fetch the latest version
		latest := &agentv1alpha1.Agent{}
		if err := r.Get(ctx, types.NamespacedName{
			Name:      agent.Name,
			Namespace: agent.Namespace,
		}, latest); err != nil {
			return err
		}

		// Initialize labels if nil
		if latest.Labels == nil {
			latest.Labels = make(map[string]string)
		}

		// Determine verification status
		verified := "false"
		if agentCard.Status.ValidSignature != nil && *agentCard.Status.ValidSignature {
			verified = "true"
		}

		// Update label if changed
		if latest.Labels[LabelSignatureVerified] != verified {
			latest.Labels[LabelSignatureVerified] = verified
			networkPolicyLogger.Info("Updating agent signature verification label",
				"agent", agent.Name,
				"verified", verified)
			return r.Update(ctx, latest)
		}

		return nil
	})
}

// manageNetworkPolicy creates or updates NetworkPolicy based on verification status
func (r *AgentCardNetworkPolicyReconciler) manageNetworkPolicy(ctx context.Context, agentCard *agentv1alpha1.AgentCard, agent *agentv1alpha1.Agent) error {
	policyName := fmt.Sprintf("%s-signature-policy", agent.Name)

	// Determine if signature is verified
	isVerified := agentCard.Status.ValidSignature != nil && *agentCard.Status.ValidSignature

	if isVerified {
		// Signature is valid - create/update permissive policy
		return r.createPermissivePolicy(ctx, policyName, agent, agentCard)
	} else {
		// Signature is invalid or missing - create/update restrictive policy
		return r.createRestrictivePolicy(ctx, policyName, agent, agentCard)
	}
}

// createPermissivePolicy creates a NetworkPolicy that allows verified agents to communicate
func (r *AgentCardNetworkPolicyReconciler) createPermissivePolicy(ctx context.Context, policyName string, agent *agentv1alpha1.Agent, agentCard *agentv1alpha1.AgentCard) error {
	policy := &netv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      policyName,
			Namespace: agent.Namespace,
			Labels: map[string]string{
				"managed-by":              "kagenti-operator",
				"kagenti.dev/agent":       agent.Name,
				"kagenti.dev/policy-type": "signature-verification",
			},
		},
		Spec: netv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{
				MatchLabels: agentCard.Spec.Selector.MatchLabels,
			},
			PolicyTypes: []netv1.PolicyType{
				netv1.PolicyTypeIngress,
				netv1.PolicyTypeEgress,
			},
			Ingress: []netv1.NetworkPolicyIngressRule{
				{
					// Allow traffic from other verified agents
					From: []netv1.NetworkPolicyPeer{
						{
							PodSelector: &metav1.LabelSelector{
								MatchLabels: map[string]string{
									LabelSignatureVerified: "true",
								},
							},
						},
						{
							// Allow traffic from operator/control plane
							NamespaceSelector: &metav1.LabelSelector{
								MatchLabels: map[string]string{
									"control-plane": "kagenti-operator",
								},
							},
						},
					},
				},
			},
			Egress: []netv1.NetworkPolicyEgressRule{
				{
					// Allow egress to other verified agents
					To: []netv1.NetworkPolicyPeer{
						{
							PodSelector: &metav1.LabelSelector{
								MatchLabels: map[string]string{
									LabelSignatureVerified: "true",
								},
							},
						},
					},
				},
				{
					// Allow DNS queries
					To: []netv1.NetworkPolicyPeer{
						{
							NamespaceSelector: &metav1.LabelSelector{
								MatchLabels: map[string]string{
									"kubernetes.io/metadata.name": "kube-system",
								},
							},
						},
					},
					Ports: []netv1.NetworkPolicyPort{
						{
							Protocol: func() *corev1.Protocol { p := corev1.ProtocolUDP; return &p }(),
							Port:     &intstr.IntOrString{Type: intstr.Int, IntVal: 53},
						},
						{
							Protocol: func() *corev1.Protocol { p := corev1.ProtocolTCP; return &p }(),
							Port:     &intstr.IntOrString{Type: intstr.Int, IntVal: 53},
						},
					},
				},
				{
					// Allow egress to API server
					To: []netv1.NetworkPolicyPeer{
						{
							NamespaceSelector: &metav1.LabelSelector{
								MatchLabels: map[string]string{
									"kubernetes.io/metadata.name": "default",
								},
							},
							PodSelector: &metav1.LabelSelector{
								MatchLabels: map[string]string{
									"component": "apiserver",
								},
							},
						},
					},
				},
			},
		},
	}

	// Set owner reference
	if err := controllerutil.SetControllerReference(agentCard, policy, r.Scheme); err != nil {
		return fmt.Errorf("failed to set controller reference: %w", err)
	}

	// Create or update the policy
	existingPolicy := &netv1.NetworkPolicy{}
	err := r.Get(ctx, types.NamespacedName{Name: policyName, Namespace: agent.Namespace}, existingPolicy)
	if err != nil {
		if apierrors.IsNotFound(err) {
			networkPolicyLogger.Info("Creating permissive NetworkPolicy for verified agent",
				"agent", agent.Name,
				"policy", policyName)
			return r.Create(ctx, policy)
		}
		return err
	}

	// Update existing policy
	existingPolicy.Spec = policy.Spec
	networkPolicyLogger.Info("Updating NetworkPolicy to permissive for verified agent",
		"agent", agent.Name,
		"policy", policyName)
	return r.Update(ctx, existingPolicy)
}

// createRestrictivePolicy creates a NetworkPolicy that blocks unverified agents
func (r *AgentCardNetworkPolicyReconciler) createRestrictivePolicy(ctx context.Context, policyName string, agent *agentv1alpha1.Agent, agentCard *agentv1alpha1.AgentCard) error {
	policy := &netv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      policyName,
			Namespace: agent.Namespace,
			Labels: map[string]string{
				"managed-by":              "kagenti-operator",
				"kagenti.dev/agent":       agent.Name,
				"kagenti.dev/policy-type": "signature-verification",
			},
		},
		Spec: netv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{
				MatchLabels: agentCard.Spec.Selector.MatchLabels,
			},
			PolicyTypes: []netv1.PolicyType{
				netv1.PolicyTypeIngress,
				netv1.PolicyTypeEgress,
			},
			// Empty rules = deny all traffic except what's explicitly allowed below
			Ingress: []netv1.NetworkPolicyIngressRule{
				{
					// Only allow traffic from operator for health checks
					From: []netv1.NetworkPolicyPeer{
						{
							NamespaceSelector: &metav1.LabelSelector{
								MatchLabels: map[string]string{
									"control-plane": "kagenti-operator",
								},
							},
						},
					},
				},
			},
			Egress: []netv1.NetworkPolicyEgressRule{
				{
					// Allow DNS queries only
					To: []netv1.NetworkPolicyPeer{
						{
							NamespaceSelector: &metav1.LabelSelector{
								MatchLabels: map[string]string{
									"kubernetes.io/metadata.name": "kube-system",
								},
							},
						},
					},
					Ports: []netv1.NetworkPolicyPort{
						{
							Protocol: func() *corev1.Protocol { p := corev1.ProtocolUDP; return &p }(),
							Port:     &intstr.IntOrString{Type: intstr.Int, IntVal: 53},
						},
						{
							Protocol: func() *corev1.Protocol { p := corev1.ProtocolTCP; return &p }(),
							Port:     &intstr.IntOrString{Type: intstr.Int, IntVal: 53},
						},
					},
				},
			},
		},
	}

	// Set owner reference
	if err := controllerutil.SetControllerReference(agentCard, policy, r.Scheme); err != nil {
		return fmt.Errorf("failed to set controller reference: %w", err)
	}

	// Create or update the policy
	existingPolicy := &netv1.NetworkPolicy{}
	err := r.Get(ctx, types.NamespacedName{Name: policyName, Namespace: agent.Namespace}, existingPolicy)
	if err != nil {
		if apierrors.IsNotFound(err) {
			networkPolicyLogger.Info("Creating restrictive NetworkPolicy for unverified agent",
				"agent", agent.Name,
				"policy", policyName,
				"reason", "signature not verified")
			return r.Create(ctx, policy)
		}
		return err
	}

	// Update existing policy
	existingPolicy.Spec = policy.Spec
	networkPolicyLogger.Info("Updating NetworkPolicy to restrictive for unverified agent",
		"agent", agent.Name,
		"policy", policyName)
	return r.Update(ctx, existingPolicy)
}

// handleDeletion handles cleanup when an AgentCard is deleted
func (r *AgentCardNetworkPolicyReconciler) handleDeletion(ctx context.Context, agentCard *agentv1alpha1.AgentCard) (ctrl.Result, error) {
	if controllerutil.ContainsFinalizer(agentCard, NetworkPolicyFinalizer) {
		networkPolicyLogger.Info("Cleaning up NetworkPolicy for AgentCard", "name", agentCard.Name)

		// Find the matching agent
		agent, err := r.findMatchingAgent(ctx, agentCard)
		if err == nil {
			// Delete the NetworkPolicy
			policyName := fmt.Sprintf("%s-signature-policy", agent.Name)
			policy := &netv1.NetworkPolicy{}
			err := r.Get(ctx, types.NamespacedName{Name: policyName, Namespace: agent.Namespace}, policy)
			if err == nil {
				if err := r.Delete(ctx, policy); err != nil {
					networkPolicyLogger.Error(err, "Failed to delete NetworkPolicy")
					return ctrl.Result{}, err
				}
				networkPolicyLogger.Info("Deleted NetworkPolicy", "policy", policyName)
			}
		}

		// Remove finalizer
		if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			latest := &agentv1alpha1.AgentCard{}
			if err := r.Get(ctx, types.NamespacedName{
				Name:      agentCard.Name,
				Namespace: agentCard.Namespace,
			}, latest); err != nil {
				return err
			}

			controllerutil.RemoveFinalizer(latest, NetworkPolicyFinalizer)
			return r.Update(ctx, latest)
		}); err != nil {
			networkPolicyLogger.Error(err, "Failed to remove finalizer from AgentCard")
			return ctrl.Result{}, err
		}

		networkPolicyLogger.Info("Removed finalizer from AgentCard")
	}

	return ctrl.Result{}, nil
}

// mapAgentToAgentCard maps Agent events to AgentCard reconcile requests
func (r *AgentCardNetworkPolicyReconciler) mapAgentToAgentCard(ctx context.Context, obj client.Object) []reconcile.Request {
	agent, ok := obj.(*agentv1alpha1.Agent)
	if !ok {
		return nil
	}

	// Find all AgentCards that might reference this Agent
	agentCardList := &agentv1alpha1.AgentCardList{}
	if err := r.List(ctx, agentCardList, client.InNamespace(agent.Namespace)); err != nil {
		networkPolicyLogger.Error(err, "Failed to list AgentCards for mapping")
		return nil
	}

	var requests []reconcile.Request
	for _, agentCard := range agentCardList.Items {
		// Check if this AgentCard's selector matches the Agent
		if r.selectorMatchesAgent(&agentCard, agent) {
			requests = append(requests, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      agentCard.Name,
					Namespace: agentCard.Namespace,
				},
			})
		}
	}

	return requests
}

// selectorMatchesAgent checks if an AgentCard selector matches an Agent
func (r *AgentCardNetworkPolicyReconciler) selectorMatchesAgent(agentCard *agentv1alpha1.AgentCard, agent *agentv1alpha1.Agent) bool {
	if agent.Labels == nil {
		return false
	}

	for key, value := range agentCard.Spec.Selector.MatchLabels {
		if agent.Labels[key] != value {
			return false
		}
	}

	return true
}

// SetupWithManager sets up the controller with the Manager.
func (r *AgentCardNetworkPolicyReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&agentv1alpha1.AgentCard{}).
		Owns(&netv1.NetworkPolicy{}).
		Watches(
			&agentv1alpha1.Agent{},
			handler.EnqueueRequestsFromMapFunc(r.mapAgentToAgentCard),
		).
		Named("AgentCardNetworkPolicy").
		Complete(r)
}
