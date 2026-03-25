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

package utils

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2" //nolint:golint,revive
)

const (
	prometheusOperatorVersion = "v0.77.1"
	prometheusOperatorURL     = "https://github.com/prometheus-operator/prometheus-operator/" +
		"releases/download/%s/bundle.yaml"

	certmanagerVersion = "v1.16.3"
	certmanagerURLTmpl = "https://github.com/cert-manager/cert-manager/releases/download/%s/cert-manager.yaml"

	spireCRDsChartVersion = "0.5.0"
	spireChartVersion     = "0.28.3"
)

func warnError(err error) {
	_, _ = fmt.Fprintf(GinkgoWriter, "warning: %v\n", err)
}

// DetectContainerTool returns the container tool to use for building images.
// Honors the CONTAINER_TOOL env var. Falls back to auto-detection: docker first, then podman.
func DetectContainerTool() string {
	if tool := os.Getenv("CONTAINER_TOOL"); tool != "" {
		return tool
	}
	if _, err := exec.LookPath("docker"); err == nil {
		return "docker"
	}
	if _, err := exec.LookPath("podman"); err == nil {
		return "podman"
	}
	return "docker"
}

// Run executes the provided command within this context
func Run(cmd *exec.Cmd) (string, error) {
	dir, _ := GetProjectDir()
	cmd.Dir = dir

	if err := os.Chdir(cmd.Dir); err != nil {
		_, _ = fmt.Fprintf(GinkgoWriter, "chdir dir: %s\n", err)
	}

	cmd.Env = append(os.Environ(), "GO111MODULE=on")
	command := strings.Join(cmd.Args, " ")
	_, _ = fmt.Fprintf(GinkgoWriter, "running: %s\n", command)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return string(output), fmt.Errorf("%s failed with error: (%v) %s", command, err, string(output))
	}

	return string(output), nil
}

// InstallPrometheusOperator installs the prometheus Operator to be used to export the enabled metrics.
func InstallPrometheusOperator() error {
	url := fmt.Sprintf(prometheusOperatorURL, prometheusOperatorVersion)
	cmd := exec.Command("kubectl", "create", "-f", url)
	_, err := Run(cmd)
	return err
}

// UninstallPrometheusOperator uninstalls the prometheus
func UninstallPrometheusOperator() {
	url := fmt.Sprintf(prometheusOperatorURL, prometheusOperatorVersion)
	cmd := exec.Command("kubectl", "delete", "-f", url)
	if _, err := Run(cmd); err != nil {
		warnError(err)
	}
}

// IsPrometheusCRDsInstalled checks if any Prometheus CRDs are installed
// by verifying the existence of key CRDs related to Prometheus.
func IsPrometheusCRDsInstalled() bool {
	// List of common Prometheus CRDs
	prometheusCRDs := []string{
		"prometheuses.monitoring.coreos.com",
		"prometheusrules.monitoring.coreos.com",
		"prometheusagents.monitoring.coreos.com",
	}

	cmd := exec.Command("kubectl", "get", "crds", "-o", "custom-columns=NAME:.metadata.name")
	output, err := Run(cmd)
	if err != nil {
		return false
	}
	crdList := GetNonEmptyLines(output)
	for _, crd := range prometheusCRDs {
		for _, line := range crdList {
			if strings.Contains(line, crd) {
				return true
			}
		}
	}

	return false
}

// UninstallCertManager uninstalls the cert manager
func UninstallCertManager() {
	url := fmt.Sprintf(certmanagerURLTmpl, certmanagerVersion)
	cmd := exec.Command("kubectl", "delete", "-f", url)
	if _, err := Run(cmd); err != nil {
		warnError(err)
	}
}

// InstallCertManager installs the cert manager bundle.
func InstallCertManager() error {
	url := fmt.Sprintf(certmanagerURLTmpl, certmanagerVersion)
	cmd := exec.Command("kubectl", "apply", "-f", url)
	if _, err := Run(cmd); err != nil {
		return err
	}
	// Wait for cert-manager-webhook to be ready, which can take time if cert-manager
	// was re-installed after uninstalling on a cluster.
	cmd = exec.Command("kubectl", "wait", "deployment.apps/cert-manager-webhook",
		"--for", "condition=Available",
		"--namespace", "cert-manager",
		"--timeout", "5m",
	)

	_, err := Run(cmd)
	return err
}

