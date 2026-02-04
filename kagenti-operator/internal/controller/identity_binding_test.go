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
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	agentv1alpha1 "github.com/kagenti/operator/api/v1alpha1"
	"github.com/kagenti/operator/internal/distribution"
)

var _ = Describe("Identity Binding", func() {
	const (
		timeout  = time.Second * 10
		interval = time.Millisecond * 250
	)

	Context("AgentCard Binding Evaluation - Matching", func() {
		const (
			agentName     = "bind-eval-match-agent"
			agentCardName = "bind-eval-match-card"
			namespace     = "default"
			trustDomain   = "test.local"
		)

		ctx := context.Background()

		AfterEach(func() {
			// Clean up resources with proper wait
			By("cleaning up test resources")
			cleanupResource(ctx, &agentv1alpha1.AgentCard{}, agentCardName, namespace)
			cleanupResource(ctx, &agentv1alpha1.Agent{}, agentName, namespace)
			cleanupResource(ctx, &corev1.Service{}, agentName, namespace)
		})

		It("should evaluate binding as Bound when SPIFFE ID matches allowlist", func() {
			By("creating an Agent with specific service account")
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
							ServiceAccountName: "test-sa",
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

			// Update agent status to Ready
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
					Name:      agentName,
					Namespace: namespace,
				},
				Spec: corev1.ServiceSpec{
					Ports: []corev1.ServicePort{
						{Name: "http", Port: 8000, Protocol: corev1.ProtocolTCP},
					},
					Selector: map[string]string{"app.kubernetes.io/name": agentName},
				},
			}
			Expect(k8sClient.Create(ctx, service)).To(Succeed())

			By("creating an AgentCard with matching SPIFFE ID")
			expectedSpiffeID := "spiffe://" + trustDomain + "/ns/" + namespace + "/sa/test-sa"
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
					IdentityBinding: &agentv1alpha1.IdentityBinding{
						TrustDomain:      trustDomain,
						AllowedSpiffeIDs: []agentv1alpha1.SpiffeID{agentv1alpha1.SpiffeID(expectedSpiffeID)},
						Strict:           false,
					},
				},
			}
			Expect(k8sClient.Create(ctx, agentCard)).To(Succeed())

			By("reconciling the AgentCard")
			reconciler := &AgentCardReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
				AgentFetcher: &mockFetcher{
					cardData: &agentv1alpha1.AgentCardData{
						Name:    "Test Agent",
						Version: "1.0.0",
						URL:     "http://localhost:8000",
					},
				},
				TrustDomain: trustDomain,
			}

			// First reconcile adds finalizer
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: agentCardName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			// Second reconcile evaluates binding
			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: agentCardName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			By("verifying binding status is Bound")
			Eventually(func() bool {
				card := &agentv1alpha1.AgentCard{}
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: agentCardName, Namespace: namespace}, card); err != nil {
					return false
				}
				return card.Status.BindingStatus != nil && card.Status.BindingStatus.Bound
			}, timeout, interval).Should(BeTrue())

			By("verifying expected SPIFFE ID is set")
			card := &agentv1alpha1.AgentCard{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: agentCardName, Namespace: namespace}, card)).To(Succeed())
			Expect(card.Status.ExpectedSpiffeID).To(Equal(expectedSpiffeID))
			Expect(card.Status.BindingStatus.Reason).To(Equal(ReasonBound))
		})

	})

	Context("AgentCard Binding Evaluation - NonMatching", func() {
		const (
			agentName     = "bind-eval-nomatch-agent"
			agentCardName = "bind-eval-nomatch-card"
			namespace     = "default"
			trustDomain   = "test.local"
		)

		ctx := context.Background()

		AfterEach(func() {
			By("cleaning up test resources")
			cleanupResource(ctx, &agentv1alpha1.AgentCard{}, agentCardName, namespace)
			cleanupResource(ctx, &agentv1alpha1.Agent{}, agentName, namespace)
			cleanupResource(ctx, &corev1.Service{}, agentName, namespace)
		})

		It("should evaluate binding as NotBound when SPIFFE ID is not in allowlist", func() {
			By("creating an Agent")
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
							ServiceAccountName: "test-sa",
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

			// Update agent status to Ready
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
					Name:      agentName,
					Namespace: namespace,
				},
				Spec: corev1.ServiceSpec{
					Ports: []corev1.ServicePort{
						{Name: "http", Port: 8000, Protocol: corev1.ProtocolTCP},
					},
					Selector: map[string]string{"app.kubernetes.io/name": agentName},
				},
			}
			Expect(k8sClient.Create(ctx, service)).To(Succeed())

			By("creating an AgentCard with non-matching SPIFFE ID")
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
					IdentityBinding: &agentv1alpha1.IdentityBinding{
						TrustDomain:      trustDomain,
						AllowedSpiffeIDs: []agentv1alpha1.SpiffeID{"spiffe://" + trustDomain + "/ns/other/sa/other-sa"},
						Strict:           false,
					},
				},
			}
			Expect(k8sClient.Create(ctx, agentCard)).To(Succeed())

			By("reconciling the AgentCard")
			reconciler := &AgentCardReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
				AgentFetcher: &mockFetcher{
					cardData: &agentv1alpha1.AgentCardData{
						Name:    "Test Agent",
						Version: "1.0.0",
						URL:     "http://localhost:8000",
					},
				},
				TrustDomain: trustDomain,
			}

			// First reconcile adds finalizer
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: agentCardName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			// Second reconcile evaluates binding
			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: agentCardName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			By("verifying binding status is NotBound")
			Eventually(func() bool {
				card := &agentv1alpha1.AgentCard{}
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: agentCardName, Namespace: namespace}, card); err != nil {
					return false
				}
				return card.Status.BindingStatus != nil && !card.Status.BindingStatus.Bound
			}, timeout, interval).Should(BeTrue())

			By("verifying reason is NotBound")
			card := &agentv1alpha1.AgentCard{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: agentCardName, Namespace: namespace}, card)).To(Succeed())
			Expect(card.Status.BindingStatus.Reason).To(Equal(ReasonNotBound))
		})
	})

	Context("Agent Binding Enforcement - Disable", func() {
		const (
			agentName     = "enforce-disable-agent"
			agentCardName = "enforce-disable-card"
			namespace     = "default"
			trustDomain   = "test.local"
		)

		ctx := context.Background()

		AfterEach(func() {
			By("cleaning up test resources")
			cleanupResource(ctx, &agentv1alpha1.AgentCard{}, agentCardName, namespace)
			cleanupResource(ctx, &agentv1alpha1.Agent{}, agentName, namespace)
			cleanupResource(ctx, &appsv1.Deployment{}, agentName, namespace)
		})

		It("should scale deployment to 0 when strict binding fails", func() {
			By("creating an Agent")
			agent := &agentv1alpha1.Agent{
				ObjectMeta: metav1.ObjectMeta{
					Name:      agentName,
					Namespace: namespace,
					Labels: map[string]string{
						"app.kubernetes.io/name": agentName,
						LabelAgentType:           LabelValueAgent,
					},
				},
				Spec: agentv1alpha1.AgentSpec{
					Replicas: ptr.To(int32(3)),
					PodTemplateSpec: &corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							ServiceAccountName: "test-sa",
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

			By("creating a Deployment for the Agent")
			labels := map[string]string{"app.kubernetes.io/name": agentName}
			deployment := &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      agentName,
					Namespace: namespace,
					Labels:    labels,
				},
				Spec: appsv1.DeploymentSpec{
					Replicas: ptr.To(int32(3)),
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
			Expect(k8sClient.Create(ctx, deployment)).To(Succeed())

			By("creating an AgentCard with strict binding that fails")
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
					IdentityBinding: &agentv1alpha1.IdentityBinding{
						TrustDomain:      trustDomain,
						AllowedSpiffeIDs: []agentv1alpha1.SpiffeID{"spiffe://" + trustDomain + "/ns/other/sa/other-sa"},
						Strict:           true,
					},
				},
			}
			Expect(k8sClient.Create(ctx, agentCard)).To(Succeed())

			// Set binding status to NotBound
			Eventually(func() error {
				card := &agentv1alpha1.AgentCard{}
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: agentCardName, Namespace: namespace}, card); err != nil {
					return err
				}
				now := metav1.Now()
				card.Status.BindingStatus = &agentv1alpha1.BindingStatus{
					Bound:              false,
					Reason:             ReasonNotBound,
					Message:            "SPIFFE ID not in allowlist",
					LastEvaluationTime: &now,
				}
				return k8sClient.Status().Update(ctx, card)
			}).Should(Succeed())

			By("reconciling the Agent")
			agentReconciler := &AgentReconciler{
				Client:       k8sClient,
				Scheme:       k8sClient.Scheme(),
				Distribution: distribution.Kubernetes,
			}

			// First reconcile adds finalizer
			_, err := agentReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: agentName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			// Second reconcile enforces binding
			_, err = agentReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: agentName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			By("verifying deployment is scaled to 0")
			Eventually(func() int32 {
				dep := &appsv1.Deployment{}
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: agentName, Namespace: namespace}, dep); err != nil {
					return -1
				}
				if dep.Spec.Replicas == nil {
					return -1
				}
				return *dep.Spec.Replicas
			}, timeout, interval).Should(Equal(int32(0)))

			By("verifying deployment has binding annotations")
			dep := &appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: agentName, Namespace: namespace}, dep)).To(Succeed())
			Expect(dep.Annotations[AnnotationDisabledBy]).To(Equal(DisabledByIdentityBinding))

			By("verifying agent status has binding enforcement info")
			Eventually(func() bool {
				ag := &agentv1alpha1.Agent{}
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: agentName, Namespace: namespace}, ag); err != nil {
					return false
				}
				return ag.Status.BindingEnforcement != nil && ag.Status.BindingEnforcement.DisabledByBinding
			}, timeout, interval).Should(BeTrue())

			ag := &agentv1alpha1.Agent{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: agentName, Namespace: namespace}, ag)).To(Succeed())
			Expect(ag.Status.BindingEnforcement.OriginalReplicas).NotTo(BeNil())
			Expect(*ag.Status.BindingEnforcement.OriginalReplicas).To(Equal(int32(3)))
		})

	})

	Context("Agent Binding Enforcement - Restore", func() {
		const (
			agentName     = "enforce-restore-agent"
			agentCardName = "enforce-restore-card"
			namespace     = "default"
			trustDomain   = "test.local"
		)

		ctx := context.Background()

		AfterEach(func() {
			By("cleaning up test resources")
			cleanupResource(ctx, &agentv1alpha1.AgentCard{}, agentCardName, namespace)
			cleanupResource(ctx, &agentv1alpha1.Agent{}, agentName, namespace)
			cleanupResource(ctx, &appsv1.Deployment{}, agentName, namespace)
		})

		It("should restore replicas when binding passes after being disabled", func() {
			By("creating an Agent with binding enforcement already in place")
			agent := &agentv1alpha1.Agent{
				ObjectMeta: metav1.ObjectMeta{
					Name:      agentName,
					Namespace: namespace,
					Labels: map[string]string{
						"app.kubernetes.io/name": agentName,
						LabelAgentType:           LabelValueAgent,
					},
				},
				Spec: agentv1alpha1.AgentSpec{
					Replicas: ptr.To(int32(3)),
					PodTemplateSpec: &corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							ServiceAccountName: "test-sa",
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

			// Set agent status to show it was disabled
			Eventually(func() error {
				ag := &agentv1alpha1.Agent{}
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: agentName, Namespace: namespace}, ag); err != nil {
					return err
				}
				now := metav1.Now()
				ag.Status.BindingEnforcement = &agentv1alpha1.BindingEnforcementStatus{
					DisabledByBinding: true,
					OriginalReplicas:  ptr.To(int32(3)),
					DisabledAt:        &now,
					DisabledReason:    "Test reason",
				}
				return k8sClient.Status().Update(ctx, ag)
			}).Should(Succeed())

			By("creating a Deployment scaled to 0 with binding annotations")
			labels := map[string]string{"app.kubernetes.io/name": agentName}
			deployment := &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      agentName,
					Namespace: namespace,
					Labels:    labels,
					Annotations: map[string]string{
						AnnotationDisabledBy:     DisabledByIdentityBinding,
						AnnotationDisabledReason: "Test reason",
					},
				},
				Spec: appsv1.DeploymentSpec{
					Replicas: ptr.To(int32(0)),
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
			Expect(k8sClient.Create(ctx, deployment)).To(Succeed())

			By("creating an AgentCard with strict binding that now passes")
			expectedSpiffeID := "spiffe://" + trustDomain + "/ns/" + namespace + "/sa/test-sa"
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
					IdentityBinding: &agentv1alpha1.IdentityBinding{
						TrustDomain:      trustDomain,
						AllowedSpiffeIDs: []agentv1alpha1.SpiffeID{agentv1alpha1.SpiffeID(expectedSpiffeID)},
						Strict:           true,
					},
				},
			}
			Expect(k8sClient.Create(ctx, agentCard)).To(Succeed())

			// Set binding status to Bound
			Eventually(func() error {
				card := &agentv1alpha1.AgentCard{}
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: agentCardName, Namespace: namespace}, card); err != nil {
					return err
				}
				now := metav1.Now()
				card.Status.BindingStatus = &agentv1alpha1.BindingStatus{
					Bound:              true,
					Reason:             ReasonBound,
					Message:            "SPIFFE ID in allowlist",
					LastEvaluationTime: &now,
				}
				card.Status.ExpectedSpiffeID = expectedSpiffeID
				return k8sClient.Status().Update(ctx, card)
			}).Should(Succeed())

			By("reconciling the Agent")
			agentReconciler := &AgentReconciler{
				Client:       k8sClient,
				Scheme:       k8sClient.Scheme(),
				Distribution: distribution.Kubernetes,
			}

			// First reconcile adds finalizer
			_, err := agentReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: agentName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			// Second reconcile restores the agent
			_, err = agentReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: agentName, Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			By("verifying deployment is restored to original replicas")
			Eventually(func() int32 {
				dep := &appsv1.Deployment{}
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: agentName, Namespace: namespace}, dep); err != nil {
					return -1
				}
				if dep.Spec.Replicas == nil {
					return -1
				}
				return *dep.Spec.Replicas
			}, timeout, interval).Should(Equal(int32(3)))

			By("verifying binding annotations are removed")
			dep := &appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: agentName, Namespace: namespace}, dep)).To(Succeed())
			Expect(dep.Annotations).NotTo(HaveKey(AnnotationDisabledBy))
			Expect(dep.Annotations).NotTo(HaveKey(AnnotationDisabledReason))

			By("verifying agent status binding enforcement is cleared")
			ag := &agentv1alpha1.Agent{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: agentName, Namespace: namespace}, ag)).To(Succeed())
			Expect(ag.Status.BindingEnforcement).To(BeNil())
		})
	})

	Context("Card ID Drift Detection", func() {
		It("should compute consistent card ID for same card data", func() {
			reconciler := &AgentCardReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			cardData := &agentv1alpha1.AgentCardData{
				Name:        "Test Agent",
				Description: "A test agent",
				Version:     "1.0.0",
				URL:         "http://localhost:8000",
			}

			cardId1 := reconciler.computeCardId(cardData)
			cardId2 := reconciler.computeCardId(cardData)

			Expect(cardId1).NotTo(BeEmpty())
			Expect(cardId1).To(Equal(cardId2))
		})

		It("should compute different card ID for different card data", func() {
			reconciler := &AgentCardReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			cardData1 := &agentv1alpha1.AgentCardData{
				Name:    "Test Agent",
				Version: "1.0.0",
			}

			cardData2 := &agentv1alpha1.AgentCardData{
				Name:    "Test Agent",
				Version: "2.0.0",
			}

			cardId1 := reconciler.computeCardId(cardData1)
			cardId2 := reconciler.computeCardId(cardData2)

			Expect(cardId1).NotTo(BeEmpty())
			Expect(cardId2).NotTo(BeEmpty())
			Expect(cardId1).NotTo(Equal(cardId2))
		})
	})

	Context("SPIFFE ID Derivation", func() {
		It("should derive expected SPIFFE ID from agent metadata", func() {
			reconciler := &AgentCardReconciler{
				Client:      k8sClient,
				Scheme:      k8sClient.Scheme(),
				TrustDomain: "my.domain",
			}

			// Create the agent in the cluster for getWorkloadServiceAccount to fetch
			agent := &agentv1alpha1.Agent{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "spiffe-test-agent",
					Namespace: "default",
				},
				Spec: agentv1alpha1.AgentSpec{
					PodTemplateSpec: &corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							ServiceAccountName: "my-service-account",
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
			Expect(k8sClient.Create(ctx, agent)).To(Succeed())
			defer k8sClient.Delete(ctx, agent)

			workload := &WorkloadInfo{
				Name:       agent.Name,
				Namespace:  agent.Namespace,
				Kind:       "Agent",
				APIVersion: agentv1alpha1.GroupVersion.String(),
			}

			sa, err := reconciler.getWorkloadServiceAccount(ctx, workload)
			Expect(err).NotTo(HaveOccurred())
			Expect(sa).To(Equal("my-service-account"))

			// Expected SPIFFE ID format: spiffe://<trust-domain>/ns/<namespace>/sa/<serviceAccount>
			expectedSpiffeID := "spiffe://my.domain/ns/default/sa/my-service-account"
			actualSpiffeID := "spiffe://" + reconciler.TrustDomain + "/ns/" + workload.Namespace + "/sa/" + sa
			Expect(actualSpiffeID).To(Equal(expectedSpiffeID))
		})

		It("should use default service account name pattern when not specified", func() {
			reconciler := &AgentCardReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			// Create the agent in the cluster for getWorkloadServiceAccount to fetch
			agent := &agentv1alpha1.Agent{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "spiffe-test-agent-default",
					Namespace: "default",
				},
				Spec: agentv1alpha1.AgentSpec{
					PodTemplateSpec: &corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							// No ServiceAccountName specified
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
			Expect(k8sClient.Create(ctx, agent)).To(Succeed())
			defer k8sClient.Delete(ctx, agent)

			workload := &WorkloadInfo{
				Name:       agent.Name,
				Namespace:  agent.Namespace,
				Kind:       "Agent",
				APIVersion: agentv1alpha1.GroupVersion.String(),
			}

			sa, err := reconciler.getWorkloadServiceAccount(ctx, workload)
			Expect(err).NotTo(HaveOccurred())
			Expect(sa).To(Equal("spiffe-test-agent-default-sa"))
		})
	})

	Context("Selector Matching", func() {
		It("should correctly match AgentCard selector to Agent", func() {
			reconciler := &AgentReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			card := &agentv1alpha1.AgentCard{
				Spec: agentv1alpha1.AgentCardSpec{
					Selector: &agentv1alpha1.AgentSelector{
						MatchLabels: map[string]string{
							"app": "test",
							"env": "prod",
						},
					},
				},
			}

			matchingAgent := &agentv1alpha1.Agent{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"app":   "test",
						"env":   "prod",
						"extra": "label",
					},
				},
			}

			nonMatchingAgent := &agentv1alpha1.Agent{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"app": "test",
						"env": "dev",
					},
				},
			}

			Expect(reconciler.agentCardSelectsAgent(card, matchingAgent)).To(BeTrue())
			Expect(reconciler.agentCardSelectsAgent(card, nonMatchingAgent)).To(BeFalse())
		})

		It("should not match when agent has no labels", func() {
			reconciler := &AgentReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			card := &agentv1alpha1.AgentCard{
				Spec: agentv1alpha1.AgentCardSpec{
					Selector: &agentv1alpha1.AgentSelector{
						MatchLabels: map[string]string{
							"app": "test",
						},
					},
				},
			}

			agent := &agentv1alpha1.Agent{
				ObjectMeta: metav1.ObjectMeta{
					Labels: nil,
				},
			}

			Expect(reconciler.agentCardSelectsAgent(card, agent)).To(BeFalse())
		})
	})
})

