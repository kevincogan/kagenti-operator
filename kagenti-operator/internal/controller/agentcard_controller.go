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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	agentv1alpha1 "github.com/kagenti/operator/api/v1alpha1"
	"github.com/kagenti/operator/internal/agentcard"
)

const (
	// Label keys
	LabelAgentType     = "kagenti.io/type"
	LabelAgentProtocol = "kagenti.io/agent-protocol"

	// Label values
	LabelValueAgent = "agent"

	// Finalizer
	AgentCardFinalizer = "agentcard.kagenti.dev/finalizer"

	// Default sync period
	DefaultSyncPeriod = 30 * time.Second

	// Default trust domain for SPIFFE IDs
	DefaultTrustDomain = "cluster.local"

	// Annotation keys for binding enforcement
	AnnotationDisabledBy     = "kagenti.io/disabled-by"
	AnnotationDisabledReason = "kagenti.io/disabled-reason"

	// Annotation values
	DisabledByIdentityBinding = "identity-binding"

	// Binding status reasons
	ReasonBound                 = "Bound"
	ReasonNotBound              = "NotBound"
	ReasonAgentNotFound         = "AgentNotFound"
	ReasonMultipleAgentsMatched = "MultipleAgentsMatched"
	ReasonNoTrustDomain         = "NoTrustDomain"
	ReasonNoIdentityConfig      = "NoIdentityConfig"
)

var (
	agentCardLogger = ctrl.Log.WithName("controller").WithName("AgentCard")
)

// AgentCardReconciler reconciles an AgentCard object
type AgentCardReconciler struct {
	client.Client
	Scheme       *runtime.Scheme
	AgentFetcher agentcard.Fetcher
	Recorder     record.EventRecorder
	TrustDomain  string
}

// +kubebuilder:rbac:groups=agent.kagenti.dev,resources=agentcards,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=agent.kagenti.dev,resources=agentcards/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=agent.kagenti.dev,resources=agentcards/finalizers,verbs=update
// +kubebuilder:rbac:groups=agent.kagenti.dev,resources=agents,verbs=get;list;watch
// +kubebuilder:rbac:groups=core,resources=services,verbs=get;list;watch

