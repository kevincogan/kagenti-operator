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
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	agentv1alpha1 "github.com/kagenti/operator/api/v1alpha1"
	"github.com/kagenti/operator/internal/signature"
)

type mockSignatureProviderWithBundleHash struct {
	pubKeyPEM  []byte
	spiffeID   string
	bundleHash string
	leafExpiry time.Time
}

func (m *mockSignatureProviderWithBundleHash) Name() string       { return "mock" }
func (m *mockSignatureProviderWithBundleHash) BundleHash() string { return m.bundleHash }

func (m *mockSignatureProviderWithBundleHash) VerifySignature(_ context.Context, cardData *agentv1alpha1.AgentCardData,
	signatures []agentv1alpha1.AgentCardSignature) (*signature.VerificationResult, error) {
	for i := range signatures {
		result, err := signature.VerifyJWS(cardData, &signatures[i], m.pubKeyPEM)
		if err == nil && result != nil && result.Verified {
			result.SpiffeID = m.spiffeID
			result.LeafNotAfter = m.leafExpiry
			return result, nil
		}
	}
	return &signature.VerificationResult{Verified: false, Details: "no valid signature"}, nil
}

func setBundleHashAnnotation(ctx context.Context, name, ns, hash string) {
	Eventually(func() error {
		d := &appsv1.Deployment{}
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: ns}, d); err != nil {
			return err
		}
		if d.Annotations == nil {
			d.Annotations = make(map[string]string)
		}
		d.Annotations[AnnotationBundleHash] = hash
		return k8sClient.Update(ctx, d)
	}).Should(Succeed())
}

func getResignTrigger(ctx context.Context, name, ns string) string {
	d := &appsv1.Deployment{}
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: ns}, d); err != nil {
		return ""
	}
	if d.Spec.Template.Annotations == nil {
		return ""
	}
	return d.Spec.Template.Annotations[AnnotationResignTrigger]
}

// reconcileTwice runs two reconcile cycles: the first adds the finalizer,
// the second performs the actual card fetch, verification, and status update.
func reconcileTwice(reconciler *AgentCardReconciler, name, ns string) {
	nn := types.NamespacedName{Name: name, Namespace: ns}
	_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
	ExpectWithOffset(1, err).NotTo(HaveOccurred())
	_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
	ExpectWithOffset(1, err).NotTo(HaveOccurred())
}

