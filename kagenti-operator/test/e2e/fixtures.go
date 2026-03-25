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

const testNamespace = "e2e-agentcard-test"

// echoAgentFixture returns YAML for echo-agent Deployment + Service (used by S1, S3).
func echoAgentFixture() string {
	return `apiVersion: apps/v1
kind: Deployment
metadata:
  name: echo-agent
  namespace: ` + testNamespace + `
  labels:
    kagenti.io/type: agent
    protocol.kagenti.io/a2a: ""
    app.kubernetes.io/name: echo-agent
spec:
  replicas: 1
  selector:
    matchLabels:
      app.kubernetes.io/name: echo-agent
      kagenti.io/type: agent
  template:
    metadata:
      labels:
        app.kubernetes.io/name: echo-agent
        kagenti.io/type: agent
        protocol.kagenti.io/a2a: ""
    spec:
      securityContext:
        runAsNonRoot: true
        runAsUser: 1000
        seccompProfile:
          type: RuntimeDefault
      containers:
        - name: echo
          image: docker.io/python:3.11-slim
          imagePullPolicy: IfNotPresent
          command:
            - python3
            - -c
            - |
              import http.server, json
              class H(http.server.BaseHTTPRequestHandler):
                  def do_GET(self):
                      if self.path == '/.well-known/agent-card.json':
                          card = {'name': 'Echo Agent', 'version': '1.0.0',
                                  'url': 'http://echo-agent.` + testNamespace + `.svc:8001'}
                          self.send_response(200)
                          self.send_header('Content-Type', 'application/json')
                          self.end_headers()
                          self.wfile.write(json.dumps(card).encode())
                      else:
                          self.send_response(404)
                          self.end_headers()
                  def log_message(self, *a): pass
              http.server.HTTPServer(('', 8001), H).serve_forever()
          ports:
            - containerPort: 8001
          securityContext:
            allowPrivilegeEscalation: false
            capabilities:
              drop:
                - ALL
---
apiVersion: v1
kind: Service
metadata:
  name: echo-agent
  namespace: ` + testNamespace + `
spec:
  selector:
    app.kubernetes.io/name: echo-agent
  ports:
    - port: 8001
      targetPort: 8001
`
}

// noProtocolAgentFixture returns YAML for noproto-agent Deployment (S2) - has
// kagenti.io/type=agent but NO protocol.kagenti.io/* label.
func noProtocolAgentFixture() string {
	return `apiVersion: apps/v1
kind: Deployment
metadata:
  name: noproto-agent
  namespace: ` + testNamespace + `
  labels:
    kagenti.io/type: agent
    app.kubernetes.io/name: noproto-agent
spec:
  replicas: 1
  selector:
    matchLabels:
      app.kubernetes.io/name: noproto-agent
      kagenti.io/type: agent
  template:
    metadata:
      labels:
        app.kubernetes.io/name: noproto-agent
        kagenti.io/type: agent
    spec:
      securityContext:
        runAsNonRoot: true
        runAsUser: 1000
        seccompProfile:
          type: RuntimeDefault
      containers:
        - name: pause
          image: registry.k8s.io/pause:3.9
          imagePullPolicy: IfNotPresent
          securityContext:
            allowPrivilegeEscalation: false
            capabilities:
              drop:
                - ALL
`
}

// manualAgentCardFixture returns YAML for a manual AgentCard targeting echo-agent (S3).
func manualAgentCardFixture() string {
	return `apiVersion: agent.kagenti.dev/v1alpha1
kind: AgentCard
metadata:
  name: echo-agent-manual-card
  namespace: ` + testNamespace + `
spec:
  targetRef:
    apiVersion: apps/v1
    kind: Deployment
    name: echo-agent
`
}

// invalidAgentCardFixture returns YAML for an AgentCard WITHOUT spec.targetRef (S6).
func invalidAgentCardFixture() string {
	return `apiVersion: agent.kagenti.dev/v1alpha1
kind: AgentCard
metadata:
  name: invalid-no-targetref
  namespace: ` + testNamespace + `
spec:
  syncPeriod: "30s"
`
}

