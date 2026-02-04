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

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	agentv1alpha1 "github.com/kagenti/operator/api/v1alpha1"
	"github.com/kagenti/operator/internal/agentcard"
)

const (
	// Label keys
	LabelAgentType       = "kagenti.io/type"
	LabelAgentProtocol   = "kagenti.io/agent-protocol" // Legacy label
	LabelKagentiProtocol = "kagenti.io/protocol"       // New label

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

	// ErrWorkloadNotFound indicates the referenced workload does not exist
	ErrWorkloadNotFound = errors.New("workload not found")

	// ErrNotAgentWorkload indicates the workload doesn't have required agent labels
	ErrNotAgentWorkload = errors.New("resource is not a Kagenti agent")
)

// WorkloadInfo contains information about a discovered agent workload
type WorkloadInfo struct {
	Name        string
	Namespace   string
	APIVersion  string
	Kind        string
	Labels      map[string]string
	Ready       bool
	ServiceName string
}

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
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch
// +kubebuilder:rbac:groups=apps,resources=statefulsets,verbs=get;list;watch

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

	// Get workload using targetRef (preferred) or selector (legacy)
	workload, err := r.getWorkload(ctx, agentCard)
	if err != nil {
		agentCardLogger.Error(err, "Failed to get workload", "agentCard", agentCard.Name)

		// Determine the appropriate reason based on error type
		var reason, message, conditionReason string
		if errors.Is(err, ErrMultipleAgentsMatched) {
			reason = ReasonMultipleAgentsMatched
			message = fmt.Sprintf("Cannot evaluate binding: %s", err.Error())
			conditionReason = "MultipleAgentsMatched"
		} else if errors.Is(err, ErrWorkloadNotFound) {
			reason = ReasonAgentNotFound
			message = "No matching workload found"
			conditionReason = "WorkloadNotFound"
		} else if errors.Is(err, ErrNotAgentWorkload) {
			reason = ReasonAgentNotFound
			message = "Referenced resource is not an agent"
			conditionReason = "NotAgentWorkload"
		} else {
			reason = ReasonAgentNotFound
			message = err.Error()
			conditionReason = "WorkloadError"
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
		if err := r.evaluateBinding(ctx, agentCard, workload); err != nil {
			agentCardLogger.Error(err, "Failed to evaluate binding", "agentCard", agentCard.Name)
		}
	}

	// Check if workload is ready
	if !workload.Ready {
		agentCardLogger.Info("Workload not ready yet, skipping sync", "workload", workload.Name, "kind", workload.Kind)
		r.updateCondition(ctx, agentCard, "Synced", metav1.ConditionFalse, "WorkloadNotReady",
			fmt.Sprintf("%s %s is not ready", workload.Kind, workload.Name))
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}

	// Get protocol from workload labels
	protocol := getWorkloadProtocol(workload.Labels)
	if protocol == "" {
		agentCardLogger.Info("No protocol label found, skipping sync", "workload", workload.Name)
		r.updateCondition(ctx, agentCard, "Synced", metav1.ConditionFalse, "NoProtocol",
			"Workload does not have kagenti.io/protocol label")
		return ctrl.Result{RequeueAfter: 1 * time.Minute}, nil
	}

	// Get the service to determine the endpoint
	service, err := r.getService(ctx, agentCard.Namespace, workload.ServiceName)
	if err != nil {
		agentCardLogger.Error(err, "Failed to get service", "service", workload.ServiceName)
		r.updateCondition(ctx, agentCard, "Synced", metav1.ConditionFalse, "ServiceNotFound", err.Error())
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	// Get service port
	servicePort := r.getServicePort(service)
	serviceURL := agentcard.GetServiceURL(workload.ServiceName, agentCard.Namespace, servicePort)

	// Fetch the agent card data
	cardData, err := r.AgentFetcher.Fetch(ctx, protocol, serviceURL)
	if err != nil {
		agentCardLogger.Error(err, "Failed to fetch agent card", "workload", workload.Name, "url", serviceURL)
		r.updateCondition(ctx, agentCard, "Synced", metav1.ConditionFalse, "FetchFailed", err.Error())
		return ctrl.Result{RequeueAfter: 1 * time.Minute}, nil
	}

	// Override the URL with the actual in-cluster Service URL.
	// Agents may advertise URLs like 0.0.0.0:8000, which are only valid from
	// within the agent Pod itself and are not usable for cluster communication.
	// Using the Service URL ensures other components can reliably reach the agent.
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

	// Build resolved targetRef for status
	resolvedTargetRef := &agentv1alpha1.TargetRef{
		APIVersion: workload.APIVersion,
		Kind:       workload.Kind,
		Name:       workload.Name,
	}

	// Update the AgentCard status with the fetched card
	if err := r.updateAgentCardStatus(ctx, agentCard, cardData, protocol, cardId, resolvedTargetRef); err != nil {
		agentCardLogger.Error(err, "Failed to update AgentCard status")
		return ctrl.Result{}, err
	}

	// Calculate next sync time based on syncPeriod
	syncPeriod := r.getSyncPeriod(agentCard)
	agentCardLogger.Info("Successfully synced agent card", "workload", workload.Name, "kind", workload.Kind, "nextSync", syncPeriod)

	return ctrl.Result{RequeueAfter: syncPeriod}, nil
}

// ErrMultipleAgentsMatched is returned when multiple agents match the selector
var ErrMultipleAgentsMatched = fmt.Errorf("multiple agents match selector")

// getWorkload fetches the workload using targetRef or falls back to selector
func (r *AgentCardReconciler) getWorkload(ctx context.Context, agentCard *agentv1alpha1.AgentCard) (*WorkloadInfo, error) {
	// Prefer targetRef if specified
	if agentCard.Spec.TargetRef != nil {
		return r.getWorkloadByTargetRef(ctx, agentCard.Namespace, agentCard.Spec.TargetRef)
	}

	// Fall back to deprecated selector
	if agentCard.Spec.Selector != nil {
		return r.findMatchingWorkloadBySelector(ctx, agentCard)
	}

	return nil, fmt.Errorf("neither targetRef nor selector specified")
}

// getWorkloadByTargetRef fetches the workload referenced by targetRef using duck typing
func (r *AgentCardReconciler) getWorkloadByTargetRef(ctx context.Context, namespace string, targetRef *agentv1alpha1.TargetRef) (*WorkloadInfo, error) {
	// Parse the GroupVersion from APIVersion
	gv, err := schema.ParseGroupVersion(targetRef.APIVersion)
	if err != nil {
		return nil, fmt.Errorf("invalid apiVersion %s: %w", targetRef.APIVersion, err)
	}
	gvk := gv.WithKind(targetRef.Kind)

	// Create an unstructured object to fetch any resource type
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(gvk)

	// Fetch the workload
	key := client.ObjectKey{Namespace: namespace, Name: targetRef.Name}
	if err := r.Get(ctx, key, obj); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("%w: %s/%s %s not found in namespace %s",
				ErrWorkloadNotFound, targetRef.APIVersion, targetRef.Kind, targetRef.Name, namespace)
		}
		return nil, err
	}

	labels := obj.GetLabels()

	// Validate it's a Kagenti agent
	if !isAgentWorkload(labels) {
		return nil, fmt.Errorf("%w: %s %s does not have kagenti.io/type=agent label",
			ErrNotAgentWorkload, targetRef.Kind, targetRef.Name)
	}

	// Determine readiness based on workload type
	ready := r.isWorkloadReady(obj, targetRef.Kind)

	return &WorkloadInfo{
		Name:        targetRef.Name,
		Namespace:   namespace,
		APIVersion:  targetRef.APIVersion,
		Kind:        targetRef.Kind,
		Labels:      labels,
		Ready:       ready,
		ServiceName: targetRef.Name, // Convention: service has same name as workload
	}, nil
}

