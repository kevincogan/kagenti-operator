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

package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kagenti/operator/test/utils"
)

// namespace where the project is deployed in
const namespace = "kagenti-operator-system"

// serviceAccountName created for the project
const serviceAccountName = "kagenti-operator-controller-manager"

// metricsServiceName is the name of the metrics service of the project
const metricsServiceName = "kagenti-operator-controller-manager-metrics-service"

// metricsRoleBindingName is the name of the RBAC that will be created to allow get the metrics data
const metricsRoleBindingName = "kagenti-operator-metrics-binding"

var _ = Describe("Manager", Ordered, func() {
	var controllerPodName string

	// Before running the tests, set up the environment by creating the namespace,
	// enforce the restricted security policy to the namespace, installing CRDs,
	// and deploying the controller.
	BeforeAll(func() {
		Expect(utils.DeployController(namespace, projectImage)).To(Succeed(), "Failed to deploy controller")
	})

	AfterAll(func() {
		By("cleaning up the curl pod for metrics")
		cmd := exec.Command("kubectl", "delete", "pod", "curl-metrics", "-n", namespace)
		_, _ = utils.Run(cmd)

		By("cleaning up metrics ClusterRoleBinding")
		cmd = exec.Command("kubectl", "delete", "clusterrolebinding", metricsRoleBindingName, "--ignore-not-found")
		_, _ = utils.Run(cmd)

		utils.UndeployController()

		By("removing manager namespace")
		cmd = exec.Command("kubectl", "delete", "ns", namespace)
		_, _ = utils.Run(cmd)
	})

	// After each test, check for failures and collect logs, events,
	// and pod descriptions for debugging.
	AfterEach(func() {
		specReport := CurrentSpecReport()
		if specReport.Failed() {
			By("Fetching controller manager pod logs")
			cmd := exec.Command("kubectl", "logs", controllerPodName, "-n", namespace)
			controllerLogs, err := utils.Run(cmd)
			if err == nil {
				_, _ = fmt.Fprintf(GinkgoWriter, "Controller logs:\n %s", controllerLogs)
			} else {
				_, _ = fmt.Fprintf(GinkgoWriter, "Failed to get Controller logs: %s", err)
			}

			By("Fetching Kubernetes events")
			cmd = exec.Command("kubectl", "get", "events", "-n", namespace, "--sort-by=.lastTimestamp")
			eventsOutput, err := utils.Run(cmd)
			if err == nil {
				_, _ = fmt.Fprintf(GinkgoWriter, "Kubernetes events:\n%s", eventsOutput)
			} else {
				_, _ = fmt.Fprintf(GinkgoWriter, "Failed to get Kubernetes events: %s", err)
			}

			By("Fetching curl-metrics logs")
			cmd = exec.Command("kubectl", "logs", "curl-metrics", "-n", namespace)
			metricsOutput, err := utils.Run(cmd)
			if err == nil {
				_, _ = fmt.Fprintf(GinkgoWriter, "Metrics logs:\n %s", metricsOutput)
			} else {
				_, _ = fmt.Fprintf(GinkgoWriter, "Failed to get curl-metrics logs: %s", err)
			}

			By("Fetching controller manager pod description")
			cmd = exec.Command("kubectl", "describe", "pod", controllerPodName, "-n", namespace)
			podDescription, err := utils.Run(cmd)
			if err == nil {
				fmt.Println("Pod description:\n", podDescription)
			} else {
				fmt.Println("Failed to describe controller pod")
			}
		}
	})

	SetDefaultEventuallyTimeout(2 * time.Minute)
	SetDefaultEventuallyPollingInterval(time.Second)

	Context("Manager", func() {
		It("should run successfully", func() {
			By("validating that the controller-manager pod is running as expected")
			verifyControllerUp := func(g Gomega) {
				// Get the name of the controller-manager pod
				cmd := exec.Command("kubectl", "get",
					"pods", "-l", "control-plane=controller-manager",
					"-o", "go-template={{ range .items }}"+
						"{{ if not .metadata.deletionTimestamp }}"+
						"{{ .metadata.name }}"+
						"{{ \"\\n\" }}{{ end }}{{ end }}",
					"-n", namespace,
				)

				podOutput, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred(), "Failed to retrieve controller-manager pod information")
				podNames := utils.GetNonEmptyLines(podOutput)
				g.Expect(podNames).To(HaveLen(1), "expected 1 controller pod running")
				controllerPodName = podNames[0]
				g.Expect(controllerPodName).To(ContainSubstring("controller-manager"))

				// Validate the pod's status
				cmd = exec.Command("kubectl", "get",
					"pods", controllerPodName, "-o", "jsonpath={.status.phase}",
					"-n", namespace,
				)
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("Running"), "Incorrect controller-manager pod status")
			}
			Eventually(verifyControllerUp).Should(Succeed())
		})

		It("should ensure the metrics endpoint is serving metrics", func() {
			By("creating a ClusterRoleBinding for the service account to allow access to metrics")
			cmd := exec.Command("kubectl", "create", "clusterrolebinding", metricsRoleBindingName,
				"--clusterrole=kagenti-operator-metrics-reader",
				fmt.Sprintf("--serviceaccount=%s:%s", namespace, serviceAccountName),
			)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to create ClusterRoleBinding")

			By("validating that the metrics service is available")
			cmd = exec.Command("kubectl", "get", "service", metricsServiceName, "-n", namespace)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Metrics service should exist")

			By("validating that the ServiceMonitor for Prometheus is applied in the namespace")
			cmd = exec.Command("kubectl", "get", "ServiceMonitor", "-n", namespace)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "ServiceMonitor should exist")

			By("getting the service account token")
			token, err := serviceAccountToken()
			Expect(err).NotTo(HaveOccurred())
			Expect(token).NotTo(BeEmpty())

			By("waiting for the metrics endpoint to be ready")
			verifyMetricsEndpointReady := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "endpoints", metricsServiceName, "-n", namespace)
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(ContainSubstring("8443"), "Metrics endpoint is not ready")
			}
			Eventually(verifyMetricsEndpointReady).Should(Succeed())

			By("verifying that the controller manager is serving the metrics server")
			verifyMetricsServerStarted := func(g Gomega) {
				cmd := exec.Command("kubectl", "logs", controllerPodName, "-n", namespace)
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(ContainSubstring("Serving metrics server"),
					"Metrics server not yet started")
			}
			Eventually(verifyMetricsServerStarted).Should(Succeed())

			By("creating the curl-metrics pod to access the metrics endpoint")
			cmd = exec.Command("kubectl", "run", "curl-metrics", "--restart=Never",
				"--namespace", namespace,
				"--image=curlimages/curl:latest",
				"--overrides",
				fmt.Sprintf(`{
					"spec": {
						"containers": [{
							"name": "curl",
							"image": "curlimages/curl:latest",
							"command": ["/bin/sh", "-c"],
							"args": ["curl -v -k -H 'Authorization: Bearer %s' https://%s.%s.svc.cluster.local:8443/metrics"],
							"securityContext": {
								"allowPrivilegeEscalation": false,
								"capabilities": {
									"drop": ["ALL"]
								},
								"runAsNonRoot": true,
								"runAsUser": 1000,
								"seccompProfile": {
									"type": "RuntimeDefault"
								}
							}
						}],
						"serviceAccount": "%s"
					}
				}`, token, metricsServiceName, namespace, serviceAccountName))
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to create curl-metrics pod")

			By("waiting for the curl-metrics pod to complete.")
			verifyCurlUp := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "pods", "curl-metrics",
					"-o", "jsonpath={.status.phase}",
					"-n", namespace)
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("Succeeded"), "curl pod in wrong status")
			}
			Eventually(verifyCurlUp, 5*time.Minute).Should(Succeed())

			By("getting the metrics by checking curl-metrics logs")
			metricsOutput := getMetricsOutput()
			Expect(metricsOutput).To(ContainSubstring(
				"controller_runtime_reconcile_total",
			))
		})

		// +kubebuilder:scaffold:e2e-webhooks-checks
	})
})