// auditAgentFixture returns YAML for audit-agent Deployment + Service (S5).
func auditAgentFixture() string {
	return `apiVersion: apps/v1
kind: Deployment
metadata:
  name: audit-agent
  namespace: ` + testNamespace + `
  labels:
    kagenti.io/type: agent
    protocol.kagenti.io/a2a: ""
    app.kubernetes.io/name: audit-agent
spec:
  replicas: 1
  selector:
    matchLabels:
      app.kubernetes.io/name: audit-agent
      kagenti.io/type: agent
  template:
    metadata:
      labels:
        app.kubernetes.io/name: audit-agent
        kagenti.io/type: agent
        protocol.kagenti.io/a2a: ""
    spec:
      securityContext:
        runAsNonRoot: true
        runAsUser: 1000
        seccompProfile:
          type: RuntimeDefault
      containers:
        - name: echo
          image: docker.io/python:3.11-slim
          imagePullPolicy: IfNotPresent
          command:
            - python3
            - -c
            - |
              import http.server, json
              class H(http.server.BaseHTTPRequestHandler):
                  def do_GET(self):
                      if self.path == '/.well-known/agent-card.json':
                          card = {'name': 'Audit Agent', 'version': '1.0.0',
                                  'url': 'http://audit-agent.` + testNamespace + `.svc:8002'}
                          self.send_response(200)
                          self.send_header('Content-Type', 'application/json')
                          self.end_headers()
                          self.wfile.write(json.dumps(card).encode())
                      else:
                          self.send_response(404)
                          self.end_headers()
                  def log_message(self, *a): pass
              http.server.HTTPServer(('', 8002), H).serve_forever()
          ports:
            - containerPort: 8002
          securityContext:
            allowPrivilegeEscalation: false
            capabilities:
              drop:
                - ALL
---
apiVersion: v1
kind: Service
metadata:
  name: audit-agent
  namespace: ` + testNamespace + `
spec:
  selector:
    app.kubernetes.io/name: audit-agent
  ports:
    - port: 8002
      targetPort: 8002
`
}

// auditModeAgentCardFixture returns YAML for AgentCard targeting audit-agent.
// Uses the auto-created card name so kubectl apply updates the existing card.
func auditModeAgentCardFixture() string {
	return `apiVersion: agent.kagenti.dev/v1alpha1
kind: AgentCard
metadata:
  name: audit-agent-deployment-card
  namespace: ` + testNamespace + `
  labels:
    app.kubernetes.io/name: audit-agent
    app.kubernetes.io/managed-by: kagenti-operator
spec:
  targetRef:
    apiVersion: apps/v1
    kind: Deployment
    name: audit-agent
`
}

