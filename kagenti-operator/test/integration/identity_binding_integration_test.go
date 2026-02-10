//go:build integration
// +build integration

/*
Copyright 2025.

Integration test for AgentCard Identity Binding (Step 1)

Run with: go test -v -tags=integration ./test/integration/... -timeout 5m
Prerequisites: kubectl configured with access to a Kubernetes cluster with kagenti CRDs installed
*/

package integration

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	agentv1alpha1 "github.com/kagenti/operator/api/v1alpha1"
	"github.com/kagenti/operator/internal/controller"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

var (
	testNamespace = fmt.Sprintf("ib-test-%d", time.Now().UnixNano()%100000)
)

const (
	trustDomain = "cluster.local"
	timeout     = 30 * time.Second
	interval    = 500 * time.Millisecond
)

var (
	k8sClient client.Client
	scheme    = runtime.NewScheme()
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(agentv1alpha1.AddToScheme(scheme))
}

func setupClient(t *testing.T) {
	// Use KUBECONFIG or default location
	kubeconfig := os.Getenv("KUBECONFIG")
	if kubeconfig == "" {
		kubeconfig = os.Getenv("HOME") + "/.kube/config"
	}

	config, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		t.Fatalf("Failed to build config: %v", err)
	}

	k8sClient, err = client.New(config, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatalf("Failed to create client: %v", err)
	}
}

func setupNamespace(t *testing.T, ctx context.Context) {
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: testNamespace,
		},
	}

	// Check if namespace exists
	existingNs := &corev1.Namespace{}
	err := k8sClient.Get(ctx, types.NamespacedName{Name: testNamespace}, existingNs)
	if err == nil {
		// Namespace exists, delete it
		_ = k8sClient.Delete(ctx, existingNs)

		// Wait for namespace to be fully deleted
		for i := 0; i < 30; i++ {
			time.Sleep(time.Second)
			err = k8sClient.Get(ctx, types.NamespacedName{Name: testNamespace}, existingNs)
			if errors.IsNotFound(err) {
				break
			}
		}
	}

	// Create fresh namespace
	err = k8sClient.Create(ctx, ns)
	if err != nil {
		t.Fatalf("Failed to create namespace: %v", err)
	}
	t.Logf("✓ Created test namespace: %s", testNamespace)
}

func cleanupNamespace(t *testing.T, ctx context.Context) {
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: testNamespace,
		},
	}
	_ = k8sClient.Delete(ctx, ns)
	t.Logf("✓ Cleaned up test namespace: %s", testNamespace)
}

// mockFetcher implements agentcard.Fetcher for testing
type mockFetcher struct{}

func (m *mockFetcher) Fetch(ctx context.Context, protocol, url string) (*agentv1alpha1.AgentCardData, error) {
	return &agentv1alpha1.AgentCardData{
		Name:        "Test Agent",
		Description: "Integration test agent",
		Version:     "1.0.0",
		URL:         url,
	}, nil
}

func TestIdentityBindingIntegration(t *testing.T) {
	ctx := context.Background()

	setupClient(t)
	setupNamespace(t, ctx)
	defer cleanupNamespace(t, ctx)

	t.Run("Test1_MatchingBindingEvaluation", testMatchingBindingEvaluation)
	t.Run("Test2_NonMatchingBindingEvaluation", testNonMatchingBindingEvaluation)
	t.Run("Test3_StrictBindingEnforcement", testStrictBindingEnforcement)
	t.Run("Test4_BindingRestoration", testBindingRestoration)
}

