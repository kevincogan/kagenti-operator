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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

var _ = Describe("mapWorkloadToAgentCards", func() {
	const namespace = "default"

	ctx := context.Background()
	logger := log.Log.WithName("indexers-test")

	Context("when the workload does not have agent labels", func() {
		It("should return no reconcile requests", func() {
			deploy := &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "non-agent-deploy",
					Namespace: namespace,
					Labels: map[string]string{
						"app": "something-else",
					},
				},
			}

			mapFn := mapWorkloadToAgentCards(k8sClient, "apps/v1", "Deployment", logger)
			requests := mapFn(ctx, deploy)
			Expect(requests).To(BeEmpty())
		})
	})

	Context("when the workload has nil labels", func() {
		It("should return no reconcile requests", func() {
			deploy := &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "nil-labels-deploy",
					Namespace: namespace,
				},
			}

			mapFn := mapWorkloadToAgentCards(k8sClient, "apps/v1", "Deployment", logger)
			requests := mapFn(ctx, deploy)
			Expect(requests).To(BeEmpty())
		})
	})

	Context("when the workload has agent label with wrong value", func() {
		It("should return no reconcile requests", func() {
			deploy := &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "wrong-label-deploy",
					Namespace: namespace,
					Labels: map[string]string{
						LabelAgentType: "not-agent",
					},
				},
			}

			mapFn := mapWorkloadToAgentCards(k8sClient, "apps/v1", "Deployment", logger)
			requests := mapFn(ctx, deploy)
			Expect(requests).To(BeEmpty())
		})
	})
})