// findMatchingWorkloadBySelector finds a workload matching the selector (legacy mode)
// It searches Deployments, StatefulSets, and Agent CRDs
// Returns ErrMultipleAgentsMatched if more than one unique workload matches
func (r *AgentCardReconciler) findMatchingWorkloadBySelector(ctx context.Context, agentCard *agentv1alpha1.AgentCard) (*WorkloadInfo, error) {
	if agentCard.Spec.Selector == nil {
		return nil, fmt.Errorf("no selector specified")
	}

	// Build selector that includes the agent label to ensure we only match agent workloads
	selectorLabels := make(map[string]string)
	for k, v := range agentCard.Spec.Selector.MatchLabels {
		selectorLabels[k] = v
	}
	selectorLabels[LabelAgentType] = LabelValueAgent

	labelSelector := client.MatchingLabels(selectorLabels)
	namespace := client.InNamespace(agentCard.Namespace)

	// Use a map to deduplicate by name (Agent CRD creates Deployment with same name)
	workloadsByName := make(map[string]*WorkloadInfo)
	var matchedNames []string

	// Search Deployments first (preferred over Agent CRD)
	deployments := &appsv1.DeploymentList{}
	if err := r.List(ctx, deployments, namespace, labelSelector); err != nil {
		return nil, fmt.Errorf("failed to list deployments: %w", err)
	}
	for i := range deployments.Items {
		d := &deployments.Items[i]
		if _, exists := workloadsByName[d.Name]; !exists {
			workloadsByName[d.Name] = &WorkloadInfo{
				Name:        d.Name,
				Namespace:   d.Namespace,
				APIVersion:  "apps/v1",
				Kind:        "Deployment",
				Labels:      d.Labels,
				Ready:       isDeploymentReady(d),
				ServiceName: d.Name,
			}
			matchedNames = append(matchedNames, d.Name)
		}
	}

	// Search StatefulSets
	statefulsets := &appsv1.StatefulSetList{}
	if err := r.List(ctx, statefulsets, namespace, labelSelector); err != nil {
		return nil, fmt.Errorf("failed to list statefulsets: %w", err)
	}
	for i := range statefulsets.Items {
		s := &statefulsets.Items[i]
		if _, exists := workloadsByName[s.Name]; !exists {
			workloadsByName[s.Name] = &WorkloadInfo{
				Name:        s.Name,
				Namespace:   s.Namespace,
				APIVersion:  "apps/v1",
				Kind:        "StatefulSet",
				Labels:      s.Labels,
				Ready:       isStatefulSetReady(s),
				ServiceName: s.Name,
			}
			matchedNames = append(matchedNames, s.Name)
		}
	}

	// Search Agent CRDs (fallback if no Deployment/StatefulSet with same name)
	agents := &agentv1alpha1.AgentList{}
	if err := r.List(ctx, agents, namespace, labelSelector); err != nil {
		return nil, fmt.Errorf("failed to list agents: %w", err)
	}
	for i := range agents.Items {
		a := &agents.Items[i]
		if _, exists := workloadsByName[a.Name]; !exists {
			workloadsByName[a.Name] = &WorkloadInfo{
				Name:        a.Name,
				Namespace:   a.Namespace,
				APIVersion:  agentv1alpha1.GroupVersion.String(),
				Kind:        "Agent",
				Labels:      a.Labels,
				Ready:       isAgentCRDReady(a),
				ServiceName: a.Name,
			}
			matchedNames = append(matchedNames, a.Name)
		}
	}

	// Check results
	if len(workloadsByName) == 0 {
		return nil, fmt.Errorf("%w: no matching workload found for selector", ErrWorkloadNotFound)
	}

	if len(workloadsByName) > 1 {
		agentCardLogger.Error(ErrMultipleAgentsMatched, "Ambiguous selector - identity binding requires unique selector",
			"count", len(workloadsByName),
			"agentCard", agentCard.Name,
			"matchingWorkloads", matchedNames)
		return nil, fmt.Errorf("%w: found %d workloads (%v) - use more specific labels in selector or use targetRef",
			ErrMultipleAgentsMatched, len(workloadsByName), matchedNames)
	}

	// Return the single match
	for _, workload := range workloadsByName {
		return workload, nil
	}

	return nil, fmt.Errorf("%w: no matching workload found for selector", ErrWorkloadNotFound)
}