// IsCertManagerCRDsInstalled checks if any Cert Manager CRDs are installed
// by verifying the existence of key CRDs related to Cert Manager.
func IsCertManagerCRDsInstalled() bool {
	// List of common Cert Manager CRDs
	certManagerCRDs := []string{
		"certificates.cert-manager.io",
		"issuers.cert-manager.io",
		"clusterissuers.cert-manager.io",
		"certificaterequests.cert-manager.io",
		"orders.acme.cert-manager.io",
		"challenges.acme.cert-manager.io",
	}

	// Execute the kubectl command to get all CRDs
	cmd := exec.Command("kubectl", "get", "crds")
	output, err := Run(cmd)
	if err != nil {
		return false
	}

	// Check if any of the Cert Manager CRDs are present
	crdList := GetNonEmptyLines(output)
	for _, crd := range certManagerCRDs {
		for _, line := range crdList {
			if strings.Contains(line, crd) {
				return true
			}
		}
	}

	return false
}

// LoadImageToKindClusterWithName loads a local container image to the kind cluster.
// Falls back to podman save + kind load image-archive when kind load docker-image fails.
func LoadImageToKindClusterWithName(name string) error {
	cluster := "kind"
	if v, ok := os.LookupEnv("KIND_CLUSTER"); ok {
		cluster = v
	}
	kindOptions := []string{"load", "docker-image", name, "--name", cluster}
	cmd := exec.Command("kind", kindOptions...)
	_, err := Run(cmd)
	if err == nil {
		return nil
	}

	// Fallback for podman: save image to archive, then load archive into Kind
	_, _ = fmt.Fprintf(GinkgoWriter, "kind load docker-image failed, trying podman save fallback...\n")
	archivePath := fmt.Sprintf("%s/kind-image-%d.tar", os.TempDir(), time.Now().UnixNano())
	defer os.Remove(archivePath)

	cmd = exec.Command("podman", "save", name, "-o", archivePath)
	if _, saveErr := Run(cmd); saveErr != nil {
		return fmt.Errorf("kind load docker-image failed (%w) and podman save fallback also failed: %v", err, saveErr)
	}

	cmd = exec.Command("kind", "load", "image-archive", archivePath, "--name", cluster)
	_, archiveErr := Run(cmd)
	return archiveErr
}

// GetNonEmptyLines converts given command output string into individual objects
// according to line breakers, and ignores the empty elements in it.
func GetNonEmptyLines(output string) []string {
	var res []string
	elements := strings.Split(output, "\n")
	for _, element := range elements {
		if element != "" {
			res = append(res, element)
		}
	}

	return res
}

// GetProjectDir will return the directory where the project is
func GetProjectDir() (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return wd, err
	}
	wd = strings.Replace(wd, "/test/e2e", "", -1)
	return wd, nil
}

// InstallSpire installs SPIRE via Helm with the given trust domain.
// The SPIFFE hardened charts require CRDs to be installed separately first.
func InstallSpire(trustDomain string) error {
	By("adding SPIFFE Helm repo")
	cmd := exec.Command("helm", "repo", "add", "spiffe",
		"https://spiffe.github.io/helm-charts-hardened/")
	if _, err := Run(cmd); err != nil {
		// Ignore "already exists" errors
		if !strings.Contains(err.Error(), "already exists") {
			return err
		}
	}

	cmd = exec.Command("helm", "repo", "update")
	if _, err := Run(cmd); err != nil {
		return err
	}

	By("installing SPIRE CRDs")
	cmd = exec.Command("helm", "install", "spire-crds", "spiffe/spire-crds",
		"--version", spireCRDsChartVersion,
		"-n", "spire-system",
		"--create-namespace",
		"--wait",
		"--timeout", "2m",
	)
	if _, err := Run(cmd); err != nil {
		return err
	}

	By("installing SPIRE Helm chart")
	cmd = exec.Command("helm", "install", "spire", "spiffe/spire",
		"--version", spireChartVersion,
		"-n", "spire-system",
		fmt.Sprintf("--set=global.spire.trustDomain=%s", trustDomain),
		"--wait",
		"--timeout", "5m",
	)
	if _, err := Run(cmd); err != nil {
		return err
	}

	By("labeling spire-bundle configmap for controller cache visibility")
	cmd = exec.Command("kubectl", "label", "--overwrite", "configmap", "spire-bundle",
		"-n", "spire-system", "kagenti.io/defaults=true")
	_, err := Run(cmd)
	return err
}

