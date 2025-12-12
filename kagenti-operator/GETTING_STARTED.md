# Getting Started with Kagenti Operator

> **Note**: This guide assumes you have already installed the Kagenti platform using the [Kagenti installer](https://github.com/kagenti/kagenti/blob/main/deployments/ansible/README.md).

## Prerequisites

The examples in this guide assume you are running Ollama locally with a specific model loaded. Before proceeding, ensure you have:

- **Ollama installed and running** on your local machine
- **Model loaded**: `llama3.2:3b-instruct-fp16`
- **Ollama accessible** at `http://host.docker.internal:11434/v1` from within the Kubernetes cluster

The agent deployments in this guide are configured with the following environment variables to connect to your local Ollama instance:
```yaml
- name: LLM_API_BASE
  value: http://host.docker.internal:11434/v1
- name: LLM_API_KEY
  value: dummy
- name: LLM_MODEL
  value: llama3.2:3b-instruct-fp16
```

---

## Intent

This guide provides step-by-step instructions for deploying and testing AI agents with the Kagenti platform. By following this guide, you will learn how to deploy agents, build custom agent images, and integrate MCP (Model Context Protocol) servers to extend agent capabilities.

## Scenario

In this guide, you will:

1. Deploy a weather agent that can answer weather-related queries
2. Optionally build a custom agent image from GitHub source code
3. Deploy an MCP server that provides tools and resources to the agent
4. Test the end-to-end flow by sending a weather query ("What is the weather in New York?") to the agent, which will communicate with the MCP server and return a response

This scenario demonstrates the complete lifecycle of an AI agent deployment on the Kagenti platform, from initial deployment to integration with external tools via MCP servers.

---
## Overview

### Kagenti Operator
The Kagenti Operator manages AI Agent deployments through two Custom Resources:

- **Agent**: Deploys an AI agent (required)
- **AgentBuild**: Builds container images from GitHub source (optional - only needed for build-from-source)

### Stacklok ToolHive Operator

The Stacklok ToolHive Operator is deployed as part of the Kagenti platform installation and manages:

- **MCPServer**: Deploys MCP servers that provide tools and resources to agents

---

## Deploy an Agent from Existing Image

### Quick Example Deployment

```yaml
kubectl apply -f - <<EOF
apiVersion: agent.kagenti.dev/v1alpha1
kind: Agent
metadata:
  name: weather-agent
  namespace: team1
  labels:
    kagenti.io/framework: LangGraph
    kagenti.io/protocol: a2a
    kagenti.io/type: agent
    kagenti-enabled: "true"
    app.kubernetes.io/name: weather-agent
spec:
  imageSource:
    image: "ghcr.io/kagenti/agent-examples/weather_service:v0.0.1-alpha.3"
  servicePorts:
    - port: 8000
      targetPort: 8000
      protocol: TCP
      name: http  
  podTemplateSpec:
    spec:
      containers:
      - name: agent
        ports:
        - containerPort: 8000
        imagePullPolicy: Always
        env:
        - name: PORT
          value: "8000"
        - name: UV_CACHE_DIR
          value: /app/.cache/uv
        - name: MCP_URL
          value: http://mcp-weather-tool-proxy.team1.svc.cluster.local:8000/mcp
        - name: LLM_API_BASE
          value: http://host.docker.internal:11434/v1
        - name: LLM_API_KEY
          value: dummy
        - name: LLM_MODEL
          value: llama3.2:3b-instruct-fp16     
EOF

```

**Check Status**:
```bash

# Check status
kubectl get agent weather-agent -n team1

# View logs
kubectl logs -l app.kubernetes.io/name=weather-agent -n team1
```
## Next Steps: Choose Your Path

After deploying the Agent from an existing image, you have two options:

### Option 1: Skip to MCP Server Deployment (Recommended for Quick Testing)
If you want to proceed directly with testing the agent with an MCP server, **skip the next section** and go directly to [Deploy an MCP Server](#deploy-an-mcp-server).

### Option 2: Build from Source (Optional)
If you want to build a custom agent image from GitHub source code, continue with the next section. **Note:** This involves building from source and deploying a new instance of an Agent that references the AgentBuild to extract the built image. If you choose this path, you must **delete the previous Agent instance** first:
```bash
kubectl delete agent weather-agent -n team1
```

---

---

## Build and Deploy from GitHub Source

### Step 1: Create AgentBuild

This builds a container image weather-service agent from kagenti GitHub repository.

```yaml
kubectl apply -f - <<EOF
apiVersion: agent.kagenti.dev/v1alpha1
kind: AgentBuild
metadata:
  name: weather-agent-build
  namespace: team1
spec:
  pipeline:
     namespace: kagenti-system
     parameters:
      - name: PYTHON_VERSION
      value: "3.13"
  source:
    sourceRepository: "github.com/kagenti/agent-examples"
    sourceRevision: "main"
    sourceSubfolder: a2a/weather_service
    # For private repos (optional):
    # sourceCredentials:
    #   name: github-token-secret
  
  buildOutput:
    image: "weather-service"
    imageTag: "v1.0.0"
    imageRegistry: "registry.cr-system.svc.cluster.local:5000"
EOF
```

**monitor**:
```bash

# Watch build progress
kubectl get agentbuild weather-agent-build -n team1 -w

# Check when phase becomes "Succeeded"
```

### Step 2: Deploy Agent Using Built Image

```yaml
kubectl apply -f - <<EOF
apiVersion: agent.kagenti.dev/v1alpha1
kind: Agent
metadata:
  name: weather-agent
  namespace  team1
  labels:
    kagenti.io/framework: LangGraph
    kagenti.io/protocol: a2a
    kagenti.io/type: agent
    kagenti-enabled: "true"
    app.kubernetes.io/name: weather-agent
spec:
  imageSource:
    buildRef:
      name: weather-agent-build  # References the AgentBuild above
  servicePorts:
    - port: 8000
      targetPort: 8000
      protocol: TCP
      name: http    
  podTemplateSpec:
    spec:
      containers:
      - name: agent
        ports:
        - containerPort: 8000
        imagePullPolicy: Always
        env:
        - name: PORT
          value: "8000"
        - name: UV_CACHE_DIR
          value: /app/.cache/uv
        - name: MCP_URL
          value: http://mcp-weather-tool-proxy.team1.svc.cluster.local:8000/mcp
        - name: LLM_API_BASE
          value: http://host.docker.internal:11434/v1
        - name: LLM_API_KEY
          value: dummy
        - name: LLM_MODEL
          value: llama3.2:3b-instruct-fp16     

EOF
```

**Monitor**:
```bash

# Verify it's using the built image
kubectl get agent weather-agent-build -n team1 -o yaml | grep builtImage
```
---
## Checking Status

### Agent Status

```bash
# List agents
kubectl get agents -n team1

# Detailed status
kubectl describe agent weather-agent -n team1

# Check deployment phase
kubectl get agent weather-agent -n team1 -o jsonpath='{.status.deploymentStatus.phase}'
# Should show: Ready
```

### AgentBuild Status

```bash
# List builds
kubectl get agentbuilds -n team1

# Check build phase
kubectl get agentbuild weather-agent-build -n team1 -o jsonpath='{.status.phase}'
# Phases: Pending → Building → Succeeded (or Failed)

# View build logs
PIPELINE=$(kubectl get agentbuild weather-agent-build -n team1 -o jsonpath='{.status.pipelineRunName}')
kubectl logs -f $(kubectl get pods -n team1 -l tekton.dev/pipelineRun=$PIPELINE -o name | head -1)
```

---
## Deploy an MCP Server

> **Note**: The MCPServer Custom Resource is managed by the Stacklok ToolHive Operator, which is deployed as part of the Kagenti platform installation. This guide shows how to deploy an MCPServer to enable end-to-end testing.

MCP (Model Context Protocol) servers provide tools and resources that your agents can use. Deploy an MCPServer after your agent is running.

### Basic MCP Server Deployment
```yaml
kubectl apply -f - <<EOF
apiVersion: toolhive.stacklok.dev/v1alpha1
kind: MCPServer
metadata:
  name: weather-tool
  namespace: team1
  labels:
    kagenti.io/framework: Python
    kagenti.io/protocol: streamable_http
    kagenti.io/type: tool  
    toolhive-basename: weather-tool
spec:
  image: "ghcr.io/kagenti/agent-examples/weather_tool:v0.0.1-alpha.3"
  transport: streamable-http
  port: 8000
  targetPort: 8000
  proxyPort: 8000
  podTemplateSpec:
    spec:
      serviceAccountName: weather-tool
      securityContext:
        runAsNonRoot: true
        seccompProfile:
          type: RuntimeDefault
      volumes:
        - name: cache
          emptyDir: {}
        - name: tmp-dir
          emptyDir: {}
      containers:
        - name: mcp
          securityContext:
            allowPrivilegeEscalation: false
            capabilities:
              drop: ["ALL"]
            runAsUser: 1000
          env:
            - name: PORT
              value: "8000"
            - name: UV_CACHE_DIR
              value: /app/.cache/uv
          volumeMounts:
            - name: cache
              mountPath: /app/.cache
              readOnly: false
            - name: tmp-dir
              mountPath: /tmp
              readOnly: false
EOF
```
---
**Check Status**:
```bash

# List MCP servers
kubectl get mcpservers.toolhive.stacklok.dev -n team1

# Check detailed status
kubectl describe mcpservers.toolhive.stacklok.dev weather-tool -n team1

# View logs
kubectl logs -l toolhive-basename=weather-tool -n team1
```


---
## End-to-End Testing: Send a Prompt to Agent

Once your Agent and MCPServer are deployed and running, you can test the end-to-end flow by sending a prompt to the agent using JSON-RPC.

> **Note**: The examples below use a temporary curl pod running inside the cluster. This approach allows you to directly access the agent service using its internal Kubernetes service DNS name (e.g., `http://mcp-weather-tool-proxy.team1.svc.cluster.local:8000`) without the need to set up port-forwarding from your local machine.

The reason this DNS name works is that the ToolHive Operator automatically creates a Kubernetes Service for each MCPServer using a specific naming convention:

mcp-[server-name]-proxy

For example, if your MCPServer resource is named weather-tool, the operator will generate a service named:

mcp-weather-tool-proxy

Kubernetes then exposes this service internally via its built-in DNS system, producing a fully qualified service hostname in the form:

mcp-[server-name]-proxy.[namespace].svc.cluster.local
### Test Weather Query

This example sends a weather query for New York to the weather-agent, which will communicate with the MCPServer to retrieve the weather information.
```bash
kubectl run curl-pod -i --tty --rm \
  --image=curlimages/curl:8.1.2 -n team1 -- \
  curl -sS -X POST http://weather-agent.team1.svc.cluster.local:8000/ \
    -H "Content-Type: application/json" \
    -d '{"jsonrpc":"2.0","id":"76CD6BA3-16AA-4CED-8E0E-19156B8C5886","method":"message/send","params":{"message":{"role":"user","parts":[{"kind":"text","text":"What is the weather in NY?"}],"messageId":"DF05857B-98B7-4414-BD63-19E16E684E39"}}}'
```

> **Tip**: If you don't see a command prompt after running the command, try pressing enter. The pod will automatically be deleted after the curl command completes.

**Expected Response**

The agent should return a JSON-RPC response similar to:
```json
{
  "id": "76CD6BA3-16AA-4CED-8E0E-19156B8C5886",
  "jsonrpc": "2.0",
  "result": {
    "artifacts": [
      {
        "artifactId": "5e6ab98e-95a8-47d4-bfaf-a35ce9bd9235",
        "parts": [
          {
            "kind": "text",
            "text": "The current weather in NY is mostly cloudy with a temperature of 46.9°F (8.3°C). There is a moderate breeze coming from the northwest at 9.7 mph (15.6 km/h). It's currently nighttime."
          }
        ]
      }
    ],
    "contextId": "4991dce0-a893-4a65-8599-0621b46984dd",
    "history": [
      {
        "contextId": "4991dce0-a893-4a65-8599-0621b46984dd",
        "kind": "message",
        "messageId": "DF05857B-98B7-4414-BD63-19E16E684E39",
        "parts": [
          {
            "kind": "text",
            "text": "What is the weather in NY?"
          }
        ],
        "role": "user",
        "taskId": "b08ce1b8-59b6-4e96-b375-b3a0a4889b65"
      }
    ],
    "id": "b08ce1b8-59b6-4e96-b375-b3a0a4889b65",
    "kind": "task",
    "status": {
      "state": "completed",
      "timestamp": "2025-12-12T15:57:31.382161+00:00"
    }
  }
}
```

The response shows:
- **artifacts**: Contains the weather information returned by the agent
- **history**: Shows the conversation history, including the agent's interaction with the MCP server tools
- **status**: Indicates the task completed successfully
---

**Monitor the Interaction**

You can watch the logs to see the agent communicating with the MCP server:
```bash
# Watch agent logs
kubectl logs -f -l app.kubernetes.io/name=weather-agent -n team1

# Watch MCP server logs (in another terminal)
kubectl logs -f -l toolhive-basename=weather-tool -n team1
```

---

## Checking Status

### Agent Status
```bash
# List agents
kubectl get agents -n team1

# Detailed status
kubectl describe agent weather-agent -n team1

# Check deployment phase
kubectl get agent weather-agent -n team1 -o jsonpath='{.status.deploymentStatus.phase}'
# Should show: Ready
```

### AgentBuild Status
```bash
# List builds
kubectl get agentbuilds -n team1

# Get the full name of the build (it will have a generated suffix)
AGENTBUILD_NAME=$(kubectl get agentbuilds -n team1 \
  -l agent=weather-agent \
  -o jsonpath='{.items[0].metadata.name}')

# Check build phase
kubectl get agentbuild $AGENTBUILD_NAME -n team1 -o jsonpath='{.status.phase}'
# Phases: Pending → Building → Succeeded (or Failed)

# Check the built image
kubectl get agentbuild $AGENTBUILD_NAME -n team1 -o jsonpath='{.status.builtImage}'

# View detailed status including conditions
kubectl get agentbuild $AGENTBUILD_NAME -n team1 -o yaml | grep -A 20 status

# Get the PipelineRun name
PIPELINE=$(kubectl get agentbuild $AGENTBUILD_NAME -n team1 -o jsonpath='{.status.pipelineRunName}')
echo "PipelineRun: $PIPELINE"

# Check if build is still running
kubectl get pipelinerun $PIPELINE -n team1

# View build logs (only available while build is in progress)
# Note: Pods are cleaned up after successful completion
kubectl get pods -n team1 -l tekton.dev/pipelineRun=$PIPELINE

# If pods exist (build in progress), view logs:
PIPELINE_POD=$(kubectl get pods -n team1 -l tekton.dev/pipelineRun=$PIPELINE -o jsonpath='{.items[0].metadata.name}')
if [ -n "$PIPELINE_POD" ]; then
  kubectl logs -f $PIPELINE_POD -n team1 --all-containers=true
else
  echo "Build completed - pods have been cleaned up. Check status with: kubectl get agentbuild $AGENTBUILD_NAME -n team1 -o yaml"
fi

# Alternative: Watch build progress in real-time (run this before or during build)
kubectl get agentbuild $AGENTBUILD_NAME -n team1 -w
```

### MCPServer Status
```bash
# List MCP servers
kubectl get mcpservers.toolhive.stacklok.dev -n team1

# Detailed status
kubectl describe mcpservers.toolhive.stacklok.dev weather-tool -n team1

# Check if pods are running
kubectl get pods -n team1 -l toolhive-basename=weather-tool

# View logs
kubectl logs -l toolhive-basename=weather-tool -n team1 -f
```

---

## Troubleshooting

### Agent Not Starting
```bash
# Check if using BuildRef 
kubectl get agent weather-agent -n team1 -o yaml | grep buildRef

# If yes, verify build succeeded
kubectl get agentbuild  -n team1 -o jsonpath='{.status.phase}'
# Should show: Succeeded

# Check events
kubectl get events -n team1 --field-selector involvedObject.name=weather-agent
```

### Build Failing
```bash
# Check build status
kubectl get agentbuild weather-agent-build -n team1 -o yaml | grep -A10 status

# View build logs
kubectl get pods -n team1 -l app.kubernetes.io/component=weather-agent-build
kubectl logs  -n team1
```

### MCP Server Not Starting
```bash
# Check MCP server status
kubectl get mcpservers.toolhive.stacklok.dev weather-tool -n team1 -o yaml

# Check pod status
kubectl get pods -n team1 -l toolhive-basename=weather-tool

# Check events
kubectl get events -n team1 --field-selector involvedObject.name=weather-tool

# Verify service account exists
kubectl get serviceaccount weather-tool-sa -n team1

# Check for image pull issues
kubectl describe pod -n team1 -l toolhive-basename=weather-tool
```

### Agent Not Responding to Requests
```bash
# Verify agent service is accessible
kubectl get svc weather-agent -n team1

# Test connectivity from within the cluster using a temporary curl pod
kubectl run test-curl --image=curlimages/curl:latest --rm -i --restart=Never -n team1 -- \
  curl -v http://weather-agent.team1.svc.cluster.local:8000/health

# Check if agent can reach MCP server
# Note: The agent container may not have curl installed, so we'll use a debug pod in the same namespace
kubectl run curl-debug --image=curlimages/curl:8.1.2 --rm -i --tty -n team1 -- \
  curl -v http://mcp-weather-tool-proxy.team1.svc.cluster.local:8000/health

```

### Ollama Connection Issues
```bash
# Verify Ollama is running and accessible from your local machine
curl http://localhost:11434/v1/models

# Check agent logs for LLM-related errors
kubectl logs -l app.kubernetes.io/name=weather-agent -n team1 | grep -i "llm\|model\|ollama"

# Check for connection errors in agent logs
kubectl logs -l app.kubernetes.io/name=weather-agent -n team1 --tail=100

# Test if the agent pod can reach Ollama
kubectl run curl-test --image=curlimages/curl:8.1.2 --rm -i --tty -n team1 -- \
  curl -v http://host.docker.internal:11434/v1/models
```

---

## Secrets (For Private Repos/Registries)

### GitHub Token
```bash
kubectl create secret generic github-token-secret \
  --from-literal=username=myusername \
  --from-literal=password=ghp_mytoken \
  -n team1
```

### Registry Credentials
```bash
kubectl create secret docker-registry ghcr-secret \
  --docker-server=ghcr.io \
  --docker-username=myusername \
  --docker-password=ghp_mytoken \
  -n team1
```

**Use in AgentBuild**:
```yaml
spec:
  source:
    sourceCredentials:
      name: github-token-secret
  buildOutput:
    imageRepoCredentials:
      name: ghcr-secret
```

---

## Next Steps

- **Custom Pipelines**: TODO: Documentation coming soon.
- **Advanced Configuration**: TODO: Documentation coming soon.
- **Webhook Details**: See [AgentBuild Webhook](docs/agentbuild-webhook.md)

---