func (r *AgentCardReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	agentCardLogger.Info("Reconciling AgentCard", "namespacedName", req.NamespacedName)

	agentCard := &agentv1alpha1.AgentCard{}
	err := r.Get(ctx, req.NamespacedName, agentCard)
	if err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Handle deletion
	if !agentCard.ObjectMeta.DeletionTimestamp.IsZero() {
		return r.handleDeletion(ctx, agentCard)
	}

	// Add finalizer
	if !controllerutil.ContainsFinalizer(agentCard, AgentCardFinalizer) {
		controllerutil.AddFinalizer(agentCard, AgentCardFinalizer)
		if err := r.Update(ctx, agentCard); err != nil {
			agentCardLogger.Error(err, "Unable to add finalizer to AgentCard")
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	// Find the matching Agent
	agent, err := r.findMatchingAgent(ctx, agentCard)
	if err != nil {
		agentCardLogger.Error(err, "Failed to find matching Agent", "agentCard", agentCard.Name)

		// Determine the appropriate reason based on error type
		var reason, message, conditionReason string
		if errors.Is(err, ErrMultipleAgentsMatched) {
			reason = ReasonMultipleAgentsMatched
			message = fmt.Sprintf("Cannot evaluate binding: %s", err.Error())
			conditionReason = "MultipleAgentsMatched"
		} else {
			reason = ReasonAgentNotFound
			message = "No matching Agent found"
			conditionReason = "AgentNotFound"
		}

		r.updateCondition(ctx, agentCard, "Synced", metav1.ConditionFalse, conditionReason, err.Error())

		// If identity binding is configured, update binding status
		if agentCard.Spec.IdentityBinding != nil {
			r.updateBindingStatus(ctx, agentCard, false, reason, message, "")
			// Emit event for visibility
			if r.Recorder != nil {
				r.Recorder.Event(agentCard, corev1.EventTypeWarning, reason, message)
			}
		}
		return ctrl.Result{RequeueAfter: 1 * time.Minute}, nil
	}

	// Evaluate identity binding BEFORE fetching the card (uses only K8s metadata)
	if agentCard.Spec.IdentityBinding != nil {
		if err := r.evaluateBinding(ctx, agentCard, agent); err != nil {
			agentCardLogger.Error(err, "Failed to evaluate binding", "agentCard", agentCard.Name)
		}
	}

	// Check if Agent is ready before attempting to fetch
	if !r.isAgentReady(agent) {
		agentCardLogger.Info("Agent not ready yet, skipping sync", "agent", agent.Name)
		r.updateCondition(ctx, agentCard, "Synced", metav1.ConditionFalse, "AgentNotReady", "Waiting for Agent to become ready")
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}

	// Determine the protocol from Agent labels
	protocol := r.getAgentProtocol(agent)
	if protocol == "" {
		agentCardLogger.Info("No agent protocol label found, skipping sync", "agent", agent.Name)
		r.updateCondition(ctx, agentCard, "Synced", metav1.ConditionFalse, "NoProtocol", "Agent does not have a protocol label")
		return ctrl.Result{}, nil
	}

	// Get the service to determine the endpoint
	service, err := r.getAgentService(ctx, agent)
	if err != nil {
		agentCardLogger.Error(err, "Failed to get Agent service", "agent", agent.Name)
		r.updateCondition(ctx, agentCard, "Synced", metav1.ConditionFalse, "ServiceNotFound", err.Error())
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	// Get service port
	servicePort := r.getServicePort(service)
	serviceURL := agentcard.GetServiceURL(agent.Name, agent.Namespace, servicePort)

	// Fetch the agent card data
	cardData, err := r.AgentFetcher.Fetch(ctx, protocol, serviceURL)
	if err != nil {
		agentCardLogger.Error(err, "Failed to fetch agent card", "agent", agent.Name, "url", serviceURL)
		r.updateCondition(ctx, agentCard, "Synced", metav1.ConditionFalse, "FetchFailed", err.Error())
		return ctrl.Result{RequeueAfter: 1 * time.Minute}, nil
	}

	// Override the URL with the actual in-cluster service URL
	// (agents may advertise 0.0.0.0:8000 which isn't useful for cluster communication)
	cardData.URL = serviceURL

	// Compute card_id for drift detection (optional)
	cardId := r.computeCardId(cardData)
	if cardId != "" && agentCard.Status.CardId != "" && agentCard.Status.CardId != cardId {
		// Card content has changed - emit event
		if r.Recorder != nil {
			r.Recorder.Event(agentCard, corev1.EventTypeWarning, "CardContentChanged",
				fmt.Sprintf("Agent card content changed: previous=%s, current=%s", agentCard.Status.CardId, cardId))
		}
		agentCardLogger.Info("Card content changed", "agentCard", agentCard.Name, "previousCardId", agentCard.Status.CardId, "newCardId", cardId)
	}

	// Update the AgentCard status with the fetched card
	if err := r.updateAgentCardStatus(ctx, agentCard, cardData, protocol, cardId); err != nil {
		agentCardLogger.Error(err, "Failed to update AgentCard status")
		return ctrl.Result{}, err
	}

	// Calculate next sync time based on syncPeriod
	syncPeriod := r.getSyncPeriod(agentCard)
	agentCardLogger.Info("Successfully synced agent card", "agent", agent.Name, "nextSync", syncPeriod)

	return ctrl.Result{RequeueAfter: syncPeriod}, nil
}

// ErrMultipleAgentsMatched is returned when multiple agents match the selector
var ErrMultipleAgentsMatched = fmt.Errorf("multiple agents match selector")

// findMatchingAgent finds the Agent that matches the AgentCard selector.
// Returns an error if zero or multiple agents match - binding requires a unique selector.
func (r *AgentCardReconciler) findMatchingAgent(ctx context.Context, agentCard *agentv1alpha1.AgentCard) (*agentv1alpha1.Agent, error) {
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

	if len(agentList.Items) > 1 {
		agentNames := make([]string, len(agentList.Items))
		for i, agent := range agentList.Items {
			agentNames[i] = agent.Name
		}
		agentCardLogger.Error(ErrMultipleAgentsMatched, "Ambiguous selector - binding requires unique selector",
			"count", len(agentList.Items),
			"agentCard", agentCard.Name,
			"matchingAgents", agentNames)
		return nil, fmt.Errorf("%w: found %d agents (%v) - use more specific labels in selector",
			ErrMultipleAgentsMatched, len(agentList.Items), agentNames)
	}

	return &agentList.Items[0], nil
}

// isAgentReady checks if the Agent is ready
func (r *AgentCardReconciler) isAgentReady(agent *agentv1alpha1.Agent) bool {
	if agent.Status.DeploymentStatus == nil {
		return false
	}
	return agent.Status.DeploymentStatus.Phase == agentv1alpha1.PhaseReady
}

// getAgentProtocol extracts the protocol from Agent labels
func (r *AgentCardReconciler) getAgentProtocol(agent *agentv1alpha1.Agent) string {
	if agent.Labels == nil {
		return ""
	}
	return agent.Labels[LabelAgentProtocol]
}

// getAgentService retrieves the Service for an Agent
func (r *AgentCardReconciler) getAgentService(ctx context.Context, agent *agentv1alpha1.Agent) (*corev1.Service, error) {
	// Service name follows the pattern: <agent-name>
	serviceName := agent.Name
	service := &corev1.Service{}

	err := r.Get(ctx, types.NamespacedName{
		Name:      serviceName,
		Namespace: agent.Namespace,
	}, service)

	if err != nil {
		return nil, fmt.Errorf("failed to get service %s: %w", serviceName, err)
	}

	return service, nil
}

// getServicePort extracts the service port (defaults to first port or 8000)
func (r *AgentCardReconciler) getServicePort(service *corev1.Service) int32 {
	if len(service.Spec.Ports) > 0 {
		return service.Spec.Ports[0].Port
	}
	return 8000 // default fallback
}

// getSyncPeriod parses the sync period from the spec or returns default
func (r *AgentCardReconciler) getSyncPeriod(agentCard *agentv1alpha1.AgentCard) time.Duration {
	if agentCard.Spec.SyncPeriod == "" {
		return DefaultSyncPeriod
	}

	duration, err := time.ParseDuration(agentCard.Spec.SyncPeriod)
	if err != nil {
		agentCardLogger.Error(err, "Invalid sync period, using default",
			"syncPeriod", agentCard.Spec.SyncPeriod)
		return DefaultSyncPeriod
	}

	return duration
}

// updateAgentCardStatus updates the AgentCard status with the fetched agent card
func (r *AgentCardReconciler) updateAgentCardStatus(ctx context.Context, agentCard *agentv1alpha1.AgentCard, cardData *agentv1alpha1.AgentCardData, protocol, cardId string) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		// Fetch the latest version
		latest := &agentv1alpha1.AgentCard{}
		if err := r.Get(ctx, types.NamespacedName{
			Name:      agentCard.Name,
			Namespace: agentCard.Namespace,
		}, latest); err != nil {
			return err
		}

		// Update status fields
		latest.Status.Card = cardData
		latest.Status.Protocol = protocol
		latest.Status.LastSyncTime = &metav1.Time{Time: time.Now()}
		if cardId != "" {
			latest.Status.CardId = cardId
		}

		// Update conditions
		meta.SetStatusCondition(&latest.Status.Conditions, metav1.Condition{
			Type:               "Synced",
			Status:             metav1.ConditionTrue,
			LastTransitionTime: metav1.Now(),
			Reason:             "SyncSucceeded",
			Message:            fmt.Sprintf("Successfully fetched agent card for %s", cardData.Name),
		})

		meta.SetStatusCondition(&latest.Status.Conditions, metav1.Condition{
			Type:               "Ready",
			Status:             metav1.ConditionTrue,
			LastTransitionTime: metav1.Now(),
			Reason:             "ReadyToServe",
			Message:            "Agent index is ready for queries",
		})

		return r.Status().Update(ctx, latest)
	})
}