// UninstallSpire removes the SPIRE Helm releases.
func UninstallSpire() {
	By("uninstalling SPIRE Helm release")
	cmd := exec.Command("helm", "uninstall", "spire", "-n", "spire-system")
	if _, err := Run(cmd); err != nil {
		warnError(err)
	}

	By("uninstalling SPIRE CRDs Helm release")
	cmd = exec.Command("helm", "uninstall", "spire-crds", "-n", "spire-system")
	if _, err := Run(cmd); err != nil {
		warnError(err)
	}

	By("deleting spire-system namespace")
	cmd = exec.Command("kubectl", "delete", "ns", "spire-system", "--ignore-not-found")
	if _, err := Run(cmd); err != nil {
		warnError(err)
	}
}

// IsSpireCRDsInstalled checks if ClusterSPIFFEID CRD exists.
func IsSpireCRDsInstalled() bool {
	cmd := exec.Command("kubectl", "get", "crd", "clusterspiffeids.spire.spiffe.io")
	_, err := Run(cmd)
	return err == nil
}

// WaitForSpireReady waits for SPIRE server and agent pods to be ready.
func WaitForSpireReady(timeout time.Duration) error {
	By("waiting for SPIRE pods to be ready")
	cmd := exec.Command("kubectl", "wait", "pods",
		"--all",
		"-n", "spire-system",
		"--for=condition=Ready",
		fmt.Sprintf("--timeout=%s", timeout),
	)
	_, err := Run(cmd)
	return err
}

// KubectlApplyStdin applies YAML from stdin to a namespace.
func KubectlApplyStdin(yaml, namespace string) (string, error) {
	args := []string{"apply", "-f", "-"}
	if namespace != "" {
		args = append(args, "-n", namespace)
	}
	cmd := exec.Command("kubectl", args...)

	dir, _ := GetProjectDir()
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GO111MODULE=on")
	cmd.Stdin = strings.NewReader(yaml)

	command := strings.Join(cmd.Args, " ")
	_, _ = fmt.Fprintf(GinkgoWriter, "running: %s\n", command)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return string(output), fmt.Errorf("%s failed with error: (%v) %s", command, err, string(output))
	}
	return string(output), nil
}

// KubectlGetJsonpath gets a value using jsonpath from a resource.
func KubectlGetJsonpath(resource, name, namespace, jsonpath string) (string, error) {
	cmd := exec.Command("kubectl", "get", resource, name,
		"-n", namespace,
		"-o", fmt.Sprintf("jsonpath=%s", jsonpath),
	)
	output, err := Run(cmd)
	return strings.TrimSpace(output), err
}

// WaitForDeploymentReady waits for a deployment to have Available condition.
func WaitForDeploymentReady(name, namespace string, timeout time.Duration) error {
	cmd := exec.Command("kubectl", "wait",
		fmt.Sprintf("deployment/%s", name),
		"-n", namespace,
		"--for=condition=Available",
		fmt.Sprintf("--timeout=%s", timeout),
	)
	_, err := Run(cmd)
	return err
}

// WaitForRollout waits for a deployment rollout to complete.
func WaitForRollout(name, namespace string, timeout time.Duration) error {
	cmd := exec.Command("kubectl", "rollout", "status",
		fmt.Sprintf("deployment/%s", name),
		"-n", namespace,
		fmt.Sprintf("--timeout=%s", timeout),
	)
	_, err := Run(cmd)
	return err
}

// PatchControllerArgs patches controller deployment args with additional flags
// and returns the original args for later restoration.
func PatchControllerArgs(namespace, deploy string, addArgs []string) (origArgs []string, err error) {
	By("getting current controller args")
	cmd := exec.Command("kubectl", "get", "deployment", deploy,
		"-n", namespace,
		"-o", "jsonpath={.spec.template.spec.containers[0].args}",
	)
	output, err := Run(cmd)
	if err != nil {
		return nil, fmt.Errorf("failed to get current args: %w", err)
	}

	output = strings.TrimSpace(output)
	if output != "" {
		if err := json.Unmarshal([]byte(output), &origArgs); err != nil {
			return nil, fmt.Errorf("failed to parse current args %q: %w", output, err)
		}
	}

	By(fmt.Sprintf("patching controller with args: %v", addArgs))
	newArgs := append(origArgs, addArgs...)
	argsJSON, jsonErr := json.Marshal(newArgs)
	if jsonErr != nil {
		return origArgs, fmt.Errorf("failed to marshal new args: %w", jsonErr)
	}
	patchJSON := fmt.Sprintf(`[{"op":"replace","path":"/spec/template/spec/containers/0/args","value":%s}]`, string(argsJSON))
	cmd = exec.Command("kubectl", "patch", "deployment", deploy,
		"-n", namespace,
		"--type=json",
		fmt.Sprintf("-p=%s", patchJSON),
	)
	if _, patchErr := Run(cmd); patchErr != nil {
		return origArgs, fmt.Errorf("failed to patch args: %w", patchErr)
	}

	By("waiting for controller rollout after patch")
	if err := WaitForRollout(deploy, namespace, 2*time.Minute); err != nil {
		return origArgs, fmt.Errorf("rollout failed after patch: %w", err)
	}

	return origArgs, nil
}

