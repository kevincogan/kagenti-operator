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
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	agentv1alpha1 "github.com/kagenti/operator/api/v1alpha1"
)

var _ = Describe("AgentCardSync Controller", func() {
	Context("When reconciling an Agent with agent labels", func() {
		const (
			agentName     = "test-sync-agent"
			agentCardName = "test-sync-agent-agent-card"
			namespace     = "default"
		)

		ctx := context.Background()

		agentNamespacedName := types.NamespacedName{
			Name:      agentName,
			Namespace: namespace,
		}

		BeforeEach(func() {
			By("creating an Agent with kagenti.io/type=agent label")
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
		})

		AfterEach(func() {
			By("cleaning up the Agent resource")
			agent := &agentv1alpha1.Agent{}
			err := k8sClient.Get(ctx, agentNamespacedName, agent)
			if err == nil {
				Expect(k8sClient.Delete(ctx, agent)).To(Succeed())
			}

			By("cleaning up any created AgentCard resource")
			agentCard := &agentv1alpha1.AgentCard{}
			err = k8sClient.Get(ctx, types.NamespacedName{
				Name:      agentCardName,
				Namespace: namespace,
			}, agentCard)
			if err == nil {
				Expect(k8sClient.Delete(ctx, agentCard)).To(Succeed())
			}
		})

		It("should automatically create an AgentCard with targetRef", func() {
			By("reconciling the Agent")
			reconciler := &AgentCardSyncReconciler{
				Client:               k8sClient,
				Scheme:               k8sClient.Scheme(),
				EnableLegacyAgentCRD: true,
			}

			_, err := reconciler.ReconcileAgent(ctx, reconcile.Request{
				NamespacedName: agentNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			By("checking that an AgentCard was created")
			agentCard := &agentv1alpha1.AgentCard{}
			Eventually(func() bool {
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      agentCardName,
					Namespace: namespace,
				}, agentCard)
				return err == nil
			}).Should(BeTrue())

			By("verifying the AgentCard has correct targetRef")
			Expect(agentCard.Spec.TargetRef).NotTo(BeNil())
			Expect(agentCard.Spec.TargetRef.APIVersion).To(Equal("agent.kagenti.dev/v1alpha1"))
			Expect(agentCard.Spec.TargetRef.Kind).To(Equal("Agent"))
			Expect(agentCard.Spec.TargetRef.Name).To(Equal(agentName))

			By("verifying the AgentCard has owner reference")
			Expect(agentCard.OwnerReferences).NotTo(BeEmpty())
			Expect(agentCard.OwnerReferences[0].Kind).To(Equal("Agent"))
			Expect(agentCard.OwnerReferences[0].Name).To(Equal(agentName))
		})

		It("should not create AgentCard for agents without protocol label", func() {
			const agentNoProtocol = "test-no-protocol-agent"

			By("creating an Agent without protocol label")
			agent := &agentv1alpha1.Agent{
				ObjectMeta: metav1.ObjectMeta{
					Name:      agentNoProtocol,
					Namespace: namespace,
					Labels: map[string]string{
						LabelAgentType: LabelValueAgent,
						// No protocol label
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

			By("reconciling the Agent")
			reconciler := &AgentCardSyncReconciler{
				Client:               k8sClient,
				Scheme:               k8sClient.Scheme(),
				EnableLegacyAgentCRD: true,
			}

			_, err := reconciler.ReconcileAgent(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      agentNoProtocol,
					Namespace: namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("verifying no AgentCard was created")
			agentCard := &agentv1alpha1.AgentCard{}
			err = k8sClient.Get(ctx, types.NamespacedName{
				Name:      agentNoProtocol + "-agent-card",
				Namespace: namespace,
			}, agentCard)
			Expect(errors.IsNotFound(err)).To(BeTrue())

			By("cleaning up the test Agent")
			Expect(k8sClient.Delete(ctx, agent)).To(Succeed())
		})
	})

	Context("When reconciling a Deployment with agent labels", func() {
		const (
			deploymentName = "test-sync-deployment"
			agentCardName  = "test-sync-deployment-deployment-card"
			namespace      = "default"
		)

		ctx := context.Background()

		deploymentNamespacedName := types.NamespacedName{
			Name:      deploymentName,
			Namespace: namespace,
		}

		BeforeEach(func() {
			By("creating a Deployment with kagenti.io/type=agent label")
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
					Replicas: ptr.To(int32(1)),
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
		})

		AfterEach(func() {
			By("cleaning up the Deployment resource")
			deployment := &appsv1.Deployment{}
			err := k8sClient.Get(ctx, deploymentNamespacedName, deployment)
			if err == nil {
				Expect(k8sClient.Delete(ctx, deployment)).To(Succeed())
			}

			By("cleaning up any created AgentCard resource")
			agentCard := &agentv1alpha1.AgentCard{}
			err = k8sClient.Get(ctx, types.NamespacedName{
				Name:      agentCardName,
				Namespace: namespace,
			}, agentCard)
			if err == nil {
				Expect(k8sClient.Delete(ctx, agentCard)).To(Succeed())
			}
		})

		It("should automatically create an AgentCard with targetRef for Deployment", func() {
			By("reconciling the Deployment")
			reconciler := &AgentCardSyncReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := reconciler.ReconcileDeployment(ctx, reconcile.Request{
				NamespacedName: deploymentNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			By("checking that an AgentCard was created")
			agentCard := &agentv1alpha1.AgentCard{}
			Eventually(func() bool {
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      agentCardName,
					Namespace: namespace,
				}, agentCard)
				return err == nil
			}).Should(BeTrue())

			By("verifying the AgentCard has correct targetRef")
			Expect(agentCard.Spec.TargetRef).NotTo(BeNil())
			Expect(agentCard.Spec.TargetRef.APIVersion).To(Equal("apps/v1"))
			Expect(agentCard.Spec.TargetRef.Kind).To(Equal("Deployment"))
			Expect(agentCard.Spec.TargetRef.Name).To(Equal(deploymentName))

			By("verifying the AgentCard has owner reference")
			Expect(agentCard.OwnerReferences).NotTo(BeEmpty())
			Expect(agentCard.OwnerReferences[0].Kind).To(Equal("Deployment"))
			Expect(agentCard.OwnerReferences[0].Name).To(Equal(deploymentName))
		})

		It("should not create AgentCard for Deployments without protocol label", func() {
			const deploymentNoProtocol = "test-no-protocol-deployment"

			By("creating a Deployment without protocol label")
			deployment := &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      deploymentNoProtocol,
					Namespace: namespace,
					Labels: map[string]string{
						LabelAgentType: LabelValueAgent,
						// No protocol label
					},
				},
				Spec: appsv1.DeploymentSpec{
					Replicas: ptr.To(int32(1)),
					Selector: &metav1.LabelSelector{
						MatchLabels: map[string]string{
							"app": deploymentNoProtocol,
						},
					},
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Labels: map[string]string{
								"app": deploymentNoProtocol,
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

			By("reconciling the Deployment")
			reconciler := &AgentCardSyncReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := reconciler.ReconcileDeployment(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      deploymentNoProtocol,
					Namespace: namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("verifying no AgentCard was created")
			agentCard := &agentv1alpha1.AgentCard{}
			err = k8sClient.Get(ctx, types.NamespacedName{
				Name:      deploymentNoProtocol + "-deployment-card",
				Namespace: namespace,
			}, agentCard)
			Expect(errors.IsNotFound(err)).To(BeTrue())

			By("cleaning up the test Deployment")
			Expect(k8sClient.Delete(ctx, deployment)).To(Succeed())
		})
	})

	Context("When reconciling a StatefulSet with agent labels", func() {
		const (
			statefulsetName = "test-sync-statefulset"
			agentCardName   = "test-sync-statefulset-statefulset-card"
			namespace       = "default"
		)

		ctx := context.Background()

		statefulsetNamespacedName := types.NamespacedName{
			Name:      statefulsetName,
			Namespace: namespace,
		}

		BeforeEach(func() {
			By("creating a StatefulSet with kagenti.io/type=agent label")
			statefulset := &appsv1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      statefulsetName,
					Namespace: namespace,
					Labels: map[string]string{
						"app.kubernetes.io/name": statefulsetName,
						LabelAgentType:           LabelValueAgent,
						LabelKagentiProtocol:     "a2a",
					},
				},
				Spec: appsv1.StatefulSetSpec{
					Replicas:    ptr.To(int32(1)),
					ServiceName: statefulsetName,
					Selector: &metav1.LabelSelector{
						MatchLabels: map[string]string{
							"app": statefulsetName,
						},
					},
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Labels: map[string]string{
								"app": statefulsetName,
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
			Expect(k8sClient.Create(ctx, statefulset)).To(Succeed())
		})

		AfterEach(func() {
			By("cleaning up the StatefulSet resource")
			statefulset := &appsv1.StatefulSet{}
			err := k8sClient.Get(ctx, statefulsetNamespacedName, statefulset)
			if err == nil {
				Expect(k8sClient.Delete(ctx, statefulset)).To(Succeed())
			}

			By("cleaning up any created AgentCard resource")
			agentCard := &agentv1alpha1.AgentCard{}
			err = k8sClient.Get(ctx, types.NamespacedName{
				Name:      agentCardName,
				Namespace: namespace,
			}, agentCard)
			if err == nil {
				Expect(k8sClient.Delete(ctx, agentCard)).To(Succeed())
			}
		})

		It("should automatically create an AgentCard with targetRef for StatefulSet", func() {
			By("reconciling the StatefulSet")
			reconciler := &AgentCardSyncReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := reconciler.ReconcileStatefulSet(ctx, reconcile.Request{
				NamespacedName: statefulsetNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			By("checking that an AgentCard was created")
			agentCard := &agentv1alpha1.AgentCard{}
			Eventually(func() bool {
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      agentCardName,
					Namespace: namespace,
				}, agentCard)
				return err == nil
			}).Should(BeTrue())

			By("verifying the AgentCard has correct targetRef")
			Expect(agentCard.Spec.TargetRef).NotTo(BeNil())
			Expect(agentCard.Spec.TargetRef.APIVersion).To(Equal("apps/v1"))
			Expect(agentCard.Spec.TargetRef.Kind).To(Equal("StatefulSet"))
			Expect(agentCard.Spec.TargetRef.Name).To(Equal(statefulsetName))

			By("verifying the AgentCard has owner reference")
			Expect(agentCard.OwnerReferences).NotTo(BeEmpty())
			Expect(agentCard.OwnerReferences[0].Kind).To(Equal("StatefulSet"))
			Expect(agentCard.OwnerReferences[0].Name).To(Equal(statefulsetName))
		})
	})

	Context("When migrating an AgentCard from selector to targetRef", func() {
		const (
			deploymentName = "test-migration-deployment"
			agentCardName  = "test-migration-deployment-deployment-card"
			namespace      = "default"
		)

		ctx := context.Background()

		BeforeEach(func() {
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
					Replicas: ptr.To(int32(1)),
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

			By("creating an AgentCard with selector (legacy format)")
			agentCard := &agentv1alpha1.AgentCard{
				ObjectMeta: metav1.ObjectMeta{
					Name:      agentCardName,
					Namespace: namespace,
					Labels: map[string]string{
						"app.kubernetes.io/name":       deploymentName,
						"app.kubernetes.io/managed-by": "kagenti-operator",
					},
				},
				Spec: agentv1alpha1.AgentCardSpec{
					SyncPeriod: "30s",
					Selector: &agentv1alpha1.AgentSelector{
						MatchLabels: map[string]string{
							"app.kubernetes.io/name": deploymentName,
							LabelAgentType:           LabelValueAgent,
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, agentCard)).To(Succeed())
		})

		AfterEach(func() {
			By("cleaning up the Deployment resource")
			deployment := &appsv1.Deployment{}
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      deploymentName,
				Namespace: namespace,
			}, deployment)
			if err == nil {
				Expect(k8sClient.Delete(ctx, deployment)).To(Succeed())
			}

			By("cleaning up any created AgentCard resource")
			agentCard := &agentv1alpha1.AgentCard{}
			err = k8sClient.Get(ctx, types.NamespacedName{
				Name:      agentCardName,
				Namespace: namespace,
			}, agentCard)
			if err == nil {
				Expect(k8sClient.Delete(ctx, agentCard)).To(Succeed())
			}
		})

		It("should migrate AgentCard from selector to targetRef", func() {
			By("reconciling the Deployment")
			reconciler := &AgentCardSyncReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := reconciler.ReconcileDeployment(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      deploymentName,
					Namespace: namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("checking that the AgentCard was updated with targetRef")
			agentCard := &agentv1alpha1.AgentCard{}
			err = k8sClient.Get(ctx, types.NamespacedName{
				Name:      agentCardName,
				Namespace: namespace,
			}, agentCard)
			Expect(err).NotTo(HaveOccurred())

			By("verifying the AgentCard now has targetRef")
			Expect(agentCard.Spec.TargetRef).NotTo(BeNil())
			Expect(agentCard.Spec.TargetRef.APIVersion).To(Equal("apps/v1"))
			Expect(agentCard.Spec.TargetRef.Kind).To(Equal("Deployment"))
			Expect(agentCard.Spec.TargetRef.Name).To(Equal(deploymentName))

			By("verifying the AgentCard has owner reference")
			Expect(agentCard.OwnerReferences).NotTo(BeEmpty())
			Expect(agentCard.OwnerReferences[0].Kind).To(Equal("Deployment"))
			Expect(agentCard.OwnerReferences[0].Name).To(Equal(deploymentName))
		})
	})
})