// serviceAccountToken returns a token for the specified service account in the given namespace.
// It uses the Kubernetes TokenRequest API to generate a token by directly sending a request
// and parsing the resulting token from the API response.
func serviceAccountToken() (string, error) {
	const tokenRequestRawString = `{
		"apiVersion": "authentication.k8s.io/v1",
		"kind": "TokenRequest"
	}`

	// Temporary file to store the token request
	secretName := fmt.Sprintf("%s-token-request", serviceAccountName)
	tokenRequestFile := filepath.Join("/tmp", secretName)
	err := os.WriteFile(tokenRequestFile, []byte(tokenRequestRawString), os.FileMode(0o644))
	if err != nil {
		return "", err
	}

	var out string
	verifyTokenCreation := func(g Gomega) {
		// Execute kubectl command to create the token
		cmd := exec.Command("kubectl", "create", "--raw", fmt.Sprintf(
			"/api/v1/namespaces/%s/serviceaccounts/%s/token",
			namespace,
			serviceAccountName,
		), "-f", tokenRequestFile)

		output, err := cmd.CombinedOutput()
		g.Expect(err).NotTo(HaveOccurred())

		// Parse the JSON output to extract the token
		var token tokenRequest
		err = json.Unmarshal(output, &token)
		g.Expect(err).NotTo(HaveOccurred())

		out = token.Status.Token
	}
	Eventually(verifyTokenCreation).Should(Succeed())

	return out, err
}

