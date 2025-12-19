# Developer Guide

This guide covers everything you need to know to develop, test, and contribute to the Kagenti Operator.

## Table of Contents

- [Development Setup](#development-setup)
- [Project Structure](#project-structure)
- [Development Workflow](#development-workflow)
- [Testing](#testing)
- [Building and Deployment](#building-and-deployment)
- [Contributing Guidelines](#contributing-guidelines)
- [Debugging](#debugging)

---

## Development Setup

### Prerequisites

- **Go** 1.23 or later
- **Docker** or **Podman** for building images
- **kubectl** configured for a Kubernetes cluster
- **Kind** or similar for local Kubernetes cluster (recommended for testing)
- **make** for build automation

### Install Required Tools

The Makefile will automatically install these tools to `./bin` when needed:

- **controller-gen** — Generates CRDs and RBAC manifests
- **kustomize** — Manages Kubernetes configuration
- **envtest** — Runs tests with a control plane
- **golangci-lint** — Linting tool

### Clone the Repository

```bash
git clone https://github.com/kagenti/kagenti-operator.git
cd kagenti-operator/kagenti-operator
```

### Install Dependencies

```bash
# Download Go dependencies
go mod download

# Install development tools
make controller-gen kustomize setup-envtest golangci-lint
```

---

## Project Structure

```
kagenti-operator/
├── api/
│   └── v1alpha1/              # CRD definitions
│       ├── agent_types.go     # Agent CRD
│       ├── agentbuild_types.go # AgentBuild CRD
│       └── ...
├── cmd/
│   └── main.go                # Operator entry point
├── config/
│   ├── crd/                   # CRD manifests
│   ├── default/               # Default deployment configs
│   ├── manager/               # Operator deployment
│   ├── rbac/                  # RBAC manifests
│   ├── samples/               # Example CRs
│   └── webhook/               # Admission webhook configs
├── internal/
│   ├── controller/            # Reconciliation logic
│   │   ├── agent_controller.go
│   │   └── agentbuild_controller.go
│   ├── builder/               # Tekton pipeline builders
│   ├── distribution/          # K8s distribution detection
│   ├── rbac/                  # RBAC management
│   └── webhook/               # Admission webhook handlers
├── test/
│   ├── e2e/                   # End-to-end tests
│   └── utils/                 # Test utilities
├── charts/                    # Helm charts
├── Dockerfile                 # Operator container image
├── Makefile                   # Build automation
└── go.mod                     # Go dependencies
```

### Key Directories

| Directory | Purpose |
|-----------|---------|
| `api/v1alpha1/` | CRD Go types and schema definitions |
| `internal/controller/` | Core reconciliation logic |
| `internal/builder/` | Tekton pipeline generation |
| `internal/webhook/` | Admission webhook validation/mutation |
| `config/` | Kubernetes manifests and kustomize configs |
| `test/` | Test suites and utilities |

---

## Development Workflow

### 1. Generate Code and Manifests

After modifying CRD types in `api/v1alpha1/`:

```bash
# Generate deepcopy methods, CRDs, and RBAC
make manifests generate

# Format and vet code
make fmt vet
```

### 2. Run Tests

```bash
# Run unit tests
make test

# Run with coverage
make test | grep coverage

# Run linter
make lint

# Fix linting issues automatically
make lint-fix
```

### 3. Local Development

#### Option A: Run Locally (Against Remote Cluster)

```bash
# Install CRDs into the cluster
make install

# Run the operator locally
make run
```

This runs the operator on your machine while connecting to a remote Kubernetes cluster.

#### Option B: Deploy to Cluster

```bash
# Build and load image to Kind
make ko-local-build kind-load-image

# Deploy using Helm
make install-local-chart
```

### 4. Test Your Changes

```bash
# Apply a sample Agent
kubectl apply -f config/samples/weather-agent-image-deployment.yaml

# Check status
kubectl get agents
kubectl describe agent weather-agent

# View logs
kubectl logs -n kagenti-system -l control-plane=controller-manager
```

### 5. Clean Up

```bash
# Uninstall from cluster
helm uninstall kagenti-operator -n kagenti-system

# Remove CRDs
make uninstall
```

---

## Testing

### Unit Tests

Unit tests use [Ginkgo](https://onsi.github.io/ginkgo/) and [Gomega](https://onsi.github.io/gomega/).

```bash
# Run all tests
make test

# Run specific package
go test ./internal/controller/...

# Run with verbose output
go test -v ./...
```

### Writing Unit Tests

Example test structure:

```go
var _ = Describe("Agent Controller", func() {
    Context("When reconciling an Agent", func() {
        It("Should create a Deployment", func() {
            // Setup
            agent := &agentv1alpha1.Agent{
                ObjectMeta: metav1.ObjectMeta{
                    Name:      "test-agent",
                    Namespace: "default",
                },
                Spec: agentv1alpha1.AgentSpec{
                    ImageSource: agentv1alpha1.ImageSource{
                        Image: ptr.To("test:latest"),
                    },
                    // ...
                },
            }
            
            // Execute
            Expect(k8sClient.Create(ctx, agent)).To(Succeed())
            
            // Verify
            deployment := &appsv1.Deployment{}
            Eventually(func() error {
                return k8sClient.Get(ctx, 
                    types.NamespacedName{Name: "test-agent", Namespace: "default"},
                    deployment)
            }).Should(Succeed())
        })
    })
})
```

### End-to-End Tests

E2E tests run against a real cluster:

```bash
# Ensure Kind cluster is running
kind create cluster --name kagenti

# Run e2e tests
make test-e2e
```

### Integration Tests

Test with real Tekton pipelines:

```bash
# Install Tekton Pipelines
kubectl apply -f https://storage.googleapis.com/tekton-releases/pipeline/latest/release.yaml

# Deploy operator
make install-local-chart

# Run integration tests
kubectl apply -f config/samples/weather-agent-build-and-deploy.yaml

# Monitor build
kubectl get agentbuilds -w
kubectl logs -f <pipelinerun-pod>
```

---

## Building and Deployment

### Build the Operator Image

```bash
# Set image name
export IMG=myregistry/kagenti-operator:v1.0.0

# Build Docker image
make docker-build

# Push to registry
make docker-push
```

### Build with Ko (Faster)

[Ko](https://ko.build/) provides faster builds:

```bash
# Build and load to Kind
export KO_DOCKER_REPO=ko.local
export IMAGE_TAG=$(git rev-parse --short HEAD)
make ko-local-build kind-load-image
```

### Deploy to Cluster

#### Using Makefile

```bash
# Install CRDs
make install

# Deploy operator
make deploy IMG=myregistry/kagenti-operator:v1.0.0
```

#### Using Helm

```bash
# Update Helm chart
make chart

# Install from local chart
helm install kagenti-operator ../charts/kagenti-operator \
  --namespace kagenti-system \
  --create-namespace \
  --set controllerManager.container.image.repository=myregistry/kagenti-operator \
  --set controllerManager.container.image.tag=v1.0.0

# Or install from OCI registry
helm install kagenti-operator \
  oci://ghcr.io/kagenti/kagenti-operator/kagenti-operator-chart \
  --version 0.2.0-alpha.19 \
  --namespace kagenti-system \
  --create-namespace
```

### Build Installer YAML

Generate a single YAML file for installation:

```bash
make build-installer

# Output: dist/install.yaml
kubectl apply -f dist/install.yaml
```

---

## Contributing Guidelines

### Code Style

- Follow [Effective Go](https://go.dev/doc/effective_go)
- Use `gofmt` for formatting (run `make fmt`)
- Pass linter checks (run `make lint`)
- Add comments for exported types and functions

### Commit Messages

Follow conventional commits:

```
<type>(<scope>): <subject>

<body>

<footer>
```

Types:
- `feat`: New feature
- `fix`: Bug fix
- `docs`: Documentation changes
- `refactor`: Code refactoring
- `test`: Test additions/changes
- `chore`: Build/tooling changes

Example:
```
feat(controller): add support for custom annotations

Add ability to specify custom annotations on Agent resources
that are propagated to the underlying Deployment and Service.

Closes #123
```

### Pull Request Process

1. **Fork** the repository
2. **Create a branch** for your feature/fix
3. **Make changes** with tests
4. **Run tests** and linter: `make test lint`
5. **Commit changes** with descriptive messages
6. **Push** to your fork
7. **Create PR** against `main` branch
8. **Address review** comments

### Adding New Features

#### Adding a New CRD Field

1. Update type definition in `api/v1alpha1/`:

```go
type AgentSpec struct {
    // NewField description
    // +optional
    NewField string `json:"newField,omitempty"`
    // ... existing fields
}
```

2. Generate manifests:

```bash
make manifests generate
```

3. Update controller logic in `internal/controller/`

4. Add tests

5. Update documentation

#### Adding a New Controller

Use kubebuilder scaffolding:

```bash
# Create new API and controller
kubebuilder create api \
  --group agent \
  --version v1alpha1 \
  --kind NewResource \
  --resource \
  --controller

# Generate manifests
make manifests generate
```

---

## Debugging

### Local Debugging with Delve

```bash
# Install Delve
go install github.com/go-delve/delve/cmd/dlv@latest

# Run with debugger
dlv debug ./cmd/main.go
```

### Remote Debugging in Cluster

1. Build debug image:

```dockerfile
FROM golang:1.23 as builder
WORKDIR /workspace
COPY . .
RUN CGO_ENABLED=0 go build -gcflags="all=-N -l" -o manager cmd/main.go

FROM gcr.io/distroless/static:nonroot
COPY --from=builder /workspace/manager /manager
COPY --from=builder /go/bin/dlv /dlv
ENTRYPOINT ["/dlv", "--listen=:2345", "--headless=true", "--api-version=2", "exec", "/manager"]
```

2. Forward port:

```bash
kubectl port-forward -n kagenti-system deployment/kagenti-operator-controller-manager 2345:2345
```

3. Connect with your IDE's debugger

### Viewing Logs

```bash
# Controller logs
kubectl logs -n kagenti-system -l control-plane=controller-manager -f

# Agent logs
kubectl logs -l app.kubernetes.io/name=my-agent -f

# Tekton build logs
kubectl logs -l tekton.dev/pipelineRun=<pipelinerun-name> -f
```

### Common Issues

#### CRD Version Mismatch

```bash
# Reinstall CRDs
make uninstall install
```

#### Controller Not Reconciling

1. Check controller logs for errors
2. Verify RBAC permissions
3. Check resource finalizers
4. Verify webhook is running (if using admission webhook)

#### Build Failures

1. Check AgentBuild status: `kubectl describe agentbuild <name>`
2. Check PipelineRun status: `kubectl get pipelineruns`
3. View build logs: `kubectl logs -l tekton.dev/pipelineRun=<name>`
4. Verify secrets exist and are correct

---

## Development Tips

### Fast Iteration

```bash
# Terminal 1: Watch for changes and rebuild
make manifests generate && make install && make run

# Terminal 2: Apply test resources
kubectl apply -f config/samples/weather-agent-image-deployment.yaml
```

### Testing Against Multiple Clusters

```bash
# Test on Kind
export CONTEXT=kind-kagenti
make install-local-chart CONTEXT=$CONTEXT

# Test on OpenShift
export CONTEXT=openshift-dev
make install-local-chart CONTEXT=$CONTEXT
```

### Code Generation

Kubebuilder markers control code generation:

```go
// +kubebuilder:validation:Required
// +kubebuilder:validation:MinLength=1
// +kubebuilder:default="default-value"
// +optional
```

See [Kubebuilder Book](https://book.kubebuilder.io/) for more markers.

---

## Useful Commands

```bash
# View all CRDs
kubectl get crds | grep agent.kagenti.dev

# View CRD schema
kubectl explain agent.spec

# Watch resources
kubectl get agents,agentbuilds -A -w

# Describe with events
kubectl describe agent my-agent

# Get raw YAML
kubectl get agent my-agent -o yaml

# Force delete stuck resource
kubectl patch agent my-agent -p '{"metadata":{"finalizers":[]}}' --type=merge
kubectl delete agent my-agent --grace-period=0 --force
```

---

## Additional Resources

- [API Reference](./api-reference.md) — CRD specifications
- [Architecture](./architecture.md) — Design and components
- [Kubebuilder Book](https://book.kubebuilder.io/) — Operator development guide
- [Controller Runtime](https://pkg.go.dev/sigs.k8s.io/controller-runtime) — Framework documentation
- [Tekton Documentation](https://tekton.dev/docs/) — Pipeline system
- [Operator Best Practices](https://sdk.operatorframework.io/docs/best-practices/) — Design patterns

---

## Getting Help

- **GitHub Issues**: [Report bugs or request features](https://github.com/kagenti/kagenti-operator/issues)
- **Discussions**: [Ask questions](https://github.com/kagenti/kagenti-operator/discussions)
- **Contributing**: See [CONTRIBUTING.md](../CONTRIBUTING.md)

---

## License

This project is licensed under the Apache License 2.0. See [LICENSE](../LICENSE) for details.
