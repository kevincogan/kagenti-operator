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
	"errors"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
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
					Selector: &agentv1alpha1.AgentSelector{
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

var _ = Describe("AgentCard Controller - findMatchingWorkloadBySelector", func() {
	const namespace = "default"

	var (
		ctx        context.Context
		reconciler *AgentCardReconciler
	)

	BeforeEach(func() {
		ctx = context.Background()
		reconciler = &AgentCardReconciler{
			Client: k8sClient,
			Scheme: k8sClient.Scheme(),
		}
	})

	Context("When searching for Deployments", func() {
		const deploymentName = "test-deployment-agent"

		AfterEach(func() {
			// Clean up deployment
			deployment := &appsv1.Deployment{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: deploymentName, Namespace: namespace}, deployment); err == nil {
				Expect(k8sClient.Delete(ctx, deployment)).To(Succeed())
			}
		})

		It("should find a Deployment with matching selector and agent label", func() {
			By("creating a Deployment with agent labels")
			deployment := &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      deploymentName,
					Namespace: namespace,
					Labels: map[string]string{
						"app.kubernetes.io/name": deploymentName,
						LabelAgentType:           LabelValueAgent,
						LabelKagentiProtocol:     "a2a",
					},
				},
				Spec: appsv1.DeploymentSpec{
					Selector: &metav1.LabelSelector{
						MatchLabels: map[string]string{
							"app": deploymentName,
						},
					},
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Labels: map[string]string{
								"app": deploymentName,
							},
						},
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name:  "agent",
									Image: "test-image:latest",
								},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, deployment)).To(Succeed())

			By("creating an AgentCard with matching selector")
			agentCard := &agentv1alpha1.AgentCard{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-card-deployment",
					Namespace: namespace,
				},
				Spec: agentv1alpha1.AgentCardSpec{
					Selector: &agentv1alpha1.AgentSelector{
						MatchLabels: map[string]string{
							"app.kubernetes.io/name": deploymentName,
							LabelAgentType:           LabelValueAgent,
						},
					},
				},
			}

			By("calling findMatchingWorkloadBySelector")
			workload, err := reconciler.findMatchingWorkloadBySelector(ctx, agentCard)

			By("verifying the Deployment was found")
			Expect(err).NotTo(HaveOccurred())
			Expect(workload).NotTo(BeNil())
			Expect(workload.Name).To(Equal(deploymentName))
			Expect(workload.Kind).To(Equal("Deployment"))
			Expect(workload.APIVersion).To(Equal("apps/v1"))
		})

		It("should not match Deployments without agent label even if selector matches other labels", func() {
			By("creating a Deployment without agent label")
			deployment := &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      deploymentName,
					Namespace: namespace,
					Labels: map[string]string{
						"app.kubernetes.io/name": deploymentName,
						// Missing LabelAgentType
					},
				},
				Spec: appsv1.DeploymentSpec{
					Selector: &metav1.LabelSelector{
						MatchLabels: map[string]string{
							"app": deploymentName,
						},
					},
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Labels: map[string]string{
								"app": deploymentName,
							},
						},
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name:  "agent",
									Image: "test-image:latest",
								},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, deployment)).To(Succeed())

			By("creating an AgentCard with matching selector")
			agentCard := &agentv1alpha1.AgentCard{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-card-no-agent-label",
					Namespace: namespace,
				},
				Spec: agentv1alpha1.AgentCardSpec{
					Selector: &agentv1alpha1.AgentSelector{
						MatchLabels: map[string]string{
							"app.kubernetes.io/name": deploymentName,
						},
					},
				},
			}

			By("calling findMatchingWorkloadBySelector")
			workload, err := reconciler.findMatchingWorkloadBySelector(ctx, agentCard)

			By("verifying no workload was found")
			Expect(err).To(HaveOccurred())
			Expect(errors.Is(err, ErrWorkloadNotFound)).To(BeTrue())
			Expect(workload).To(BeNil())
		})
	})

	Context("When searching for StatefulSets", func() {
		const statefulSetName = "test-statefulset-agent"

		AfterEach(func() {
			// Clean up statefulset
			statefulSet := &appsv1.StatefulSet{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: statefulSetName, Namespace: namespace}, statefulSet); err == nil {
				Expect(k8sClient.Delete(ctx, statefulSet)).To(Succeed())
			}
		})

		It("should find a StatefulSet with matching selector and agent label", func() {
			By("creating a StatefulSet with agent labels")
			statefulSet := &appsv1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      statefulSetName,
					Namespace: namespace,
					Labels: map[string]string{
						"app.kubernetes.io/name": statefulSetName,
						LabelAgentType:           LabelValueAgent,
						LabelKagentiProtocol:     "a2a",
					},
				},
				Spec: appsv1.StatefulSetSpec{
					ServiceName: statefulSetName,
					Selector: &metav1.LabelSelector{
						MatchLabels: map[string]string{
							"app": statefulSetName,
						},
					},
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Labels: map[string]string{
								"app": statefulSetName,
							},
						},
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name:  "agent",
									Image: "test-image:latest",
								},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, statefulSet)).To(Succeed())

			By("creating an AgentCard with matching selector")
			agentCard := &agentv1alpha1.AgentCard{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-card-statefulset",
					Namespace: namespace,
				},
				Spec: agentv1alpha1.AgentCardSpec{
					Selector: &agentv1alpha1.AgentSelector{
						MatchLabels: map[string]string{
							"app.kubernetes.io/name": statefulSetName,
							LabelAgentType:           LabelValueAgent,
						},
					},
				},
			}

			By("calling findMatchingWorkloadBySelector")
			workload, err := reconciler.findMatchingWorkloadBySelector(ctx, agentCard)

			By("verifying the StatefulSet was found")
			Expect(err).NotTo(HaveOccurred())
			Expect(workload).NotTo(BeNil())
			Expect(workload.Name).To(Equal(statefulSetName))
			Expect(workload.Kind).To(Equal("StatefulSet"))
			Expect(workload.APIVersion).To(Equal("apps/v1"))
		})
	})

	Context("When verifying search order", func() {
		const (
			deploymentName  = "test-order-deployment"
			statefulSetName = "test-order-statefulset"
			agentCRDName    = "test-order-agent"
		)

		AfterEach(func() {
			// Clean up resources
			deployment := &appsv1.Deployment{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: deploymentName, Namespace: namespace}, deployment); err == nil {
				Expect(k8sClient.Delete(ctx, deployment)).To(Succeed())
			}
			statefulSet := &appsv1.StatefulSet{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: statefulSetName, Namespace: namespace}, statefulSet); err == nil {
				Expect(k8sClient.Delete(ctx, statefulSet)).To(Succeed())
			}
			agent := &agentv1alpha1.Agent{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: agentCRDName, Namespace: namespace}, agent); err == nil {
				Expect(k8sClient.Delete(ctx, agent)).To(Succeed())
			}
		})

		It("should return error when multiple workloads match (ambiguous selector)", func() {
			sharedLabel := "shared-app"

			By("creating a Deployment with agent labels")
			deployment := &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      deploymentName,
					Namespace: namespace,
					Labels: map[string]string{
						"app.kubernetes.io/name": sharedLabel,
						LabelAgentType:           LabelValueAgent,
					},
				},
				Spec: appsv1.DeploymentSpec{
					Selector: &metav1.LabelSelector{
						MatchLabels: map[string]string{
							"app": deploymentName,
						},
					},
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Labels: map[string]string{
								"app": deploymentName,
							},
						},
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name:  "agent",
									Image: "test-image:latest",
								},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, deployment)).To(Succeed())

			By("creating a StatefulSet with the same labels")
			statefulSet := &appsv1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      statefulSetName,
					Namespace: namespace,
					Labels: map[string]string{
						"app.kubernetes.io/name": sharedLabel,
						LabelAgentType:           LabelValueAgent,
					},
				},
				Spec: appsv1.StatefulSetSpec{
					ServiceName: statefulSetName,
					Selector: &metav1.LabelSelector{
						MatchLabels: map[string]string{
							"app": statefulSetName,
						},
					},
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Labels: map[string]string{
								"app": statefulSetName,
							},
						},
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name:  "agent",
									Image: "test-image:latest",
								},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, statefulSet)).To(Succeed())

			By("creating an AgentCard with matching selector")
			agentCard := &agentv1alpha1.AgentCard{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-card-order",
					Namespace: namespace,
				},
				Spec: agentv1alpha1.AgentCardSpec{
					Selector: &agentv1alpha1.AgentSelector{
						MatchLabels: map[string]string{
							"app.kubernetes.io/name": sharedLabel,
							LabelAgentType:           LabelValueAgent,
						},
					},
				},
			}

			By("calling findMatchingWorkloadBySelector")
			workload, err := reconciler.findMatchingWorkloadBySelector(ctx, agentCard)

			By("verifying error is returned for ambiguous selector")
			Expect(err).To(HaveOccurred())
			Expect(errors.Is(err, ErrMultipleAgentsMatched)).To(BeTrue())
			Expect(workload).To(BeNil())
		})

		It("should fall back to Agent CRD when no Deployment or StatefulSet matches", func() {
			By("creating an Agent CRD with agent labels")
			agent := &agentv1alpha1.Agent{
				ObjectMeta: metav1.ObjectMeta{
					Name:      agentCRDName,
					Namespace: namespace,
					Labels: map[string]string{
						"app.kubernetes.io/name": agentCRDName,
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

			By("creating an AgentCard with matching selector")
			agentCard := &agentv1alpha1.AgentCard{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-card-agent-crd",
					Namespace: namespace,
				},
				Spec: agentv1alpha1.AgentCardSpec{
					Selector: &agentv1alpha1.AgentSelector{
						MatchLabels: map[string]string{
							"app.kubernetes.io/name": agentCRDName,
							LabelAgentType:           LabelValueAgent,
						},
					},
				},
			}

			By("calling findMatchingWorkloadBySelector")
			workload, err := reconciler.findMatchingWorkloadBySelector(ctx, agentCard)

			By("verifying Agent CRD was found")
			Expect(err).NotTo(HaveOccurred())
			Expect(workload).NotTo(BeNil())
			Expect(workload.Kind).To(Equal("Agent"))
			Expect(workload.Name).To(Equal(agentCRDName))
		})
	})

	Context("When no matching workloads exist", func() {
		It("should return ErrWorkloadNotFound when selector matches nothing", func() {
			By("creating an AgentCard with selector that matches nothing")
			agentCard := &agentv1alpha1.AgentCard{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-card-no-match",
					Namespace: namespace,
				},
				Spec: agentv1alpha1.AgentCardSpec{
					Selector: &agentv1alpha1.AgentSelector{
						MatchLabels: map[string]string{
							"app.kubernetes.io/name": "nonexistent-workload",
							LabelAgentType:           LabelValueAgent,
						},
					},
				},
			}

			By("calling findMatchingWorkloadBySelector")
			workload, err := reconciler.findMatchingWorkloadBySelector(ctx, agentCard)

			By("verifying ErrWorkloadNotFound is returned")
			Expect(err).To(HaveOccurred())
			Expect(errors.Is(err, ErrWorkloadNotFound)).To(BeTrue())
			Expect(workload).To(BeNil())
		})

		It("should return error when selector is nil", func() {
			By("creating an AgentCard without selector")
			agentCard := &agentv1alpha1.AgentCard{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-card-nil-selector",
					Namespace: namespace,
				},
				Spec: agentv1alpha1.AgentCardSpec{
					// No selector
				},
			}

			By("calling findMatchingWorkloadBySelector")
			workload, err := reconciler.findMatchingWorkloadBySelector(ctx, agentCard)

			By("verifying error is returned")
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("no selector specified"))
			Expect(workload).To(BeNil())
		})
	})
})