// RestoreControllerArgs restores controller deployment to original args.
func RestoreControllerArgs(namespace, deploy string, origArgs []string) error {
	By(fmt.Sprintf("restoring controller args to: %v", origArgs))

	argsJSON, err := json.Marshal(origArgs)
	if err != nil {
		return fmt.Errorf("failed to marshal original args: %w", err)
	}

	patchJSON := fmt.Sprintf(`[{"op":"replace","path":"/spec/template/spec/containers/0/args","value":%s}]`, string(argsJSON))
	cmd := exec.Command("kubectl", "patch", "deployment", deploy,
		"-n", namespace,
		"--type=json",
		fmt.Sprintf("-p=%s", patchJSON),
	)
	if _, err := Run(cmd); err != nil {
		return fmt.Errorf("failed to restore args: %w", err)
	}

	By("waiting for controller rollout after restore")
	if err := WaitForRollout(deploy, namespace, 2*time.Minute); err != nil {
		return fmt.Errorf("rollout failed after restore: %w", err)
	}

	return nil
}

// DeployController installs CRDs and deploys the controller-manager.
func DeployController(namespace, img string) error {
	By("creating manager namespace")
	cmd := exec.Command("kubectl", "create", "ns", namespace)
	_, _ = Run(cmd) // ignore if already exists

	By("labeling the namespace to enforce the restricted security policy")
	cmd = exec.Command("kubectl", "label", "--overwrite", "ns", namespace,
		"pod-security.kubernetes.io/enforce=restricted")
	if _, err := Run(cmd); err != nil {
		return err
	}

	By("installing CRDs")
	cmd = exec.Command("make", "install")
	if _, err := Run(cmd); err != nil {
		return err
	}

	By("deploying the controller-manager")
	cmd = exec.Command("make", "deploy", fmt.Sprintf("IMG=%s", img))
	_, err := Run(cmd)
	return err
}

// UndeployController undeploys the controller-manager and uninstalls CRDs.
func UndeployController() {
	By("undeploying the controller-manager")
	cmd := exec.Command("make", "undeploy")
	if _, err := Run(cmd); err != nil {
		warnError(err)
	}

	By("uninstalling CRDs")
	cmd = exec.Command("make", "uninstall")
	if _, err := Run(cmd); err != nil {
		warnError(err)
	}
}

// UncommentCode searches for target in the file and remove the comment prefix
// of the target content. The target content may span multiple lines.
func UncommentCode(filename, target, prefix string) error {
	// false positive
	// nolint:gosec
	content, err := os.ReadFile(filename)
	if err != nil {
		return err
	}
	strContent := string(content)

	idx := strings.Index(strContent, target)
	if idx < 0 {
		return fmt.Errorf("unable to find the code %s to be uncomment", target)
	}

	out := new(bytes.Buffer)
	_, err = out.Write(content[:idx])
	if err != nil {
		return err
	}

	scanner := bufio.NewScanner(bytes.NewBufferString(target))
	if !scanner.Scan() {
		return nil
	}
	for {
		_, err := out.WriteString(strings.TrimPrefix(scanner.Text(), prefix))
		if err != nil {
			return err
		}
		// Avoid writing a newline in case the previous line was the last in target.
		if !scanner.Scan() {
			break
		}
		if _, err := out.WriteString("\n"); err != nil {
			return err
		}
	}

	_, err = out.Write(content[idx+len(target):])
	if err != nil {
		return err
	}
	// false positive
	// nolint:gosec
	return os.WriteFile(filename, out.Bytes(), 0644)
}