func testMatchingBindingEvaluation(t *testing.T) {
	ctx := context.Background()
	agentName := "test-match-agent"
	cardName := "test-match-card"
	saName := "test-sa"

	t.Log("\n========================================")
	t.Log("TEST 1: Matching Binding Evaluation")
	t.Log("========================================")

	// Create Agent
	agent := createTestAgent(t, ctx, agentName, saName)
	defer deleteResource(ctx, agent)

	// Create Service
	service := createTestService(t, ctx, agentName)
	defer deleteResource(ctx, service)

	// Create AgentCard with matching SPIFFE ID
	expectedSpiffeID := fmt.Sprintf("spiffe://%s/ns/%s/sa/%s", trustDomain, testNamespace, saName)
	agentCard := createTestAgentCard(t, ctx, cardName, agentName, []agentv1alpha1.SpiffeID{agentv1alpha1.SpiffeID(expectedSpiffeID)}, false)
	defer deleteResource(ctx, agentCard)

	// Create and run AgentCard reconciler
	reconciler := &controller.AgentCardReconciler{
		Client:       k8sClient,
		Scheme:       scheme,
		AgentFetcher: &mockFetcher{},
	}

	// First reconcile adds finalizer
	_, _ = reconciler.Reconcile(ctx, reconcile.Request{
		NamespacedName: types.NamespacedName{Name: cardName, Namespace: testNamespace},
	})
	time.Sleep(100 * time.Millisecond)

	// Simulate a verified signature with SPIFFE ID in JWS protected header
	simulateJWSSpiffeID(t, ctx, cardName, expectedSpiffeID)

	// Subsequent reconciles evaluate binding (with JWS SPIFFE ID now in status)
	for i := 0; i < 2; i++ {
		_, _ = reconciler.Reconcile(ctx, reconcile.Request{
			NamespacedName: types.NamespacedName{Name: cardName, Namespace: testNamespace},
		})
		time.Sleep(100 * time.Millisecond)
	}

	// Verify binding status
	card := &agentv1alpha1.AgentCard{}
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: cardName, Namespace: testNamespace}, card); err != nil {
		t.Fatalf("Failed to get AgentCard: %v", err)
	}

	if card.Status.BindingStatus == nil {
		t.Fatal("❌ BindingStatus is nil")
	}

	if !card.Status.BindingStatus.Bound {
		t.Fatalf("❌ Expected Bound=true, got Bound=%v, Reason=%s, Message=%s",
			card.Status.BindingStatus.Bound,
			card.Status.BindingStatus.Reason,
			card.Status.BindingStatus.Message)
	}

	t.Logf("✓ Binding Status: Bound=%v", card.Status.BindingStatus.Bound)
	t.Logf("✓ Reason: %s", card.Status.BindingStatus.Reason)
	t.Logf("✓ Expected SPIFFE ID: %s", card.Status.ExpectedSpiffeID)
	t.Log("✓ TEST 1 PASSED: Matching binding evaluated as Bound")
}

func testNonMatchingBindingEvaluation(t *testing.T) {
	ctx := context.Background()
	agentName := "test-nomatch-agent"
	cardName := "test-nomatch-card"
	saName := "test-sa"

	t.Log("\n========================================")
	t.Log("TEST 2: Non-Matching Binding Evaluation")
	t.Log("========================================")

	// Create Agent
	agent := createTestAgent(t, ctx, agentName, saName)
	defer deleteResource(ctx, agent)

	// Create Service
	service := createTestService(t, ctx, agentName)
	defer deleteResource(ctx, service)

	// Create AgentCard with NON-matching SPIFFE ID in allowlist
	wrongSpiffeID := fmt.Sprintf("spiffe://%s/ns/other/sa/other-sa", trustDomain)
	agentCard := createTestAgentCard(t, ctx, cardName, agentName, []agentv1alpha1.SpiffeID{agentv1alpha1.SpiffeID(wrongSpiffeID)}, false)
	defer deleteResource(ctx, agentCard)

	// Create and run AgentCard reconciler
	reconciler := &controller.AgentCardReconciler{
		Client:       k8sClient,
		Scheme:       scheme,
		AgentFetcher: &mockFetcher{},
	}

	// First reconcile adds finalizer
	reconciler.Reconcile(ctx, reconcile.Request{
		NamespacedName: types.NamespacedName{Name: cardName, Namespace: testNamespace},
	})
	time.Sleep(100 * time.Millisecond)

	// Simulate JWS SPIFFE ID that does NOT match the allowlist
	actualSpiffeID := fmt.Sprintf("spiffe://%s/ns/%s/sa/%s", trustDomain, testNamespace, saName)
	simulateJWSSpiffeID(t, ctx, cardName, actualSpiffeID)

	// Reconcile again to evaluate binding
	reconciler.Reconcile(ctx, reconcile.Request{
		NamespacedName: types.NamespacedName{Name: cardName, Namespace: testNamespace},
	})

	// Verify binding status
	card := &agentv1alpha1.AgentCard{}
	err := k8sClient.Get(ctx, types.NamespacedName{Name: cardName, Namespace: testNamespace}, card)
	if err != nil {
		t.Fatalf("Failed to get AgentCard: %v", err)
	}

	if card.Status.BindingStatus == nil {
		t.Fatal("❌ BindingStatus is nil")
	}

	if card.Status.BindingStatus.Bound {
		t.Fatalf("❌ Expected Bound=false, got Bound=%v", card.Status.BindingStatus.Bound)
	}

	t.Logf("✓ Binding Status: Bound=%v", card.Status.BindingStatus.Bound)
	t.Logf("✓ Reason: %s", card.Status.BindingStatus.Reason)
	t.Logf("✓ Expected SPIFFE ID: %s", card.Status.ExpectedSpiffeID)
	t.Logf("✓ Allowed SPIFFE IDs: %v", agentCard.Spec.IdentityBinding.AllowedSpiffeIDs)
	t.Log("✓ TEST 2 PASSED: Non-matching binding evaluated as NotBound")
}