var _ = Describe("AgentCard Controller - getWorkloadByTargetRef", func() {
	const namespace = "default"

	var (
		ctx        context.Context
		reconciler *AgentCardReconciler
	)

	BeforeEach(func() {
		ctx = context.Background()
		reconciler = &AgentCardReconciler{
			Client: k8sClient,
			Scheme: k8sClient.Scheme(),
		}
	})

	Context("When using targetRef with Deployment", func() {
		const deploymentName = "test-targetref-deployment"

		AfterEach(func() {
			deployment := &appsv1.Deployment{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: deploymentName, Namespace: namespace}, deployment); err == nil {
				Expect(k8sClient.Delete(ctx, deployment)).To(Succeed())
			}
		})

		It("should fetch Deployment by targetRef with agent label", func() {
			By("creating a Deployment with agent labels")
			deployment := &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      deploymentName,
					Namespace: namespace,
					Labels: map[string]string{
						LabelAgentType:       LabelValueAgent,
						LabelKagentiProtocol: "a2a",
					},
				},
				Spec: appsv1.DeploymentSpec{
					Selector: &metav1.LabelSelector{
						MatchLabels: map[string]string{"app": deploymentName},
					},
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Labels: map[string]string{"app": deploymentName},
						},
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{Name: "agent", Image: "test:latest"},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, deployment)).To(Succeed())

			By("calling getWorkloadByTargetRef")
			targetRef := &agentv1alpha1.TargetRef{
				APIVersion: "apps/v1",
				Kind:       "Deployment",
				Name:       deploymentName,
			}
			workload, err := reconciler.getWorkloadByTargetRef(ctx, namespace, targetRef)

			By("verifying the Deployment was found")
			Expect(err).NotTo(HaveOccurred())
			Expect(workload).NotTo(BeNil())
			Expect(workload.Name).To(Equal(deploymentName))
			Expect(workload.Kind).To(Equal("Deployment"))
			Expect(workload.APIVersion).To(Equal("apps/v1"))
			Expect(workload.Namespace).To(Equal(namespace))
			Expect(workload.ServiceName).To(Equal(deploymentName))
		})

		It("should detect Deployment readiness when Available condition is True", func() {
			By("creating a Deployment with agent labels")
			deployment := &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      deploymentName,
					Namespace: namespace,
					Labels: map[string]string{
						LabelAgentType:       LabelValueAgent,
						LabelKagentiProtocol: "a2a",
					},
				},
				Spec: appsv1.DeploymentSpec{
					Selector: &metav1.LabelSelector{
						MatchLabels: map[string]string{"app": deploymentName},
					},
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Labels: map[string]string{"app": deploymentName},
						},
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{Name: "agent", Image: "test:latest"},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, deployment)).To(Succeed())

			By("updating Deployment status to Available")
			Eventually(func() error {
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: deploymentName, Namespace: namespace}, deployment); err != nil {
					return err
				}
				deployment.Status.Conditions = []appsv1.DeploymentCondition{
					{
						Type:   appsv1.DeploymentAvailable,
						Status: corev1.ConditionTrue,
					},
				}
				return k8sClient.Status().Update(ctx, deployment)
			}).Should(Succeed())

			By("calling getWorkloadByTargetRef")
			targetRef := &agentv1alpha1.TargetRef{
				APIVersion: "apps/v1",
				Kind:       "Deployment",
				Name:       deploymentName,
			}
			workload, err := reconciler.getWorkloadByTargetRef(ctx, namespace, targetRef)

			By("verifying readiness is detected")
			Expect(err).NotTo(HaveOccurred())
			Expect(workload.Ready).To(BeTrue())
		})
	})

	Context("When using targetRef with StatefulSet", func() {
		const statefulSetName = "test-targetref-statefulset"

		AfterEach(func() {
			statefulSet := &appsv1.StatefulSet{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: statefulSetName, Namespace: namespace}, statefulSet); err == nil {
				Expect(k8sClient.Delete(ctx, statefulSet)).To(Succeed())
			}
		})

		It("should fetch StatefulSet by targetRef with agent label", func() {
			By("creating a StatefulSet with agent labels")
			statefulSet := &appsv1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      statefulSetName,
					Namespace: namespace,
					Labels: map[string]string{
						LabelAgentType:       LabelValueAgent,
						LabelKagentiProtocol: "a2a",
					},
				},
				Spec: appsv1.StatefulSetSpec{
					ServiceName: statefulSetName,
					Selector: &metav1.LabelSelector{
						MatchLabels: map[string]string{"app": statefulSetName},
					},
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Labels: map[string]string{"app": statefulSetName},
						},
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{Name: "agent", Image: "test:latest"},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, statefulSet)).To(Succeed())

			By("calling getWorkloadByTargetRef")
			targetRef := &agentv1alpha1.TargetRef{
				APIVersion: "apps/v1",
				Kind:       "StatefulSet",
				Name:       statefulSetName,
			}
			workload, err := reconciler.getWorkloadByTargetRef(ctx, namespace, targetRef)

			By("verifying the StatefulSet was found")
			Expect(err).NotTo(HaveOccurred())
			Expect(workload).NotTo(BeNil())
			Expect(workload.Name).To(Equal(statefulSetName))
			Expect(workload.Kind).To(Equal("StatefulSet"))
			Expect(workload.APIVersion).To(Equal("apps/v1"))
		})

		It("should detect StatefulSet readiness when replicas match", func() {
			By("creating a StatefulSet with agent labels")
			statefulSet := &appsv1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      statefulSetName,
					Namespace: namespace,
					Labels: map[string]string{
						LabelAgentType:       LabelValueAgent,
						LabelKagentiProtocol: "a2a",
					},
				},
				Spec: appsv1.StatefulSetSpec{
					ServiceName: statefulSetName,
					Selector: &metav1.LabelSelector{
						MatchLabels: map[string]string{"app": statefulSetName},
					},
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Labels: map[string]string{"app": statefulSetName},
						},
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{Name: "agent", Image: "test:latest"},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, statefulSet)).To(Succeed())

			By("updating StatefulSet status with ready replicas")
			Eventually(func() error {
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: statefulSetName, Namespace: namespace}, statefulSet); err != nil {
					return err
				}
				statefulSet.Status.Replicas = 1
				statefulSet.Status.ReadyReplicas = 1
				return k8sClient.Status().Update(ctx, statefulSet)
			}).Should(Succeed())

			By("calling getWorkloadByTargetRef")
			targetRef := &agentv1alpha1.TargetRef{
				APIVersion: "apps/v1",
				Kind:       "StatefulSet",
				Name:       statefulSetName,
			}
			workload, err := reconciler.getWorkloadByTargetRef(ctx, namespace, targetRef)

			By("verifying readiness is detected")
			Expect(err).NotTo(HaveOccurred())
			Expect(workload.Ready).To(BeTrue())
		})
	})

	Context("When targetRef references non-existent workload", func() {
		It("should return ErrWorkloadNotFound for non-existent Deployment", func() {
			targetRef := &agentv1alpha1.TargetRef{
				APIVersion: "apps/v1",
				Kind:       "Deployment",
				Name:       "nonexistent-deployment",
			}

			workload, err := reconciler.getWorkloadByTargetRef(ctx, namespace, targetRef)

			Expect(err).To(HaveOccurred())
			Expect(errors.Is(err, ErrWorkloadNotFound)).To(BeTrue())
			Expect(workload).To(BeNil())
		})

		It("should return ErrWorkloadNotFound for non-existent StatefulSet", func() {
			targetRef := &agentv1alpha1.TargetRef{
				APIVersion: "apps/v1",
				Kind:       "StatefulSet",
				Name:       "nonexistent-statefulset",
			}

			workload, err := reconciler.getWorkloadByTargetRef(ctx, namespace, targetRef)

			Expect(err).To(HaveOccurred())
			Expect(errors.Is(err, ErrWorkloadNotFound)).To(BeTrue())
			Expect(workload).To(BeNil())
		})
	})

	Context("When targetRef references workload without agent label", func() {
		const deploymentName = "test-no-agent-label-deployment"

		AfterEach(func() {
			deployment := &appsv1.Deployment{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: deploymentName, Namespace: namespace}, deployment); err == nil {
				Expect(k8sClient.Delete(ctx, deployment)).To(Succeed())
			}
		})

		It("should return ErrNotAgentWorkload when Deployment lacks agent label", func() {
			By("creating a Deployment without agent label")
			deployment := &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      deploymentName,
					Namespace: namespace,
					Labels: map[string]string{
						"app": deploymentName,
						// Missing LabelAgentType
					},
				},
				Spec: appsv1.DeploymentSpec{
					Selector: &metav1.LabelSelector{
						MatchLabels: map[string]string{"app": deploymentName},
					},
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Labels: map[string]string{"app": deploymentName},
						},
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{Name: "agent", Image: "test:latest"},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, deployment)).To(Succeed())

			By("calling getWorkloadByTargetRef")
			targetRef := &agentv1alpha1.TargetRef{
				APIVersion: "apps/v1",
				Kind:       "Deployment",
				Name:       deploymentName,
			}
			workload, err := reconciler.getWorkloadByTargetRef(ctx, namespace, targetRef)

			By("verifying ErrNotAgentWorkload is returned")
			Expect(err).To(HaveOccurred())
			Expect(errors.Is(err, ErrNotAgentWorkload)).To(BeTrue())
			Expect(workload).To(BeNil())
		})
	})
})