// getMetricsOutput retrieves and returns the logs from the curl pod used to access the metrics endpoint.
func getMetricsOutput() string {
	By("getting the curl-metrics logs")
	cmd := exec.Command("kubectl", "logs", "curl-metrics", "-n", namespace)
	metricsOutput, err := utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred(), "Failed to retrieve logs from curl pod")
	Expect(metricsOutput).To(ContainSubstring("< HTTP/1.1 200 OK"))
	return metricsOutput
}

// tokenRequest is a simplified representation of the Kubernetes TokenRequest API response,
// containing only the token field that we need to extract.
type tokenRequest struct {
	Status struct {
		Token string `json:"token"`
	} `json:"status"`
}

var _ = Describe("AgentCard E2E", Ordered, func() {
	const controllerNamespace = "kagenti-operator-system"
	const controllerDeployment = "kagenti-operator-controller-manager"

	BeforeAll(func() {
		Expect(utils.DeployController(controllerNamespace, projectImage)).To(Succeed(), "Failed to deploy controller")

		By("waiting for controller-manager to be ready")
		Eventually(func(g Gomega) {
			cmd := exec.Command("kubectl", "get", "pods", "-l", "control-plane=controller-manager",
				"-n", controllerNamespace,
				"-o", "go-template={{ range .items }}{{ if not .metadata.deletionTimestamp }}{{ .status.phase }}{{ end }}{{ end }}")
			output, err := utils.Run(cmd)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(output).To(ContainSubstring("Running"))
		}, 2*time.Minute, 2*time.Second).Should(Succeed())

		By("waiting for webhook endpoint to be ready")
		Eventually(func(g Gomega) {
			cmd := exec.Command("kubectl", "get", "endpoints",
				"kagenti-operator-webhook-service", "-n", controllerNamespace,
				"-o", "jsonpath={.subsets[0].addresses[0].ip}")
			output, err := utils.Run(cmd)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(output).NotTo(BeEmpty(), "webhook endpoint not yet populated")
		}, 2*time.Minute, 2*time.Second).Should(Succeed())

		By("creating test namespace with labels")
		cmd := exec.Command("kubectl", "create", "ns", testNamespace)
		_, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred())

		cmd = exec.Command("kubectl", "label", "--overwrite", "ns", testNamespace,
			"agentcard=true",
			"pod-security.kubernetes.io/enforce=restricted")
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred())
	})

	AfterAll(func() {
		By("deleting test namespace")
		cmd := exec.Command("kubectl", "delete", "ns", testNamespace, "--ignore-not-found")
		_, _ = utils.Run(cmd)

		By("cleaning up ClusterSPIFFEID")
		cmd = exec.Command("kubectl", "delete", "clusterspiffeid", "e2e-agentcard-test", "--ignore-not-found")
		_, _ = utils.Run(cmd)

		utils.UndeployController()
	})

	AfterEach(func() {
		if CurrentSpecReport().Failed() {
			// Dump controller logs
			cmd := exec.Command("kubectl", "logs", "-l", "control-plane=controller-manager",
				"-n", controllerNamespace, "--tail=100")
			logs, err := utils.Run(cmd)
			if err == nil {
				_, _ = fmt.Fprintf(GinkgoWriter, "Controller logs:\n%s\n", logs)
			}

			// Dump events in test namespace
			cmd = exec.Command("kubectl", "get", "events", "-n", testNamespace, "--sort-by=.lastTimestamp")
			events, err := utils.Run(cmd)
			if err == nil {
				_, _ = fmt.Fprintf(GinkgoWriter, "Events:\n%s\n", events)
			}

			// Dump AgentCards
			cmd = exec.Command("kubectl", "get", "agentcards", "-n", testNamespace, "-o", "yaml")
			cards, err := utils.Run(cmd)
			if err == nil {
				_, _ = fmt.Fprintf(GinkgoWriter, "AgentCards:\n%s\n", cards)
			}
		}
	})

	SetDefaultEventuallyTimeout(2 * time.Minute)
	SetDefaultEventuallyPollingInterval(time.Second)

	Context("Without signature verification", Ordered, func() {
		It("should reject AgentCard without targetRef", func() {
			By("attempting to apply AgentCard without targetRef")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "apply", "-f", "-", "-n", testNamespace)
				cmd.Stdin = strings.NewReader(invalidAgentCardFixture())
				output, err := cmd.CombinedOutput()
				g.Expect(err).To(HaveOccurred(), "kubectl apply should fail")
				g.Expect(string(output)).To(ContainSubstring("spec.targetRef is required"))
			}, 1*time.Minute, 2*time.Second).Should(Succeed())
		})

		It("should not create AgentCard for workload without protocol label", func() {
			By("deploying noproto-agent without protocol label")
			_, err := utils.KubectlApplyStdin(noProtocolAgentFixture(), testNamespace)
			Expect(err).NotTo(HaveOccurred())

			By("waiting for deployment to be ready")
			Expect(utils.WaitForDeploymentReady("noproto-agent", testNamespace, 2*time.Minute)).To(Succeed())

			By("verifying no AgentCard is created")
			Consistently(func() string {
				cmd := exec.Command("kubectl", "get", "agentcards", "-n", testNamespace,
					"-o", "jsonpath={.items[*].metadata.name}")
				output, _ := utils.Run(cmd)
				return output
			}, 15*time.Second, 5*time.Second).ShouldNot(ContainSubstring("noproto-agent"))
		})

		It("should auto-create AgentCard for labelled workload", func() {
			By("deploying echo-agent with agent and protocol labels")
			_, err := utils.KubectlApplyStdin(echoAgentFixture(), testNamespace)
			Expect(err).NotTo(HaveOccurred())

			By("waiting for deployment to be ready")
			Expect(utils.WaitForDeploymentReady("echo-agent", testNamespace, 2*time.Minute)).To(Succeed())

			cardName := "echo-agent-deployment-card"

			By("verifying AgentCard is auto-created")
			Eventually(func(g Gomega) {
				managedBy, err := utils.KubectlGetJsonpath("agentcard", cardName, testNamespace,
					"{.metadata.labels['app\\.kubernetes\\.io/managed-by']}")
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(managedBy).To(Equal("kagenti-operator"))
			}).Should(Succeed())

			By("verifying targetRef")
			Eventually(func(g Gomega) {
				apiVersion, err := utils.KubectlGetJsonpath("agentcard", cardName, testNamespace,
					"{.spec.targetRef.apiVersion}")
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(apiVersion).To(Equal("apps/v1"))

				kind, err := utils.KubectlGetJsonpath("agentcard", cardName, testNamespace,
					"{.spec.targetRef.kind}")
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(kind).To(Equal("Deployment"))

				name, err := utils.KubectlGetJsonpath("agentcard", cardName, testNamespace,
					"{.spec.targetRef.name}")
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(name).To(Equal("echo-agent"))
			}).Should(Succeed())

			By("verifying protocol and Synced condition")
			Eventually(func(g Gomega) {
				protocol, err := utils.KubectlGetJsonpath("agentcard", cardName, testNamespace,
					"{.status.protocol}")
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(protocol).To(Equal("a2a"))

				syncedStatus, err := utils.KubectlGetJsonpath("agentcard", cardName, testNamespace,
					"{.status.conditions[?(@.type=='Synced')].status}")
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(syncedStatus).To(Equal("True"))
			}).Should(Succeed())
		})

		It("should reject duplicate AgentCard targeting same workload", func() {
			By("attempting to create manual AgentCard targeting echo-agent")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "apply", "-f", "-", "-n", testNamespace)
				cmd.Stdin = strings.NewReader(manualAgentCardFixture())
				output, err := cmd.CombinedOutput()
				g.Expect(err).To(HaveOccurred(), "kubectl apply should fail for duplicate")
				g.Expect(string(output)).To(ContainSubstring("an AgentCard already targets"))
			}, 30*time.Second, 2*time.Second).Should(Succeed())
		})
	})

	Context("With signature verification", Ordered, func() {
		var origArgs []string

		BeforeAll(func() {
			By("patching controller with signature verification flags")
			var err error
			origArgs, err = utils.PatchControllerArgs(controllerNamespace, controllerDeployment, []string{
				"--require-a2a-signature=true",
				"--spire-trust-domain=example.org",
				"--spire-trust-bundle-configmap=spire-bundle",
				"--spire-trust-bundle-configmap-namespace=spire-system",
			})
			Expect(err).NotTo(HaveOccurred())
		})

		AfterAll(func() {
			By("restoring original controller args")
			if origArgs != nil {
				err := utils.RestoreControllerArgs(controllerNamespace, controllerDeployment, origArgs)
				Expect(err).NotTo(HaveOccurred())
			}
		})

		Context("Audit mode", Ordered, func() {
			var auditOrigArgs []string

			BeforeAll(func() {
				By("adding audit mode flag")
				var err error
				auditOrigArgs, err = utils.PatchControllerArgs(controllerNamespace, controllerDeployment, []string{
					"--signature-audit-mode=true",
				})
				Expect(err).NotTo(HaveOccurred())
			})

			AfterAll(func() {
				By("removing audit mode flag")
				if auditOrigArgs != nil {
					err := utils.RestoreControllerArgs(controllerNamespace, controllerDeployment, auditOrigArgs)
					Expect(err).NotTo(HaveOccurred())
				}
			})

			It("should allow sync but report SignatureInvalidAudit", func() {
				By("deploying audit-agent (unsigned)")
				_, err := utils.KubectlApplyStdin(auditAgentFixture(), testNamespace)
				Expect(err).NotTo(HaveOccurred())
				Expect(utils.WaitForDeploymentReady("audit-agent", testNamespace, 2*time.Minute)).To(Succeed())

				By("updating auto-created AgentCard for audit-agent")
				Eventually(func(g Gomega) {
					_, applyErr := utils.KubectlApplyStdin(auditModeAgentCardFixture(), testNamespace)
					g.Expect(applyErr).NotTo(HaveOccurred())
				}, 30*time.Second, 2*time.Second).Should(Succeed())

				cardName := "audit-agent-deployment-card"

				By("verifying Synced=True (audit mode allows sync)")
				Eventually(func(g Gomega) {
					syncedStatus, err := utils.KubectlGetJsonpath("agentcard", cardName, testNamespace,
						"{.status.conditions[?(@.type=='Synced')].status}")
					g.Expect(err).NotTo(HaveOccurred())
					g.Expect(syncedStatus).To(Equal("True"))
				}).Should(Succeed())

				By("verifying SignatureVerified=False with reason SignatureInvalidAudit")
				Eventually(func(g Gomega) {
					sigStatus, err := utils.KubectlGetJsonpath("agentcard", cardName, testNamespace,
						"{.status.conditions[?(@.type=='SignatureVerified')].status}")
					g.Expect(err).NotTo(HaveOccurred())
					g.Expect(sigStatus).To(Equal("False"))

					sigReason, err := utils.KubectlGetJsonpath("agentcard", cardName, testNamespace,
						"{.status.conditions[?(@.type=='SignatureVerified')].reason}")
					g.Expect(err).NotTo(HaveOccurred())
					g.Expect(sigReason).To(Equal("SignatureInvalidAudit"))
				}).Should(Succeed())
			})
		})

		It("should verify signed agent card", func() {
			By("creating ClusterSPIFFEID")
			_, err := utils.KubectlApplyStdin(clusterSPIFFEIDFixture(), "")
			Expect(err).NotTo(HaveOccurred())

			By("deploying signed-agent stack")
			_, err = utils.KubectlApplyStdin(signedAgentFixture(), testNamespace)
			Expect(err).NotTo(HaveOccurred())
			Expect(utils.WaitForDeploymentReady("signed-agent", testNamespace, 3*time.Minute)).To(Succeed())

			By("updating auto-created AgentCard with identityBinding")
			Eventually(func(g Gomega) {
				_, applyErr := utils.KubectlApplyStdin(signedAgentCardFixture(), testNamespace)
				g.Expect(applyErr).NotTo(HaveOccurred())
			}, 30*time.Second, 2*time.Second).Should(Succeed())

			cardName := "signed-agent-deployment-card"

			By("verifying SignatureVerified=True")
			Eventually(func(g Gomega) {
				sigStatus, err := utils.KubectlGetJsonpath("agentcard", cardName, testNamespace,
					"{.status.conditions[?(@.type=='SignatureVerified')].status}")
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(sigStatus).To(Equal("True"))

				sigReason, err := utils.KubectlGetJsonpath("agentcard", cardName, testNamespace,
					"{.status.conditions[?(@.type=='SignatureVerified')].reason}")
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(sigReason).To(Equal("SignatureValid"))
			}, 3*time.Minute).Should(Succeed())

			By("verifying signatureSpiffeId")
			Eventually(func(g Gomega) {
				spiffeId, err := utils.KubectlGetJsonpath("agentcard", cardName, testNamespace,
					"{.status.signatureSpiffeId}")
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(spiffeId).To(Equal("spiffe://example.org/ns/e2e-agentcard-test/sa/signed-agent-sa"))
			}).Should(Succeed())

			By("verifying Synced=True")
			Eventually(func(g Gomega) {
				syncedStatus, err := utils.KubectlGetJsonpath("agentcard", cardName, testNamespace,
					"{.status.conditions[?(@.type=='Synced')].status}")
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(syncedStatus).To(Equal("True"))
			}).Should(Succeed())

			By("verifying Bound=True")
			Eventually(func(g Gomega) {
				boundStatus, err := utils.KubectlGetJsonpath("agentcard", cardName, testNamespace,
					"{.status.conditions[?(@.type=='Bound')].status}")
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(boundStatus).To(Equal("True"))
			}).Should(Succeed())
		})
	})
})