func testStrictBindingEnforcement(t *testing.T) {
	ctx := context.Background()
	agentName := "test-strict-agent"
	cardName := "test-strict-card"
	saName := "test-sa"

	t.Log("\n========================================")
	t.Log("TEST 3: Strict Binding Enforcement")
	t.Log("========================================")

	// Create Agent
	agent := createTestAgent(t, ctx, agentName, saName)
	defer deleteResource(ctx, agent)

	// Create Deployment for the agent (normally created by Agent controller)
	deployment := createTestDeployment(t, ctx, agentName, 3)
	defer deleteResource(ctx, deployment)

	// Create AgentCard with STRICT binding and NON-matching SPIFFE ID
	wrongSpiffeID := fmt.Sprintf("spiffe://%s/ns/other/sa/other-sa", trustDomain)
	agentCard := createTestAgentCard(t, ctx, cardName, agentName, []agentv1alpha1.SpiffeID{agentv1alpha1.SpiffeID(wrongSpiffeID)}, true)
	defer deleteResource(ctx, agentCard)

	// First, evaluate binding with AgentCard controller
	cardReconciler := &controller.AgentCardReconciler{
		Client:       k8sClient,
		Scheme:       scheme,
		AgentFetcher: &mockFetcher{},
	}

	// Reconcile multiple times to ensure binding is evaluated
	for i := 0; i < 3; i++ {
		cardReconciler.Reconcile(ctx, reconcile.Request{
			NamespacedName: types.NamespacedName{Name: cardName, Namespace: testNamespace},
		})
		time.Sleep(100 * time.Millisecond)
	}

	// Verify AgentCard shows NotBound
	card := &agentv1alpha1.AgentCard{}
	k8sClient.Get(ctx, types.NamespacedName{Name: cardName, Namespace: testNamespace}, card)

	bound := false
	if card.Status.BindingStatus != nil {
		bound = card.Status.BindingStatus.Bound
	}
	strict := false
	if card.Spec.IdentityBinding != nil {
		strict = card.Spec.IdentityBinding.Strict
	}
	t.Logf("  AgentCard binding status: Bound=%v, Strict=%v", bound, strict)

	// Now enforce with Agent controller
	agentReconciler := &controller.AgentReconciler{
		Client:   k8sClient,
		Scheme:   scheme,
		Recorder: record.NewFakeRecorder(10),
	}

	// Reconcile Agent multiple times to handle finalizer and enforcement
	for i := 0; i < 3; i++ {
		agentReconciler.Reconcile(ctx, reconcile.Request{
			NamespacedName: types.NamespacedName{Name: agentName, Namespace: testNamespace},
		})
		time.Sleep(100 * time.Millisecond)
	}

	// Verify deployment scaled to 0
	dep := &appsv1.Deployment{}
	err := k8sClient.Get(ctx, types.NamespacedName{Name: agentName, Namespace: testNamespace}, dep)
	if err != nil {
		t.Fatalf("Failed to get Deployment: %v", err)
	}

	if dep.Spec.Replicas == nil || *dep.Spec.Replicas != 0 {
		replicas := int32(-1)
		if dep.Spec.Replicas != nil {
			replicas = *dep.Spec.Replicas
		}
		t.Fatalf("❌ Expected replicas=0, got replicas=%d", replicas)
	}

	t.Logf("✓ Deployment replicas: %d", *dep.Spec.Replicas)
	t.Logf("✓ Disabled-by annotation: %s", dep.Annotations[controller.AnnotationDisabledBy])

	// Verify Agent status
	ag := &agentv1alpha1.Agent{}
	err = k8sClient.Get(ctx, types.NamespacedName{Name: agentName, Namespace: testNamespace}, ag)
	if err != nil {
		t.Fatalf("Failed to get Agent: %v", err)
	}

	if ag.Status.BindingEnforcement == nil || !ag.Status.BindingEnforcement.DisabledByBinding {
		t.Fatal("❌ Agent status should show DisabledByBinding=true")
	}

	t.Logf("✓ Agent DisabledByBinding: %v", ag.Status.BindingEnforcement.DisabledByBinding)
	if ag.Status.BindingEnforcement.OriginalReplicas != nil {
		t.Logf("✓ Agent OriginalReplicas: %d", *ag.Status.BindingEnforcement.OriginalReplicas)
	}
	t.Log("✓ TEST 3 PASSED: Strict binding failure scales deployment to 0")
}