// updateCondition updates a specific condition
func (r *AgentCardReconciler) updateCondition(ctx context.Context, agentCard *agentv1alpha1.AgentCard, conditionType string, status metav1.ConditionStatus, reason, message string) {
	_ = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		latest := &agentv1alpha1.AgentCard{}
		if err := r.Get(ctx, types.NamespacedName{
			Name:      agentCard.Name,
			Namespace: agentCard.Namespace,
		}, latest); err != nil {
			return err
		}

		meta.SetStatusCondition(&latest.Status.Conditions, metav1.Condition{
			Type:               conditionType,
			Status:             status,
			LastTransitionTime: metav1.Now(),
			Reason:             reason,
			Message:            message,
		})

		return r.Status().Update(ctx, latest)
	})
}

// handleDeletion handles cleanup when an AgentCard is deleted
func (r *AgentCardReconciler) handleDeletion(ctx context.Context, agentCard *agentv1alpha1.AgentCard) (ctrl.Result, error) {
	if controllerutil.ContainsFinalizer(agentCard, AgentCardFinalizer) {
		agentCardLogger.Info("Cleaning up AgentCard", "name", agentCard.Name)

		// Perform any cleanup here if needed

		// Remove finalizer
		if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			latest := &agentv1alpha1.AgentCard{}
			if err := r.Get(ctx, types.NamespacedName{
				Name:      agentCard.Name,
				Namespace: agentCard.Namespace,
			}, latest); err != nil {
				return err
			}

			controllerutil.RemoveFinalizer(latest, AgentCardFinalizer)
			return r.Update(ctx, latest)
		}); err != nil {
			agentCardLogger.Error(err, "Failed to remove finalizer from AgentCard")
			return ctrl.Result{}, err
		}

		agentCardLogger.Info("Removed finalizer from AgentCard")
	}

	return ctrl.Result{}, nil
}

