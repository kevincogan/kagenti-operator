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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	agentv1alpha1 "github.com/kagenti/operator/api/v1alpha1"
)

// mockFetcher implements agentcard.Fetcher for testing
type mockFetcher struct {
	cardData *agentv1alpha1.AgentCardData
	err      error
}

func (m *mockFetcher) Fetch(ctx context.Context, protocol, url string) (*agentv1alpha1.AgentCardData, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.cardData, nil
}

var _ = Describe("AgentCard Controller", func() {
	Context("When reconciling an AgentCard with a ready Agent", func() {
		const (
			agentName     = "test-card-agent"
			agentCardName = "test-agentcard"
			serviceName   = "test-card-agent"
			namespace     = "default"
		)

		ctx := context.Background()

		agentCardNamespacedName := types.NamespacedName{
			Name:      agentCardName,
			Namespace: namespace,
		}

		BeforeEach(func() {
			By("creating an Agent with proper labels")
			agent := &agentv1alpha1.Agent{
				ObjectMeta: metav1.ObjectMeta{
					Name:      agentName,
					Namespace: namespace,
					Labels: map[string]string{
						"app.kubernetes.io/name": agentName,
						LabelAgentType:           LabelValueAgent,
						LabelAgentProtocol:       "a2a",
					},
				},
				Spec: agentv1alpha1.AgentSpec{
					PodTemplateSpec: &corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
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
			Expect(k8sClient.Create(ctx, agent)).To(Succeed())

			// Update status separately since it's a subresource
			// Fetch the agent first to get the latest version
			Eventually(func() error {
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: agentName, Namespace: namespace}, agent); err != nil {
					return err
				}
				agent.Status.DeploymentStatus = &agentv1alpha1.DeploymentStatus{
					Phase: agentv1alpha1.PhaseReady,
				}
				return k8sClient.Status().Update(ctx, agent)
			}).Should(Succeed())

			By("creating a Service for the Agent")
			service := &corev1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Name:      serviceName,
					Namespace: namespace,
				},
				Spec: corev1.ServiceSpec{
					Ports: []corev1.ServicePort{
						{
							Name:     "http",
							Port:     8000,
							Protocol: corev1.ProtocolTCP,
						},
					},
					Selector: map[string]string{
						"app.kubernetes.io/name": agentName,
					},
				},
			}
			Expect(k8sClient.Create(ctx, service)).To(Succeed())

			By("creating an AgentCard")
			agentCard := &agentv1alpha1.AgentCard{
				ObjectMeta: metav1.ObjectMeta{
					Name:      agentCardName,
					Namespace: namespace,
				},
				Spec: agentv1alpha1.AgentCardSpec{
					SyncPeriod: "30s",
					Selector: agentv1alpha1.AgentSelector{
						MatchLabels: map[string]string{
							"app.kubernetes.io/name": agentName,
							LabelAgentType:           LabelValueAgent,
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, agentCard)).To(Succeed())
		})

		AfterEach(func() {
			By("cleaning up resources")
			agentCard := &agentv1alpha1.AgentCard{}
			if err := k8sClient.Get(ctx, agentCardNamespacedName, agentCard); err == nil {
				Expect(k8sClient.Delete(ctx, agentCard)).To(Succeed())
			}

			agent := &agentv1alpha1.Agent{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: agentName, Namespace: namespace}, agent); err == nil {
				Expect(k8sClient.Delete(ctx, agent)).To(Succeed())
			}

			service := &corev1.Service{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: serviceName, Namespace: namespace}, service); err == nil {
				Expect(k8sClient.Delete(ctx, service)).To(Succeed())
			}
		})

		It("should fetch agent card and override URL with service URL", func() {
			By("setting up a mock fetcher that returns agent card with 0.0.0.0 URL")
			mockCard := &agentv1alpha1.AgentCardData{
				Name:        "Test Agent",
				Description: "A test agent",
				Version:     "1.0.0",
				URL:         "http://0.0.0.0:8000", // Agent's advertised URL
				Skills: []agentv1alpha1.AgentSkill{
					{
						Name:        "test-skill",
						Description: "A test skill",
					},
				},
			}

			reconciler := &AgentCardReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
				AgentFetcher: &mockFetcher{
					cardData: mockCard,
					err:      nil,
				},
			}

			By("reconciling the AgentCard (first reconcile adds finalizer)")
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: agentCardNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			By("reconciling again to fetch the agent card")
			result, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: agentCardNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(BeNumerically(">", 0))

			By("verifying the AgentCard status was updated")
			agentCard := &agentv1alpha1.AgentCard{}
			Eventually(func() bool {
				err := k8sClient.Get(ctx, agentCardNamespacedName, agentCard)
				if err != nil {
					return false
				}
				return agentCard.Status.Card != nil
			}).Should(BeTrue())

			By("verifying the URL was overridden with the service URL")
			expectedURL := "http://test-card-agent.default.svc.cluster.local:8000"
			Expect(agentCard.Status.Card.URL).To(Equal(expectedURL))

			By("verifying other card data was preserved")
			Expect(agentCard.Status.Card.Name).To(Equal("Test Agent"))
			Expect(agentCard.Status.Card.Description).To(Equal("A test agent"))
			Expect(agentCard.Status.Card.Version).To(Equal("1.0.0"))
			Expect(agentCard.Status.Card.Skills).To(HaveLen(1))
			Expect(agentCard.Status.Card.Skills[0].Name).To(Equal("test-skill"))

			By("verifying the protocol was set")
			Expect(agentCard.Status.Protocol).To(Equal("a2a"))

			By("verifying the Synced condition is True")
			syncedCondition := findCondition(agentCard.Status.Conditions, "Synced")
			Expect(syncedCondition).NotTo(BeNil())
			Expect(syncedCondition.Status).To(Equal(metav1.ConditionTrue))
		})
	})
})

// Helper function to find a condition by type
func findCondition(conditions []metav1.Condition, conditionType string) *metav1.Condition {
	for i := range conditions {
		if conditions[i].Type == conditionType {
			return &conditions[i]
		}
	}
	return nil
}