func testBindingRestoration(t *testing.T) {
	ctx := context.Background()
	agentName := "test-restore-agent"
	cardName := "test-restore-card"
	saName := "test-sa"

	t.Log("\n========================================")
	t.Log("TEST 4: Binding Restoration")
	t.Log("========================================")

	// Create Agent with binding enforcement status already set (simulating previous disable)
	agent := createTestAgent(t, ctx, agentName, saName)
	defer deleteResource(ctx, agent)

	// Update Agent status to show it was disabled
	agent.Status.BindingEnforcement = &agentv1alpha1.BindingEnforcementStatus{
		DisabledByBinding: true,
		OriginalReplicas:  ptr.To(int32(3)),
		DisabledReason:    "Previous binding failure",
	}
	k8sClient.Status().Update(ctx, agent)

	// Create Deployment scaled to 0 with binding annotations
	deployment := createTestDeployment(t, ctx, agentName, 0)
	deployment.Annotations = map[string]string{
		controller.AnnotationDisabledBy:     controller.DisabledByIdentityBinding,
		controller.AnnotationDisabledReason: "Previous binding failure",
	}
	k8sClient.Update(ctx, deployment)
	defer deleteResource(ctx, deployment)

	t.Log("  Initial state: Deployment scaled to 0, Agent marked as disabled")

	// Create AgentCard with MATCHING SPIFFE ID (binding should pass now)
	expectedSpiffeID := fmt.Sprintf("spiffe://%s/ns/%s/sa/%s", trustDomain, testNamespace, saName)
	agentCard := createTestAgentCard(t, ctx, cardName, agentName, []agentv1alpha1.SpiffeID{agentv1alpha1.SpiffeID(expectedSpiffeID)}, true)
	defer deleteResource(ctx, agentCard)

	// Evaluate binding with AgentCard controller (should be Bound now)
	cardReconciler := &controller.AgentCardReconciler{
		Client:       k8sClient,
		Scheme:       scheme,
		AgentFetcher: &mockFetcher{},
	}

	// First reconcile adds finalizer
	cardReconciler.Reconcile(ctx, reconcile.Request{
		NamespacedName: types.NamespacedName{Name: cardName, Namespace: testNamespace},
	})
	time.Sleep(100 * time.Millisecond)

	// Simulate a verified signature with matching SPIFFE ID in JWS header
	simulateJWSSpiffeID(t, ctx, cardName, expectedSpiffeID)

	// Subsequent reconciles evaluate binding
	for i := 0; i < 2; i++ {
		cardReconciler.Reconcile(ctx, reconcile.Request{
			NamespacedName: types.NamespacedName{Name: cardName, Namespace: testNamespace},
		})
		time.Sleep(100 * time.Millisecond)
	}

	// Verify binding is now Bound
	card := &agentv1alpha1.AgentCard{}
	k8sClient.Get(ctx, types.NamespacedName{Name: cardName, Namespace: testNamespace}, card)
	if card.Status.BindingStatus == nil || !card.Status.BindingStatus.Bound {
		t.Fatal("❌ AgentCard should be Bound after fixing SPIFFE ID")
	}
	t.Logf("  AgentCard now Bound: %v", card.Status.BindingStatus.Bound)

	// Restore with Agent controller
	agentReconciler := &controller.AgentReconciler{
		Client:   k8sClient,
		Scheme:   scheme,
		Recorder: record.NewFakeRecorder(10),
	}

	// Refresh agent from cluster and ensure binding enforcement status is set
	freshAgent := &agentv1alpha1.Agent{}
	k8sClient.Get(ctx, types.NamespacedName{Name: agentName, Namespace: testNamespace}, freshAgent)

	// Ensure the binding enforcement status persists (may have been cleared by prior reconcile)
	if freshAgent.Status.BindingEnforcement == nil || !freshAgent.Status.BindingEnforcement.DisabledByBinding {
		freshAgent.Status.BindingEnforcement = &agentv1alpha1.BindingEnforcementStatus{
			DisabledByBinding: true,
			OriginalReplicas:  ptr.To(int32(3)),
			DisabledReason:    "Previous binding failure",
		}
		k8sClient.Status().Update(ctx, freshAgent)
	}

	// Reconcile Agent - this should restore since all bindings now pass
	for i := 0; i < 3; i++ {
		agentReconciler.Reconcile(ctx, reconcile.Request{
			NamespacedName: types.NamespacedName{Name: agentName, Namespace: testNamespace},
		})
		time.Sleep(100 * time.Millisecond)
	}

	// Verify deployment restored
	dep := &appsv1.Deployment{}
	err := k8sClient.Get(ctx, types.NamespacedName{Name: agentName, Namespace: testNamespace}, dep)
	if err != nil {
		t.Fatalf("Failed to get Deployment: %v", err)
	}

	if dep.Spec.Replicas == nil || *dep.Spec.Replicas != 3 {
		replicas := int32(-1)
		if dep.Spec.Replicas != nil {
			replicas = *dep.Spec.Replicas
		}
		t.Fatalf("❌ Expected replicas=3 (restored), got replicas=%d", replicas)
	}

	t.Logf("✓ Deployment replicas restored: %d", *dep.Spec.Replicas)

	// Verify annotations removed
	if _, exists := dep.Annotations[controller.AnnotationDisabledBy]; exists {
		t.Fatal("❌ Disabled-by annotation should be removed")
	}
	t.Log("✓ Binding annotations removed")

	// Verify Agent status cleared
	ag := &agentv1alpha1.Agent{}
	k8sClient.Get(ctx, types.NamespacedName{Name: agentName, Namespace: testNamespace}, ag)

	if ag.Status.BindingEnforcement != nil {
		t.Fatal("❌ Agent BindingEnforcement status should be cleared")
	}

	t.Log("✓ Agent BindingEnforcement status cleared")
	t.Log("✓ TEST 4 PASSED: Binding restoration works correctly")
}