// isWorkloadReady determines if a workload is ready to serve traffic using duck typing
func (r *AgentCardReconciler) isWorkloadReady(obj *unstructured.Unstructured, kind string) bool {
	switch kind {
	case "Deployment":
		return isDeploymentReadyFromUnstructured(obj)
	case "StatefulSet":
		return isStatefulSetReadyFromUnstructured(obj)
	case "Agent":
		return isAgentReadyFromUnstructured(obj)
	default:
		// For unknown types, check for common ready conditions
		return hasReadyCondition(obj)
	}
}

// isAgentWorkload checks if labels indicate this is a Kagenti agent
func isAgentWorkload(labels map[string]string) bool {
	return labels != nil && labels[LabelAgentType] == LabelValueAgent
}

// isDeploymentReady checks if a Deployment is ready
func isDeploymentReady(d *appsv1.Deployment) bool {
	for _, c := range d.Status.Conditions {
		if c.Type == appsv1.DeploymentAvailable && c.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

// isStatefulSetReady checks if a StatefulSet is ready
func isStatefulSetReady(s *appsv1.StatefulSet) bool {
	return s.Status.ReadyReplicas == s.Status.Replicas
}

// isAgentCRDReady checks if an Agent CRD is ready
func isAgentCRDReady(agent *agentv1alpha1.Agent) bool {
	if agent.Status.DeploymentStatus == nil {
		return false
	}
	return agent.Status.DeploymentStatus.Phase == agentv1alpha1.PhaseReady
}

// isDeploymentReadyFromUnstructured checks Deployment readiness from unstructured
func isDeploymentReadyFromUnstructured(obj *unstructured.Unstructured) bool {
	conditions, found, err := unstructured.NestedSlice(obj.Object, "status", "conditions")
	if err != nil || !found {
		return false
	}

	for _, c := range conditions {
		condition, ok := c.(map[string]interface{})
		if !ok {
			continue
		}
		if condition["type"] == "Available" && condition["status"] == "True" {
			return true
		}
	}
	return false
}

// isStatefulSetReadyFromUnstructured checks StatefulSet readiness from unstructured
func isStatefulSetReadyFromUnstructured(obj *unstructured.Unstructured) bool {
	readyReplicas, _, err := unstructured.NestedInt64(obj.Object, "status", "readyReplicas")
	if err != nil {
		return false
	}
	replicas, _, err := unstructured.NestedInt64(obj.Object, "status", "replicas")
	if err != nil {
		return false
	}
	return readyReplicas == replicas
}

// isAgentReadyFromUnstructured checks Agent CRD readiness from unstructured
func isAgentReadyFromUnstructured(obj *unstructured.Unstructured) bool {
	phase, found, err := unstructured.NestedString(obj.Object, "status", "deploymentStatus", "phase")
	if err != nil || !found {
		return false
	}
	return phase == "Ready"
}

// hasReadyCondition is a generic check for workloads with standard conditions
func hasReadyCondition(obj *unstructured.Unstructured) bool {
	conditions, found, err := unstructured.NestedSlice(obj.Object, "status", "conditions")
	if err != nil || !found {
		return false
	}

	for _, c := range conditions {
		condition, ok := c.(map[string]interface{})
		if !ok {
			continue
		}
		condType, _ := condition["type"].(string)
		status, _ := condition["status"].(string)
		if (condType == "Ready" || condType == "Available") && status == "True" {
			return true
		}
	}
	return false
}

// getWorkloadProtocol extracts the protocol from workload labels
// Supports both old (kagenti.io/agent-protocol) and new (kagenti.io/protocol) labels
func getWorkloadProtocol(labels map[string]string) string {
	if labels == nil {
		return ""
	}
	// Try new label first
	if protocol := labels[LabelKagentiProtocol]; protocol != "" {
		return protocol
	}
	// Fall back to old label
	return labels[LabelAgentProtocol]
}

// getService retrieves a Service by name
func (r *AgentCardReconciler) getService(ctx context.Context, namespace, name string) (*corev1.Service, error) {
	service := &corev1.Service{}
	err := r.Get(ctx, types.NamespacedName{
		Name:      name,
		Namespace: namespace,
	}, service)

	if err != nil {
		return nil, fmt.Errorf("failed to get service %s: %w", name, err)
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
func (r *AgentCardReconciler) updateAgentCardStatus(ctx context.Context, agentCard *agentv1alpha1.AgentCard, cardData *agentv1alpha1.AgentCardData, protocol, cardId string, targetRef *agentv1alpha1.TargetRef) error {
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
		latest.Status.TargetRef = targetRef
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

	// Find all AgentCards that might reference this Agent
	agentCardList := &agentv1alpha1.AgentCardList{}
	if err := r.List(ctx, agentCardList, client.InNamespace(agent.Namespace)); err != nil {
		agentCardLogger.Error(err, "Failed to list AgentCards for mapping")
		return nil
	}

	var requests []reconcile.Request
	for _, agentCard := range agentCardList.Items {
		// Check if this AgentCard references this Agent via targetRef
		if r.targetRefMatchesWorkload(&agentCard, agent, agentv1alpha1.GroupVersion.String(), "Agent") {
			requests = append(requests, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      agentCard.Name,
					Namespace: agentCard.Namespace,
				},
			})
			continue
		}
		// Fall back to selector matching
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

// mapWorkloadToAgentCard maps Deployment/StatefulSet events to AgentCard reconcile requests
func (r *AgentCardReconciler) mapWorkloadToAgentCard(apiVersion, kind string) handler.MapFunc {
	return func(ctx context.Context, obj client.Object) []reconcile.Request {
		labels := obj.GetLabels()

		// Only process workloads with the agent type label
		if !isAgentWorkload(labels) {
			return nil
		}

		// Find all AgentCards that might reference this workload
		agentCardList := &agentv1alpha1.AgentCardList{}
		if err := r.List(ctx, agentCardList, client.InNamespace(obj.GetNamespace())); err != nil {
			agentCardLogger.Error(err, "Failed to list AgentCards for mapping")
			return nil
		}

		var requests []reconcile.Request
		for _, agentCard := range agentCardList.Items {
			// Check if this AgentCard references this workload via targetRef
			if r.targetRefMatchesWorkload(&agentCard, obj, apiVersion, kind) {
				requests = append(requests, reconcile.Request{
					NamespacedName: types.NamespacedName{
						Name:      agentCard.Name,
						Namespace: agentCard.Namespace,
					},
				})
				continue
			}
			// Fall back to selector matching
			if r.selectorMatchesWorkload(&agentCard, labels) {
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
}

// selectorMatchesAgent checks if an AgentCard selector matches an Agent
func (r *AgentCardReconciler) selectorMatchesAgent(agentCard *agentv1alpha1.AgentCard, agent *agentv1alpha1.Agent) bool {
	if agentCard.Spec.Selector == nil || agent.Labels == nil {
		return false
	}

	for key, value := range agentCard.Spec.Selector.MatchLabels {
		if agent.Labels[key] != value {
			return false
		}
	}

	return true
}

// targetRefMatchesWorkload checks if an AgentCard targetRef matches a workload
func (r *AgentCardReconciler) targetRefMatchesWorkload(agentCard *agentv1alpha1.AgentCard, obj client.Object, apiVersion, kind string) bool {
	if agentCard.Spec.TargetRef == nil {
		return false
	}
	return agentCard.Spec.TargetRef.Name == obj.GetName() &&
		agentCard.Spec.TargetRef.Kind == kind &&
		agentCard.Spec.TargetRef.APIVersion == apiVersion
}

// selectorMatchesWorkload checks if an AgentCard selector matches a workload's labels
func (r *AgentCardReconciler) selectorMatchesWorkload(agentCard *agentv1alpha1.AgentCard, labels map[string]string) bool {
	if agentCard.Spec.Selector == nil || labels == nil {
		return false
	}

	for key, value := range agentCard.Spec.Selector.MatchLabels {
		if labels[key] != value {
			return false
		}
	}

	return true
}

// agentLabelPredicate filters for resources with kagenti.io/type=agent
func agentLabelPredicate() predicate.Predicate {
	return predicate.NewPredicateFuncs(func(obj client.Object) bool {
		labels := obj.GetLabels()
		return labels != nil && labels[LabelAgentType] == LabelValueAgent
	})
}

// evaluateBinding evaluates identity binding using Kubernetes metadata only
// Works with WorkloadInfo to support any workload type (Deployment, StatefulSet, Agent CRD)
func (r *AgentCardReconciler) evaluateBinding(ctx context.Context, agentCard *agentv1alpha1.AgentCard, workload *WorkloadInfo) error {
	binding := agentCard.Spec.IdentityBinding
	if binding == nil {
		return nil
	}

	// Determine expected SPIFFE ID
	var expectedSpiffeID string

	// Use explicit expectedSpiffeID if provided (supports custom SPIRE configurations)
	if binding.ExpectedSpiffeID != "" {
		expectedSpiffeID = string(binding.ExpectedSpiffeID)
		agentCardLogger.V(1).Info("Using explicit expectedSpiffeID from spec", "expectedSpiffeID", expectedSpiffeID)
	} else {
		// Derive from Kubernetes metadata (standard SPIRE Helm operator pattern)
		// See: https://github.com/spiffe/spire/blob/main/doc/plugin_agent_workloadattestor_k8s.md

		// Determine trust domain
		trustDomain := binding.TrustDomain
		if trustDomain == "" {
			trustDomain = r.TrustDomain
		}
		if trustDomain == "" {
			trustDomain = DefaultTrustDomain
		}

		// Get service account from the workload
		serviceAccount, err := r.getWorkloadServiceAccount(ctx, workload)
		if err != nil {
			agentCardLogger.Error(err, "Failed to get service account for workload", "workload", workload.Name)
			serviceAccount = workload.Name + "-sa" // fallback
		}

		// Convention: spiffe://<trust-domain>/ns/<namespace>/sa/<serviceAccount>
		expectedSpiffeID = fmt.Sprintf("spiffe://%s/ns/%s/sa/%s", trustDomain, workload.Namespace, serviceAccount)
	}

	// Check if expected SPIFFE ID is in the allowlist
	bound := false
	for _, allowedID := range binding.AllowedSpiffeIDs {
		if string(allowedID) == expectedSpiffeID {
			bound = true
			break
		}
	}

	// Warn if expectedSpiffeID doesn't match any allowedSpiffeID (likely config error)
	if !bound && r.Recorder != nil {
		// Log at higher verbosity to help users debug misconfigurations
		agentCardLogger.Info("SPIFFE ID mismatch - verify your SPIRE configuration matches allowedSpiffeIDs",
			"expectedSpiffeID", expectedSpiffeID,
			"allowedSpiffeIDs", binding.AllowedSpiffeIDs,
			"hint", "If using custom SPIRE identity patterns, set spec.identityBinding.expectedSpiffeID explicitly")
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

// getWorkloadServiceAccount returns the service account name for a workload
func (r *AgentCardReconciler) getWorkloadServiceAccount(ctx context.Context, workload *WorkloadInfo) (string, error) {
	switch workload.Kind {
	case "Agent":
		// For Agent CRD, get the Agent and check its PodTemplateSpec
		agent := &agentv1alpha1.Agent{}
		if err := r.Get(ctx, types.NamespacedName{Name: workload.Name, Namespace: workload.Namespace}, agent); err != nil {
			return "", err
		}
		if agent.Spec.PodTemplateSpec != nil && agent.Spec.PodTemplateSpec.Spec.ServiceAccountName != "" {
			return agent.Spec.PodTemplateSpec.Spec.ServiceAccountName, nil
		}
		return agent.Name + "-sa", nil

	case "Deployment":
		deployment := &appsv1.Deployment{}
		if err := r.Get(ctx, types.NamespacedName{Name: workload.Name, Namespace: workload.Namespace}, deployment); err != nil {
			return "", err
		}
		if deployment.Spec.Template.Spec.ServiceAccountName != "" {
			return deployment.Spec.Template.Spec.ServiceAccountName, nil
		}
		return "default", nil

	case "StatefulSet":
		statefulset := &appsv1.StatefulSet{}
		if err := r.Get(ctx, types.NamespacedName{Name: workload.Name, Namespace: workload.Namespace}, statefulset); err != nil {
			return "", err
		}
		if statefulset.Spec.Template.Spec.ServiceAccountName != "" {
			return statefulset.Spec.Template.Spec.ServiceAccountName, nil
		}
		return "default", nil

	default:
		// For unknown types, try to get from unstructured
		return workload.Name + "-sa", nil
	}
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
		// Watch Deployments with agent labels
		Watches(
			&appsv1.Deployment{},
			handler.EnqueueRequestsFromMapFunc(r.mapWorkloadToAgentCard("apps/v1", "Deployment")),
			builder.WithPredicates(agentLabelPredicate()),
		).
		// Watch StatefulSets with agent labels
		Watches(
			&appsv1.StatefulSet{},
			handler.EnqueueRequestsFromMapFunc(r.mapWorkloadToAgentCard("apps/v1", "StatefulSet")),
			builder.WithPredicates(agentLabelPredicate()),
		).
		// Watch legacy Agent CRDs
		Watches(
			&agentv1alpha1.Agent{},
			handler.EnqueueRequestsFromMapFunc(r.mapAgentToAgentCard),
			builder.WithPredicates(agentLabelPredicate()),
		).
		Named("AgentCard").
		Complete(r)
}