var _ = Describe("AgentCard Controller - getWorkload orchestration", func() {
	const namespace = "default"

	var (
		ctx        context.Context
		reconciler *AgentCardReconciler
	)

	BeforeEach(func() {
		ctx = context.Background()
		reconciler = &AgentCardReconciler{
			Client: k8sClient,
			Scheme: k8sClient.Scheme(),
		}
	})

	Context("When both targetRef and selector are specified", func() {
		const deploymentName = "test-getworkload-deployment"

		AfterEach(func() {
			deployment := &appsv1.Deployment{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: deploymentName, Namespace: namespace}, deployment); err == nil {
				Expect(k8sClient.Delete(ctx, deployment)).To(Succeed())
			}
		})

		It("should prefer targetRef over selector", func() {
			By("creating a Deployment with agent labels")
			deployment := &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      deploymentName,
					Namespace: namespace,
					Labels: map[string]string{
						"app.kubernetes.io/name": deploymentName,
						LabelAgentType:           LabelValueAgent,
						LabelKagentiProtocol:     "a2a",
					},
				},
				Spec: appsv1.DeploymentSpec{
					Selector: &metav1.LabelSelector{
						MatchLabels: map[string]string{"app": deploymentName},
					},
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Labels: map[string]string{"app": deploymentName},
						},
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{Name: "agent", Image: "test:latest"},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, deployment)).To(Succeed())

			By("creating an AgentCard with both targetRef and selector")
			agentCard := &agentv1alpha1.AgentCard{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-card-both",
					Namespace: namespace,
				},
				Spec: agentv1alpha1.AgentCardSpec{
					TargetRef: &agentv1alpha1.TargetRef{
						APIVersion: "apps/v1",
						Kind:       "Deployment",
						Name:       deploymentName,
					},
					Selector: &agentv1alpha1.AgentSelector{
						MatchLabels: map[string]string{
							"app.kubernetes.io/name": "different-name", // Different selector
						},
					},
				},
			}

			By("calling getWorkload")
			workload, err := reconciler.getWorkload(ctx, agentCard)

			By("verifying targetRef was used (not selector)")
			Expect(err).NotTo(HaveOccurred())
			Expect(workload).NotTo(BeNil())
			Expect(workload.Name).To(Equal(deploymentName))
		})
	})

	Context("When only selector is specified (no targetRef)", func() {
		const deploymentName = "test-selector-only-deployment"

		AfterEach(func() {
			deployment := &appsv1.Deployment{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: deploymentName, Namespace: namespace}, deployment); err == nil {
				Expect(k8sClient.Delete(ctx, deployment)).To(Succeed())
			}
		})

		It("should fall back to selector when targetRef is nil", func() {
			By("creating a Deployment with agent labels")
			deployment := &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      deploymentName,
					Namespace: namespace,
					Labels: map[string]string{
						"app.kubernetes.io/name": deploymentName,
						LabelAgentType:           LabelValueAgent,
						LabelKagentiProtocol:     "a2a",
					},
				},
				Spec: appsv1.DeploymentSpec{
					Selector: &metav1.LabelSelector{
						MatchLabels: map[string]string{"app": deploymentName},
					},
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Labels: map[string]string{"app": deploymentName},
						},
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{Name: "agent", Image: "test:latest"},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, deployment)).To(Succeed())

			By("creating an AgentCard with only selector (no targetRef)")
			agentCard := &agentv1alpha1.AgentCard{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-card-selector-only",
					Namespace: namespace,
				},
				Spec: agentv1alpha1.AgentCardSpec{
					// No TargetRef
					Selector: &agentv1alpha1.AgentSelector{
						MatchLabels: map[string]string{
							"app.kubernetes.io/name": deploymentName,
							LabelAgentType:           LabelValueAgent,
						},
					},
				},
			}

			By("calling getWorkload")
			workload, err := reconciler.getWorkload(ctx, agentCard)

			By("verifying selector fallback worked")
			Expect(err).NotTo(HaveOccurred())
			Expect(workload).NotTo(BeNil())
			Expect(workload.Name).To(Equal(deploymentName))
		})
	})

	Context("When neither targetRef nor selector is specified", func() {
		It("should return error when both are nil", func() {
			agentCard := &agentv1alpha1.AgentCard{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-card-no-ref",
					Namespace: namespace,
				},
				Spec: agentv1alpha1.AgentCardSpec{
					// No TargetRef, no Selector
				},
			}

			workload, err := reconciler.getWorkload(ctx, agentCard)

			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("neither targetRef nor selector specified"))
			Expect(workload).To(BeNil())
		})
	})
})