// Helper functions

func createTestAgent(t *testing.T, ctx context.Context, name, saName string) *agentv1alpha1.Agent {
	agent := &agentv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: testNamespace,
			Labels: map[string]string{
				"app.kubernetes.io/name":    name,
				"kagenti.io/type":           "agent",
				"kagenti.io/agent-protocol": "a2a",
			},
		},
		Spec: agentv1alpha1.AgentSpec{
			Replicas: ptr.To(int32(3)),
			PodTemplateSpec: &corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					ServiceAccountName: saName,
					Containers: []corev1.Container{
						{
							Name:  "agent",
							Image: "test-image:latest",
						},
					},
				},
			},
			ImageSource: agentv1alpha1.ImageSource{
				Image: ptr.To("test-image:latest"),
			},
		},
	}

	err := k8sClient.Create(ctx, agent)
	if err != nil {
		t.Fatalf("Failed to create Agent: %v", err)
	}

	// Fetch the agent to get the resource version
	freshAgent := &agentv1alpha1.Agent{}
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: testNamespace}, freshAgent); err != nil {
		t.Fatalf("Failed to get Agent for status update: %v", err)
	}

	// Set status to Ready
	freshAgent.Status.DeploymentStatus = &agentv1alpha1.DeploymentStatus{
		Phase: agentv1alpha1.PhaseReady,
	}
	if err := k8sClient.Status().Update(ctx, freshAgent); err != nil {
		t.Fatalf("Failed to update Agent status: %v", err)
	}

	t.Logf("  Created Agent: %s (SA: %s)", name, saName)
	return freshAgent
}