// Helper function to list AgentCards for an Agent
func listAgentCardsForAgent(ctx context.Context, c client.Client, agent *agentv1alpha1.Agent) ([]*agentv1alpha1.AgentCard, error) {
	cards := &agentv1alpha1.AgentCardList{}
	if err := c.List(ctx, cards, client.InNamespace(agent.Namespace)); err != nil {
		return nil, err
	}

	var result []*agentv1alpha1.AgentCard
	for i := range cards.Items {
		card := &cards.Items[i]
		if card.Labels == nil {
			continue
		}
		match := true
		for key, value := range card.Spec.Selector.MatchLabels {
			if agent.Labels[key] != value {
				match = false
				break
			}
		}
		if match {
			result = append(result, card)
		}
	}
	return result, nil
}

// cleanupResource removes a resource and waits for it to be fully deleted
func cleanupResource(ctx context.Context, obj client.Object, name, namespace string) {
	key := types.NamespacedName{Name: name, Namespace: namespace}

	// Try to get the object
	if err := k8sClient.Get(ctx, key, obj); err != nil {
		return // Object doesn't exist, nothing to clean up
	}

	// Remove finalizers to allow deletion
	obj.SetFinalizers(nil)
	_ = k8sClient.Update(ctx, obj)

	// Delete the object
	_ = k8sClient.Delete(ctx, obj)

	// Wait for deletion to complete
	Eventually(func() bool {
		err := k8sClient.Get(ctx, key, obj)
		return err != nil // Returns true when object is gone
	}, time.Second*5, time.Millisecond*100).Should(BeTrue())
}
