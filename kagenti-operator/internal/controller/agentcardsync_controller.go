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

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	agentv1alpha1 "github.com/kagenti/operator/api/v1alpha1"
)

var (
	syncLogger = ctrl.Log.WithName("controller").WithName("AgentCardSync")
)

// AgentCardSyncReconciler automatically creates AgentCard resources for agent workloads
// (Deployments, StatefulSets, and legacy Agent CRDs)
type AgentCardSyncReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	// EnableLegacyAgentCRD enables watching legacy Agent CRD resources
	EnableLegacyAgentCRD bool
}

// +kubebuilder:rbac:groups=agent.kagenti.dev,resources=agents,verbs=get;list;watch
// +kubebuilder:rbac:groups=agent.kagenti.dev,resources=agentcards,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch
// +kubebuilder:rbac:groups=apps,resources=statefulsets,verbs=get;list;watch

// ReconcileDeployment handles Deployment events to create/update AgentCards
func (r *AgentCardSyncReconciler) ReconcileDeployment(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	syncLogger.Info("Reconciling Deployment for auto-sync", "namespacedName", req.NamespacedName)

	deployment := &appsv1.Deployment{}
	if err := r.Get(ctx, req.NamespacedName, deployment); err != nil {
		if errors.IsNotFound(err) {
			// Deployment deleted - AgentCard cleanup handled by owner references
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// Check if this is an agent deployment
	if !r.shouldSyncWorkload(deployment.Labels) {
		return ctrl.Result{}, nil
	}

	// Create or update AgentCard with targetRef
	gvk := appsv1.SchemeGroupVersion.WithKind("Deployment")
	return r.ensureAgentCard(ctx, deployment, gvk)
}

// ReconcileStatefulSet handles StatefulSet events to create/update AgentCards
func (r *AgentCardSyncReconciler) ReconcileStatefulSet(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	syncLogger.Info("Reconciling StatefulSet for auto-sync", "namespacedName", req.NamespacedName)

	statefulset := &appsv1.StatefulSet{}
	if err := r.Get(ctx, req.NamespacedName, statefulset); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	if !r.shouldSyncWorkload(statefulset.Labels) {
		return ctrl.Result{}, nil
	}

	gvk := appsv1.SchemeGroupVersion.WithKind("StatefulSet")
	return r.ensureAgentCard(ctx, statefulset, gvk)
}

// ReconcileAgent handles legacy Agent CRD events (backward compatibility)
func (r *AgentCardSyncReconciler) ReconcileAgent(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	syncLogger.Info("Reconciling Agent for auto-sync", "namespacedName", req.NamespacedName)

	agent := &agentv1alpha1.Agent{}
	err := r.Get(ctx, req.NamespacedName, agent)
	if err != nil {
		if errors.IsNotFound(err) {
			// Agent deleted, check for orphaned AgentCards
			return r.cleanupOrphanedCards(ctx, req.NamespacedName)
		}
		return ctrl.Result{}, err
	}

	// Check if Agent has the required labels
	if !r.shouldSyncAgent(agent) {
		syncLogger.Info("Agent does not have agent card labels, skipping", "agent", agent.Name)
		return ctrl.Result{}, nil
	}

	// Create or update AgentCard with targetRef
	gvk := agentv1alpha1.GroupVersion.WithKind("Agent")
	return r.ensureAgentCard(ctx, agent, gvk)
}

// shouldSyncWorkload checks if a workload should have an AgentCard created
func (r *AgentCardSyncReconciler) shouldSyncWorkload(labels map[string]string) bool {
	if labels == nil {
		return false
	}

	// Must have kagenti.io/type=agent label
	if labels[LabelAgentType] != LabelValueAgent {
		return false
	}

	// Must have protocol label (new or old format)
	if labels[LabelKagentiProtocol] != "" {
		return true
	}

	// Fall back to old label
	if labels[LabelAgentProtocol] != "" {
		return true
	}

	return false
}

// shouldSyncAgent checks if an Agent should have an AgentCard created
func (r *AgentCardSyncReconciler) shouldSyncAgent(agent *agentv1alpha1.Agent) bool {
	if agent.Labels == nil {
		return false
	}

	// Must have kagenti.io/type=agent label
	if agent.Labels[LabelAgentType] != LabelValueAgent {
		return false
	}

	// Check for protocol label - support both old and new labels
	// Try new label first
	if agent.Labels[LabelKagentiProtocol] != "" {
		return true
	}

	// Fall back to old label with deprecation warning
	if agent.Labels[LabelAgentProtocol] != "" {
		syncLogger.Info("DEPRECATION WARNING: Agent uses deprecated label 'kagenti.io/agent-protocol', please migrate to 'kagenti.io/protocol'",
			"agent", agent.Name,
			"protocol", agent.Labels[LabelAgentProtocol])
		return true
	}

	syncLogger.Info("Agent has type=agent but no protocol label", "agent", agent.Name)
	return false
}

// getAgentCardName generates the AgentCard name from workload name
func (r *AgentCardSyncReconciler) getAgentCardNameFromWorkload(workloadName string) string {
	return workloadName + "-card"
}

// getAgentCardName generates the AgentCard name for an Agent (backward compatibility)
func (r *AgentCardSyncReconciler) getAgentCardName(agent *agentv1alpha1.Agent) string {
	return r.getAgentCardNameFromWorkload(agent.Name)
}

// ensureAgentCard creates or updates an AgentCard for a workload using targetRef
func (r *AgentCardSyncReconciler) ensureAgentCard(ctx context.Context, obj client.Object, gvk schema.GroupVersionKind) (ctrl.Result, error) {
	cardName := r.getAgentCardNameFromWorkload(obj.GetName())

	// Check if AgentCard already exists
	existingCard := &agentv1alpha1.AgentCard{}
	err := r.Get(ctx, types.NamespacedName{
		Name:      cardName,
		Namespace: obj.GetNamespace(),
	}, existingCard)

	if err == nil {
		// Card exists - ensure owner reference is set and targetRef is correct
		needsUpdate := false

		if !r.hasOwnerReferenceForObject(existingCard, obj) {
			syncLogger.Info("Adding owner reference to existing AgentCard",
				"agentCard", cardName, "owner", obj.GetName(), "kind", gvk.Kind)
			if err := controllerutil.SetControllerReference(obj, existingCard, r.Scheme); err != nil {
				syncLogger.Error(err, "Failed to set owner reference")
				return ctrl.Result{}, err
			}
			needsUpdate = true
		}

		// Update targetRef if it's still using the old selector format
		if existingCard.Spec.TargetRef == nil && existingCard.Spec.Selector != nil {
			syncLogger.Info("Migrating AgentCard from selector to targetRef",
				"agentCard", cardName)
			existingCard.Spec.TargetRef = &agentv1alpha1.TargetRef{
				APIVersion: gvk.GroupVersion().String(),
				Kind:       gvk.Kind,
				Name:       obj.GetName(),
			}
			needsUpdate = true
		}

		if needsUpdate {
			if err := r.Update(ctx, existingCard); err != nil {
				syncLogger.Error(err, "Failed to update AgentCard")
				return ctrl.Result{}, err
			}
			syncLogger.Info("Successfully updated AgentCard", "agentCard", cardName)
		}
		return ctrl.Result{}, nil
	}

	if !errors.IsNotFound(err) {
		return ctrl.Result{}, err
	}

	// Create new AgentCard with targetRef
	return r.createAgentCardForWorkload(ctx, obj, gvk, cardName)
}

// createAgentCardForWorkload creates a new AgentCard for a workload using targetRef
func (r *AgentCardSyncReconciler) createAgentCardForWorkload(ctx context.Context, obj client.Object, gvk schema.GroupVersionKind, cardName string) (ctrl.Result, error) {
	syncLogger.Info("Creating AgentCard for workload",
		"agentCard", cardName,
		"kind", gvk.Kind,
		"workload", obj.GetName())

	labels := obj.GetLabels()
	appName := labels["app.kubernetes.io/name"]
	if appName == "" {
		appName = obj.GetName()
	}

	agentCard := &agentv1alpha1.AgentCard{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cardName,
			Namespace: obj.GetNamespace(),
			Labels: map[string]string{
				"app.kubernetes.io/name":       appName,
				"app.kubernetes.io/managed-by": "kagenti-operator",
			},
		},
		Spec: agentv1alpha1.AgentCardSpec{
			SyncPeriod: "30s",
			TargetRef: &agentv1alpha1.TargetRef{
				APIVersion: gvk.GroupVersion().String(),
				Kind:       gvk.Kind,
				Name:       obj.GetName(),
			},
		},
	}

	// Set owner reference for garbage collection
	if err := controllerutil.SetControllerReference(obj, agentCard, r.Scheme); err != nil {
		syncLogger.Error(err, "Failed to set controller reference for AgentCard")
		return ctrl.Result{}, err
	}

	if err := r.Create(ctx, agentCard); err != nil {
		if errors.IsAlreadyExists(err) {
			syncLogger.Info("AgentCard already exists", "agentCard", agentCard.Name)
			return ctrl.Result{}, nil
		}
		syncLogger.Error(err, "Failed to create AgentCard")
		return ctrl.Result{}, err
	}

	syncLogger.Info("Successfully created AgentCard", "agentCard", agentCard.Name)
	return ctrl.Result{}, nil
}

// hasOwnerReferenceForObject checks if an AgentCard has the correct owner reference for any object
func (r *AgentCardSyncReconciler) hasOwnerReferenceForObject(agentCard *agentv1alpha1.AgentCard, obj client.Object) bool {
	for _, ownerRef := range agentCard.OwnerReferences {
		if ownerRef.UID == obj.GetUID() {
			return true
		}
	}
	return false
}

// hasOwnerReference checks if an AgentCard has the correct owner reference (backward compatibility)
func (r *AgentCardSyncReconciler) hasOwnerReference(agentCard *agentv1alpha1.AgentCard, agent *agentv1alpha1.Agent) bool {
	return r.hasOwnerReferenceForObject(agentCard, agent)
}

// cleanupOrphanedCards removes AgentCard resources for deleted Agents
func (r *AgentCardSyncReconciler) cleanupOrphanedCards(ctx context.Context, agentName types.NamespacedName) (ctrl.Result, error) {
	// Check if there's an AgentCard that should be cleaned up
	agentCardName := fmt.Sprintf("%s-card", agentName.Name)
	agentCard := &agentv1alpha1.AgentCard{}

	err := r.Get(ctx, types.NamespacedName{
		Name:      agentCardName,
		Namespace: agentName.Namespace,
	}, agentCard)

	if err != nil {
		// AgentCard doesn't exist or was already deleted, nothing to do
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Check if this AgentCard is orphaned (no valid owner reference)
	hasValidOwner := false
	for _, ownerRef := range agentCard.OwnerReferences {
		if ownerRef.Kind == "Agent" {
			// Try to get the owner Agent
			agent := &agentv1alpha1.Agent{}
			err := r.Get(ctx, types.NamespacedName{
				Name:      ownerRef.Name,
				Namespace: agentCard.Namespace,
			}, agent)
			if err == nil {
				hasValidOwner = true
				break
			}
		}
	}

	if !hasValidOwner {
		syncLogger.Info("Deleting orphaned AgentCard", "agentCard", agentCard.Name)
		if err := r.Delete(ctx, agentCard); err != nil {
			syncLogger.Error(err, "Failed to delete orphaned AgentCard")
			return ctrl.Result{}, err
		}
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
// It creates separate controllers for Deployments, StatefulSets, and optionally Agent CRDs.
func (r *AgentCardSyncReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// Watch Deployments with agent labels
	if err := ctrl.NewControllerManagedBy(mgr).
		Named("agentcardsync-deployment").
		For(&appsv1.Deployment{}).
		WithEventFilter(agentLabelPredicate()).
		Complete(&deploymentReconcilerAdapter{r}); err != nil {
		return err
	}

	// Watch StatefulSets with agent labels
	if err := ctrl.NewControllerManagedBy(mgr).
		Named("agentcardsync-statefulset").
		For(&appsv1.StatefulSet{}).
		WithEventFilter(agentLabelPredicate()).
		Complete(&statefulSetReconcilerAdapter{r}); err != nil {
		return err
	}

	// Optionally watch legacy Agent CRD
	if r.EnableLegacyAgentCRD {
		syncLogger.Info("Legacy Agent CRD support is enabled for AgentCardSync, watching Agent resources")
		if err := ctrl.NewControllerManagedBy(mgr).
			Named("agentcardsync-agent").
			For(&agentv1alpha1.Agent{}).
			WithEventFilter(agentLabelPredicate()).
			Complete(&agentReconcilerAdapter{r}); err != nil {
			return err
		}
	} else {
		syncLogger.Info("Legacy Agent CRD support is disabled for AgentCardSync, not watching Agent resources")
	}

	return nil
}

// deploymentReconcilerAdapter adapts AgentCardSyncReconciler to handle Deployment reconcile requests
type deploymentReconcilerAdapter struct {
	*AgentCardSyncReconciler
}

func (a *deploymentReconcilerAdapter) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	return a.ReconcileDeployment(ctx, req)
}

// statefulSetReconcilerAdapter adapts AgentCardSyncReconciler to handle StatefulSet reconcile requests
type statefulSetReconcilerAdapter struct {
	*AgentCardSyncReconciler
}

func (a *statefulSetReconcilerAdapter) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	return a.ReconcileStatefulSet(ctx, req)
}

// agentReconcilerAdapter adapts AgentCardSyncReconciler to handle Agent reconcile requests
type agentReconcilerAdapter struct {
	*AgentCardSyncReconciler
}

func (a *agentReconcilerAdapter) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	return a.ReconcileAgent(ctx, req)
}