// signedAgentFixture returns YAML for the full signed-agent stack (S4):
// ServiceAccount, Role, RoleBinding, ConfigMap, Deployment (with agentcard-signer
// init-container + SPIRE CSI volume), Service.
func signedAgentFixture() string {
	return `apiVersion: v1
kind: ServiceAccount
metadata:
  name: signed-agent-sa
  namespace: ` + testNamespace + `
---
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: agentcard-signer
  namespace: ` + testNamespace + `
rules:
  - apiGroups: [""]
    resources: ["configmaps"]
    verbs: ["create", "update", "get"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: agentcard-signer
  namespace: ` + testNamespace + `
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: agentcard-signer
subjects:
  - kind: ServiceAccount
    name: signed-agent-sa
    namespace: ` + testNamespace + `
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: signed-agent-card-unsigned
  namespace: ` + testNamespace + `
data:
  agent.json: |
    {
      "name": "Signed Agent",
      "description": "Agent with SPIRE-signed agent card",
      "url": "http://signed-agent.` + testNamespace + `.svc.cluster.local:8080",
      "version": "1.0.0",
      "capabilities": {
        "streaming": false,
        "pushNotifications": false
      },
      "defaultInputModes": ["text/plain"],
      "defaultOutputModes": ["text/plain"],
      "skills": [
        {
          "name": "echo",
          "description": "Echo back the input",
          "inputModes": ["text/plain"],
          "outputModes": ["text/plain"]
        }
      ]
    }
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: signed-agent
  namespace: ` + testNamespace + `
  labels:
    kagenti.io/type: agent
    protocol.kagenti.io/a2a: ""
    app.kubernetes.io/name: signed-agent
spec:
  replicas: 1
  selector:
    matchLabels:
      app.kubernetes.io/name: signed-agent
      kagenti.io/type: agent
  template:
    metadata:
      labels:
        app.kubernetes.io/name: signed-agent
        kagenti.io/type: agent
        protocol.kagenti.io/a2a: ""
    spec:
      serviceAccountName: signed-agent-sa
      securityContext:
        runAsNonRoot: true
        runAsUser: 1000
        seccompProfile:
          type: RuntimeDefault
      initContainers:
        - name: sign-agentcard
          image: ghcr.io/kagenti/kagenti-operator/agentcard-signer:e2e-test
          imagePullPolicy: IfNotPresent
          env:
            - name: SPIFFE_ENDPOINT_SOCKET
              value: unix:///run/spire/agent-sockets/spire-agent.sock
            - name: UNSIGNED_CARD_PATH
              value: /etc/agentcard/agent.json
            - name: AGENT_CARD_PATH
              value: /app/.well-known/agent-card.json
            - name: SIGN_TIMEOUT
              value: "30s"
            - name: AGENT_NAME
              value: signed-agent
            - name: POD_NAMESPACE
              valueFrom:
                fieldRef:
                  fieldPath: metadata.namespace
          volumeMounts:
            - name: spire-agent-socket
              mountPath: /run/spire/agent-sockets
              readOnly: true
            - name: unsigned-card
              mountPath: /etc/agentcard
              readOnly: true
            - name: signed-card
              mountPath: /app/.well-known
          securityContext:
            runAsNonRoot: true
            readOnlyRootFilesystem: true
            allowPrivilegeEscalation: false
            capabilities:
              drop:
                - ALL
          resources:
            requests:
              cpu: 10m
              memory: 16Mi
            limits:
              cpu: 100m
              memory: 32Mi
      containers:
        - name: agent
          image: docker.io/python:3.11-slim
          imagePullPolicy: IfNotPresent
          command: ["python3", "-m", "http.server", "8080", "--directory", "/app"]
          ports:
            - containerPort: 8080
          volumeMounts:
            - name: signed-card
              mountPath: /app/.well-known
              readOnly: true
          securityContext:
            allowPrivilegeEscalation: false
            capabilities:
              drop:
                - ALL
      volumes:
        - name: spire-agent-socket
          csi:
            driver: csi.spiffe.io
            readOnly: true
        - name: unsigned-card
          configMap:
            name: signed-agent-card-unsigned
        - name: signed-card
          emptyDir:
            medium: Memory
            sizeLimit: 1Mi
---
apiVersion: v1
kind: Service
metadata:
  name: signed-agent
  namespace: ` + testNamespace + `
spec:
  selector:
    app.kubernetes.io/name: signed-agent
  ports:
    - port: 8080
      targetPort: 8080
`
}

// signedAgentCardFixture returns YAML for AgentCard with identityBinding for signed-agent (S4).
// Uses the auto-created card name so kubectl apply updates the existing card.
func signedAgentCardFixture() string {
	return `apiVersion: agent.kagenti.dev/v1alpha1
kind: AgentCard
metadata:
  name: signed-agent-deployment-card
  namespace: ` + testNamespace + `
  labels:
    app.kubernetes.io/name: signed-agent
    app.kubernetes.io/managed-by: kagenti-operator
spec:
  syncPeriod: "30s"
  targetRef:
    apiVersion: apps/v1
    kind: Deployment
    name: signed-agent
  identityBinding:
    strict: true
`
}

// clusterSPIFFEIDFixture returns YAML for ClusterSPIFFEID (S4).
func clusterSPIFFEIDFixture() string {
	return `apiVersion: spire.spiffe.io/v1alpha1
kind: ClusterSPIFFEID
metadata:
  name: e2e-agentcard-test
spec:
  spiffeIDTemplate: "spiffe://{{ .TrustDomain }}/ns/{{ .PodMeta.Namespace }}/sa/{{ .PodSpec.ServiceAccountName }}"
  podSelector:
    matchLabels:
      kagenti.io/type: agent
  namespaceSelector:
    matchLabels:
      agentcard: "true"
`
}