var _ = Describe("getWorkloadProtocol", func() {
	It("should return new label value when both labels are present", func() {
		labels := map[string]string{
			LabelKagentiProtocol: "a2a",       // New label
			LabelAgentProtocol:   "old-value", // Legacy label
		}

		protocol := getWorkloadProtocol(labels)

		Expect(protocol).To(Equal("a2a"))
	})

	It("should fall back to legacy label when new label is absent", func() {
		labels := map[string]string{
			LabelAgentProtocol: "a2a", // Only legacy label
		}

		protocol := getWorkloadProtocol(labels)

		Expect(protocol).To(Equal("a2a"))
	})

	It("should return empty string when neither label is present", func() {
		labels := map[string]string{
			"some-other-label": "value",
		}

		protocol := getWorkloadProtocol(labels)

		Expect(protocol).To(BeEmpty())
	})

	It("should return empty string when labels map is nil", func() {
		protocol := getWorkloadProtocol(nil)

		Expect(protocol).To(BeEmpty())
	})

	It("should return empty string when labels map is empty", func() {
		labels := map[string]string{}

		protocol := getWorkloadProtocol(labels)

		Expect(protocol).To(BeEmpty())
	})

	It("should use new label even when legacy label has different value", func() {
		labels := map[string]string{
			LabelKagentiProtocol: "mcp",
			LabelAgentProtocol:   "a2a",
		}

		protocol := getWorkloadProtocol(labels)

		Expect(protocol).To(Equal("mcp"))
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