func createTestService(t *testing.T, ctx context.Context, name string) *corev1.Service {
	service := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: testNamespace,
		},
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{
				{Name: "http", Port: 8000, Protocol: corev1.ProtocolTCP},
			},
			Selector: map[string]string{"app.kubernetes.io/name": name},
		},
	}

	err := k8sClient.Create(ctx, service)
	if err != nil {
		t.Fatalf("Failed to create Service: %v", err)
	}

	t.Logf("  Created Service: %s", name)
	return service
}

func createTestAgentCard(t *testing.T, ctx context.Context, name, agentName string, allowedIDs []agentv1alpha1.SpiffeID, strict bool) *agentv1alpha1.AgentCard {
	agentCard := &agentv1alpha1.AgentCard{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: testNamespace,
		},
		Spec: agentv1alpha1.AgentCardSpec{
			SyncPeriod: "30s",
			Selector: agentv1alpha1.AgentSelector{
				MatchLabels: map[string]string{
					"app.kubernetes.io/name": agentName,
					"kagenti.io/type":        "agent",
				},
			},
			IdentityBinding: &agentv1alpha1.IdentityBinding{
				AllowedSpiffeIDs: allowedIDs,
				Strict:           strict,
			},
		},
	}

	err := k8sClient.Create(ctx, agentCard)
	if err != nil {
		t.Fatalf("Failed to create AgentCard: %v", err)
	}

	t.Logf("  Created AgentCard: %s (strict=%v)", name, strict)
	return agentCard
}

func createTestDeployment(t *testing.T, ctx context.Context, name string, replicas int32) *appsv1.Deployment {
	labels := map[string]string{"app.kubernetes.io/name": name}
	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: testNamespace,
			Labels:    labels,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr.To(replicas),
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{Name: "agent", Image: "test-image:latest"},
					},
				},
			},
		},
	}

	err := k8sClient.Create(ctx, deployment)
	if err != nil {
		t.Fatalf("Failed to create Deployment: %v", err)
	}

	t.Logf("  Created Deployment: %s (replicas=%d)", name, replicas)
	return deployment
}

// simulateJWSSpiffeID pre-sets the AgentCard status to simulate a verified signature
// with a SPIFFE ID in the JWS protected header. This is needed because the integration
// tests don't perform actual signing — the SPIFFE ID now comes exclusively from the
// JWS protected header (no fallback paths).
func simulateJWSSpiffeID(t *testing.T, ctx context.Context, cardName, spiffeID string) {
	card := &agentv1alpha1.AgentCard{}
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: cardName, Namespace: testNamespace}, card); err != nil {
		t.Fatalf("Failed to get AgentCard for SPIFFE ID simulation: %v", err)
	}
	validSig := true
	card.Status.ValidSignature = &validSig
	card.Status.SignatureSpiffeID = spiffeID
	if err := k8sClient.Status().Update(ctx, card); err != nil {
		t.Fatalf("Failed to simulate JWS SPIFFE ID: %v", err)
	}
	t.Logf("  Simulated JWS SPIFFE ID: %s", spiffeID)
}

func deleteResource(ctx context.Context, obj client.Object) {
	// Remove finalizers first
	obj.SetFinalizers(nil)
	_ = k8sClient.Update(ctx, obj)
	_ = k8sClient.Delete(ctx, obj)
}

func TestMain(m *testing.M) {
	// Don't set logger to avoid recursion issues
	os.Exit(m.Run())
}