// mapAgentToAgentCard maps Agent events to AgentCard reconcile requests
func (r *AgentCardReconciler) mapAgentToAgentCard(ctx context.Context, obj client.Object) []reconcile.Request {
	agent, ok := obj.(*agentv1alpha1.Agent)
	if !ok {
		return nil
	}

	// Only process Agents with the agent type label
	if agent.Labels == nil || agent.Labels[LabelAgentType] != LabelValueAgent {
		return nil
	}

	// Find all AgentCardes that might reference this Agent
	agentCardList := &agentv1alpha1.AgentCardList{}
	if err := r.List(ctx, agentCardList, client.InNamespace(agent.Namespace)); err != nil {
		agentCardLogger.Error(err, "Failed to list AgentCardes for mapping")
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
func (r *AgentCardReconciler) selectorMatchesAgent(agentCard *agentv1alpha1.AgentCard, agent *agentv1alpha1.Agent) bool {
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

// evaluateBinding evaluates identity binding using Kubernetes metadata only
func (r *AgentCardReconciler) evaluateBinding(ctx context.Context, agentCard *agentv1alpha1.AgentCard, agent *agentv1alpha1.Agent) error {
	binding := agentCard.Spec.IdentityBinding
	if binding == nil {
		return nil
	}

	// Determine trust domain
	trustDomain := binding.TrustDomain
	if trustDomain == "" {
		trustDomain = r.TrustDomain
	}
	if trustDomain == "" {
		trustDomain = DefaultTrustDomain
	}

	// Derive expected SPIFFE ID from Kubernetes metadata
	// Convention: spiffe://<trust-domain>/ns/<namespace>/sa/<serviceAccount>
	serviceAccount := r.getAgentServiceAccount(agent)
	expectedSpiffeID := fmt.Sprintf("spiffe://%s/ns/%s/sa/%s", trustDomain, agent.Namespace, serviceAccount)

	// Check if expected SPIFFE ID is in the allowlist
	bound := false
	for _, allowedID := range binding.AllowedSpiffeIDs {
		if string(allowedID) == expectedSpiffeID {
			bound = true
			break
		}
	}

	// Determine reason and message
	var reason, message string
	if bound {
		reason = ReasonBound
		message = fmt.Sprintf("Expected SPIFFE ID %s is in the allowlist", expectedSpiffeID)
	} else {
		reason = ReasonNotBound
		message = fmt.Sprintf("Expected SPIFFE ID %s is not in the allowlist", expectedSpiffeID)
	}

	// Update binding status
	if err := r.updateBindingStatus(ctx, agentCard, bound, reason, message, expectedSpiffeID); err != nil {
		return err
	}

	// Emit events
	if r.Recorder != nil {
		if bound {
			r.Recorder.Event(agentCard, corev1.EventTypeNormal, "BindingEvaluated", message)
		} else {
			r.Recorder.Event(agentCard, corev1.EventTypeWarning, "BindingFailed", message)
		}
	}

	return nil
}

// getAgentServiceAccount returns the service account name for an Agent
func (r *AgentCardReconciler) getAgentServiceAccount(agent *agentv1alpha1.Agent) string {
	if agent.Spec.PodTemplateSpec != nil && agent.Spec.PodTemplateSpec.Spec.ServiceAccountName != "" {
		return agent.Spec.PodTemplateSpec.Spec.ServiceAccountName
	}
	// Default service account follows the pattern: <agent-name>-sa
	return agent.Name + "-sa"
}

// updateBindingStatus updates the binding status in the AgentCard
func (r *AgentCardReconciler) updateBindingStatus(ctx context.Context, agentCard *agentv1alpha1.AgentCard, bound bool, reason, message, expectedSpiffeID string) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		latest := &agentv1alpha1.AgentCard{}
		if err := r.Get(ctx, types.NamespacedName{
			Name:      agentCard.Name,
			Namespace: agentCard.Namespace,
		}, latest); err != nil {
			return err
		}

		now := metav1.Now()
		latest.Status.BindingStatus = &agentv1alpha1.BindingStatus{
			Bound:              bound,
			Reason:             reason,
			Message:            message,
			LastEvaluationTime: &now,
		}
		if expectedSpiffeID != "" {
			latest.Status.ExpectedSpiffeID = expectedSpiffeID
		}

		// Update the Bound condition
		conditionStatus := metav1.ConditionFalse
		if bound {
			conditionStatus = metav1.ConditionTrue
		}
		meta.SetStatusCondition(&latest.Status.Conditions, metav1.Condition{
			Type:               "Bound",
			Status:             conditionStatus,
			LastTransitionTime: now,
			Reason:             reason,
			Message:            message,
		})

		return r.Status().Update(ctx, latest)
	})
}

// computeCardId computes a SHA256 hash of the card data for drift detection
func (r *AgentCardReconciler) computeCardId(cardData *agentv1alpha1.AgentCardData) string {
	if cardData == nil {
		return ""
	}
	// Use JSON serialization for simplicity (JCS would be ideal but adds complexity)
	data, err := json.Marshal(cardData)
	if err != nil {
		agentCardLogger.Error(err, "Failed to marshal card data for hash computation")
		return ""
	}
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}

// SetupWithManager sets up the controller with the Manager.
func (r *AgentCardReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// Initialize the fetcher if not set
	if r.AgentFetcher == nil {
		r.AgentFetcher = agentcard.NewFetcher()
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&agentv1alpha1.AgentCard{}).
		Watches(
			&agentv1alpha1.Agent{},
			handler.EnqueueRequestsFromMapFunc(r.mapAgentToAgentCard),
		).
		Named("AgentCard").
		Complete(r)
}