var _ = Describe("Proactive Restart for Re-signing", func() {
	const (
		timeout  = time.Second * 10
		interval = time.Millisecond * 250
	)

	Context("Trust bundle rotation triggers workload restart", func() {
		const (
			deploymentName = "restart-bundle-agent"
			agentCardName  = "restart-bundle-card"
			namespace      = "default"
		)

		AfterEach(func() {
			cleanupResource(ctx, &agentv1alpha1.AgentCard{}, agentCardName, namespace)
			cleanupResource(ctx, &appsv1.Deployment{}, deploymentName, namespace)
			cleanupResource(ctx, &corev1.Service{}, deploymentName, namespace)
		})

		It("should patch pod template annotation when trust bundle hash changes", func() {
			privKey, pubPEM := generateTestRSAKeyPair()
			createDeploymentWithService(ctx, deploymentName, namespace)
			setBundleHashAnnotation(ctx, deploymentName, namespace, "old-bundle-hash")

			makeCardData := func() *agentv1alpha1.AgentCardData {
				cd := &agentv1alpha1.AgentCardData{
					Name: "Bundle Restart Agent", Version: "1.0.0", URL: "http://localhost:8000",
				}
				jwsSig := buildTestJWS(cd, privKey, "key-1", "")
				cd.Signatures = []agentv1alpha1.AgentCardSignature{jwsSig}
				return cd
			}

			agentCard := &agentv1alpha1.AgentCard{
				ObjectMeta: metav1.ObjectMeta{Name: agentCardName, Namespace: namespace},
				Spec: agentv1alpha1.AgentCardSpec{
					SyncPeriod: "30s",
					TargetRef:  &agentv1alpha1.TargetRef{APIVersion: "apps/v1", Kind: "Deployment", Name: deploymentName},
				},
			}
			Expect(k8sClient.Create(ctx, agentCard)).To(Succeed())

			reconciler := &AgentCardReconciler{
				Client:           k8sClient,
				Scheme:           k8sClient.Scheme(),
				AgentFetcher:     &mockFetcherFunc{fn: makeCardData},
				RequireSignature: true,
				SignatureProvider: &mockSignatureProviderWithBundleHash{
					pubKeyPEM: pubPEM, bundleHash: "new-bundle-hash", leafExpiry: time.Now().Add(24 * time.Hour),
				},
			}

			reconcileTwice(reconciler, agentCardName, namespace)

			Eventually(func() string { return getResignTrigger(ctx, deploymentName, namespace) }, timeout, interval).ShouldNot(BeEmpty())

			d := &appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: deploymentName, Namespace: namespace}, d)).To(Succeed())
			Expect(d.Annotations[AnnotationBundleHash]).To(Equal("new-bundle-hash"))
		})
	})

	Context("SVID expiry triggers workload restart", func() {
		const (
			deploymentName = "restart-svid-agent"
			agentCardName  = "restart-svid-card"
			namespace      = "default"
		)

		AfterEach(func() {
			cleanupResource(ctx, &agentv1alpha1.AgentCard{}, agentCardName, namespace)
			cleanupResource(ctx, &appsv1.Deployment{}, deploymentName, namespace)
			cleanupResource(ctx, &corev1.Service{}, deploymentName, namespace)
		})

		It("should trigger restart when SVID leaf cert is within grace period", func() {
			privKey, pubPEM := generateTestRSAKeyPair()
			createDeploymentWithService(ctx, deploymentName, namespace)
			setBundleHashAnnotation(ctx, deploymentName, namespace, "same-hash")

			makeCardData := func() *agentv1alpha1.AgentCardData {
				cd := &agentv1alpha1.AgentCardData{
					Name: "SVID Expiry Agent", Version: "1.0.0", URL: "http://localhost:8000",
				}
				jwsSig := buildTestJWS(cd, privKey, "key-1", "")
				cd.Signatures = []agentv1alpha1.AgentCardSignature{jwsSig}
				return cd
			}

			agentCard := &agentv1alpha1.AgentCard{
				ObjectMeta: metav1.ObjectMeta{Name: agentCardName, Namespace: namespace},
				Spec: agentv1alpha1.AgentCardSpec{
					SyncPeriod: "30s",
					TargetRef:  &agentv1alpha1.TargetRef{APIVersion: "apps/v1", Kind: "Deployment", Name: deploymentName},
				},
			}
			Expect(k8sClient.Create(ctx, agentCard)).To(Succeed())

			reconciler := &AgentCardReconciler{
				Client:           k8sClient,
				Scheme:           k8sClient.Scheme(),
				AgentFetcher:     &mockFetcherFunc{fn: makeCardData},
				RequireSignature: true,
				SignatureProvider: &mockSignatureProviderWithBundleHash{
					pubKeyPEM: pubPEM, bundleHash: "same-hash", leafExpiry: time.Now().Add(DefaultSVIDExpiryGracePeriod / 6),
				},
			}

			reconcileTwice(reconciler, agentCardName, namespace)

			Eventually(func() string { return getResignTrigger(ctx, deploymentName, namespace) }, timeout, interval).ShouldNot(BeEmpty())
		})
	})

	Context("No restart when cert is healthy and bundle unchanged", func() {
		const (
			deploymentName = "restart-noop-agent"
			agentCardName  = "restart-noop-card"
			namespace      = "default"
		)

		AfterEach(func() {
			cleanupResource(ctx, &agentv1alpha1.AgentCard{}, agentCardName, namespace)
			cleanupResource(ctx, &appsv1.Deployment{}, deploymentName, namespace)
			cleanupResource(ctx, &corev1.Service{}, deploymentName, namespace)
		})

		It("should not trigger restart", func() {
			privKey, pubPEM := generateTestRSAKeyPair()
			createDeploymentWithService(ctx, deploymentName, namespace)
			setBundleHashAnnotation(ctx, deploymentName, namespace, "stable-hash")

			makeCardData := func() *agentv1alpha1.AgentCardData {
				cd := &agentv1alpha1.AgentCardData{
					Name: "No Restart Agent", Version: "1.0.0", URL: "http://localhost:8000",
				}
				jwsSig := buildTestJWS(cd, privKey, "key-1", "")
				cd.Signatures = []agentv1alpha1.AgentCardSignature{jwsSig}
				return cd
			}

			agentCard := &agentv1alpha1.AgentCard{
				ObjectMeta: metav1.ObjectMeta{Name: agentCardName, Namespace: namespace},
				Spec: agentv1alpha1.AgentCardSpec{
					SyncPeriod: "30s",
					TargetRef:  &agentv1alpha1.TargetRef{APIVersion: "apps/v1", Kind: "Deployment", Name: deploymentName},
				},
			}
			Expect(k8sClient.Create(ctx, agentCard)).To(Succeed())

			reconciler := &AgentCardReconciler{
				Client:           k8sClient,
				Scheme:           k8sClient.Scheme(),
				AgentFetcher:     &mockFetcherFunc{fn: makeCardData},
				RequireSignature: true,
				SignatureProvider: &mockSignatureProviderWithBundleHash{
					pubKeyPEM: pubPEM, bundleHash: "stable-hash", leafExpiry: time.Now().Add(2 * time.Hour),
				},
			}

			reconcileTwice(reconciler, agentCardName, namespace)

			Expect(getResignTrigger(ctx, deploymentName, namespace)).To(BeEmpty())
		})
	})
})
