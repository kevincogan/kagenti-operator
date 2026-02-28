# OpenClaw + Kagenti Operator: End-to-End Setup Guide

Deploy the kagenti operator with full AgentCard security on OpenShift, integrated
with OpenClaw and the NPS Agent. By the end you will have two agents with:

- **Auto-discovery** via the AgentCardSync controller
- **JWS signature verification** using SPIRE-issued X.509 SVIDs
- **SPIFFE identity binding** with trust domain validation
- **Network policy enforcement** (optional)

```
┌─────────────────────────────────────────────────────────────────┐
│                      Kagenti Operator                           │
│                                                                 │
│  AgentCardSync ──► watches Deployments with kagenti.io labels   │
│       │                                                         │
│       ▼                                                         │
│  Creates AgentCard CR ──► fetches /.well-known/agent.json       │
│       │                    verifies JWS signature (x5c)         │
│       │                    validates SPIFFE trust domain         │
│       ▼                                                         │
│  NetworkPolicy Controller ──► allow/deny based on verification  │
└─────────────────────────────────────────────────────────────────┘
        │                              │
        ▼                              ▼
┌───────────────────┐    ┌───────────────────────┐
│  OpenClaw Pod     │    │  NPS Agent Pod         │
│  (<prefix>-openclaw)│  │  (nps-agent)           │
│                   │    │                        │
│  gateway :18789   │    │  nps-agent :8090       │
│  a2a-bridge :8080 │    │  a2a-bridge :8080      │
│  spiffe-helper    │    │  spiffe-helper         │
│  envoy-proxy      │    │  envoy-proxy           │
└───────────────────┘    └────────────────────────┘
```

---

## Prerequisites

| Requirement | Notes |
|---|---|
| OpenShift 4.12+ | Tested on NERC `ocp-beta-test` |
| SPIRE server + agent | Deployed in `spire-system` |
| `spire-controller-manager` | Watches `ClusterSPIFFEID` CRs |
| SPIFFE CSI driver | Or `spiffe-helper` sidecar (used here) |
| Keycloak | For A2A OAuth; deployed in `spiffe-demo` realm |
| `oc` / `kubectl` CLI | Logged in with cluster-admin |
| `helm` v3 | For operator installation |
| `docker` or `podman` | For building the operator image |
| `envsubst` | Usually pre-installed; needed by setup scripts |
| `openclaw-infra` repo | Cloned alongside `kagenti-operator` |

---

## Step 1: Build and Push the Operator Image

Build for `linux/amd64` (the cluster architecture) and push to a registry.
[`ttl.sh`](https://ttl.sh) is convenient for testing (images expire automatically).

```bash
# From the repo root
cd kagenti-operator/kagenti-operator

# Pick a unique tag
export IMG=ttl.sh/kagenti-operator-$(whoami):24h

# Cross-compile for amd64 (required if building on Apple Silicon)
docker buildx build --platform linux/amd64 -t "$IMG" --load .
docker push "$IMG"
```

> **Gotcha:** A plain `make docker-build` on macOS will produce an `arm64` image.
> Always use `--platform linux/amd64` for OpenShift clusters.

---

## Step 2: Install the Operator via Helm

```bash
# From the kagenti-operator repo root (parent of kagenti-operator/ and charts/)
cd ..

helm install kagenti-operator ./charts/kagenti-operator \
  -n kagenti-system --create-namespace \
  -f kagenti-operator/demos/openclaw-integration/k8s/values-openclaw-integration.yaml \
  --set controllerManager.container.image.repository=$(echo $IMG | cut -d: -f1) \
  --set controllerManager.container.image.tag=$(echo $IMG | cut -d: -f2) \
  --set controllerManager.container.cmd=/manager \
  --set controllerManager.container.resources.limits.memory=512Mi \
  --set certmanager.enable=false
```

| Flag | Why |
|---|---|
| `cmd=/manager` | The Dockerfile builds the binary as `/manager`; the chart defaults to `/ko-app/cmd` |
| `resources.limits.memory=512Mi` | Default 128Mi is too low when the operator watches many namespaces |
| `certmanager.enable=false` | Skip if cert-manager is not installed on the cluster |

Verify the operator starts:

```bash
oc get pod -n kagenti-system -l control-plane=controller-manager
# Should show 1/1 Running

oc logs -n kagenti-system -l control-plane=controller-manager --tail=5
# Look for: "Signature verification enabled","auditMode":true
```

---

## Step 3: Apply ClusterSPIFFEIDs

SPIRE needs to know which pods get SPIFFE identities. Apply the ClusterSPIFFEID
resources that match pods with `kagenti.io/type=agent` in labeled namespaces:

```bash
cd openclaw-infra
oc apply -f manifests/a2a-infra/spire/clusterspiffeid.yaml
```

This creates two ClusterSPIFFEIDs:
- `agentcard-agents` — matches namespaces labeled `agentcard=true`
- `kagenti-enabled-agents` — matches namespaces labeled `kagenti-enabled=true`

Verify:

```bash
oc get clusterspiffeid
```

---

## Step 4: Deploy OpenClaw

### 4a. Create the `.env.a2a` file

The setup script reads Keycloak credentials from this file. Create it in the
`openclaw-infra` root. Update the URL to match your cluster's Keycloak instance:

```bash
cat > .env.a2a <<'EOF'
KEYCLOAK_URL=https://<your-keycloak-host>
KEYCLOAK_REALM=spiffe-demo
KEYCLOAK_ADMIN_USERNAME=admin
KEYCLOAK_ADMIN_PASSWORD=<your-keycloak-admin-password>
EOF
```

> On the NERC cluster the URL is
> `https://keycloak-spiffe-demo.apps.ocp-beta-test.nerc.mghpcc.org`.

### 4b. Run the setup script

```bash
./scripts/setup.sh --with-a2a
```

The script is interactive. It will prompt for:

| Prompt | Suggested value |
|---|---|
| Prefix | Your username (e.g. `kcogan`) |
| Namespace | Press Enter for default `<prefix>-openclaw` |
| Shadowman display name | Press Enter for default |
| Continue? | `y` |
| Model endpoint | Your OpenAI-compatible endpoint URL |
| Enable Vertex? | `N` |
| Telegram bot token | Press Enter to skip |

The script creates the namespace, generates secrets, runs `envsubst` on all
`.envsubst` templates, and deploys via kustomize.

### 4c. Wait for all containers

```bash
oc get pod -n <prefix>-openclaw -w
# Wait for 6/6 Running
```

### 4d. Verify the agent card is signed

```bash
oc exec -n <prefix>-openclaw deploy/openclaw -c a2a-bridge -- \
  node -e "
    const http = require('http');
    http.get('http://127.0.0.1:8080/.well-known/agent.json', res => {
      let d=''; res.on('data',c=>d+=c);
      res.on('end',()=>{ const j=JSON.parse(d); console.log('name:', j.name, '| signed:', !!j.signatures); });
    });
  "
# Expected: name: openclaw | signed: true
```

---

## Step 5: Deploy the NPS Agent

The NPS setup script reads Keycloak config from `.env.a2a` (created in Step 4a)
and secrets from `.env` (generated by `setup.sh` in Step 4b).

```bash
cd openclaw-infra
./scripts/setup-nps-agent.sh
```

The script prompts for a model endpoint, model name, and an NPS API key
(free from <https://www.nps.gov/subjects/developer/get-started.htm>).

> The NPS deployment template already sets `runAsUser: 0` on the `a2a-bridge`
> container so it can read the SVID private key. If you see `EACCES` errors,
> see [Troubleshooting](#a2a-bridge-cannot-read-svid-private-key-eacces).

After deployment, verify the signed card:

```bash
oc exec -n nps-agent deploy/nps-agent -c a2a-bridge -- \
  node -e "
    const http = require('http');
    http.get('http://127.0.0.1:8080/.well-known/agent.json', res => {
      let d=''; res.on('data',c=>d+=c);
      res.on('end',()=>{ const j=JSON.parse(d); console.log('name:', j.name, '| signed:', !!j.signatures); });
    });
  "
# Expected: name: NPS Agent | signed: true
```

---

## Step 6: Verify Auto-Discovery and Signature Verification

Run the integration check script:

```bash
cd kagenti-operator/kagenti-operator
OPENCLAW_NS=<prefix>-openclaw NPS_NS=nps-agent ./demos/openclaw-integration/run-demo.sh
```

Or check manually:

```bash
# Both agents should appear with VERIFIED=true, SYNCED=True
oc get agentcard -A

# Detailed status
oc get agentcard -n <prefix>-openclaw -o yaml
oc get agentcard -n nps-agent -o yaml
```

Expected output:

```
NAMESPACE         NAME                        VERIFIED   BOUND   SYNCED
<prefix>-openclaw openclaw-deployment-card    true               True
nps-agent         nps-agent-deployment-card   true               True
```

---

## Step 7: Add Identity Binding (Optional)

Identity binding validates that the SPIFFE ID in the agent's certificate belongs
to the expected trust domain. Create manual AgentCard CRs that supersede the
auto-created ones:

```bash
export OPENCLAW_NAMESPACE=<prefix>-openclaw

# OpenClaw (uses envsubst to substitute OPENCLAW_NAMESPACE)
envsubst < demos/openclaw-integration/k8s/openclaw-agentcard.yaml.envsubst | oc apply -f -

# NPS Agent
oc apply -f demos/openclaw-integration/k8s/nps-agentcard.yaml
```

The sync controller automatically deletes the auto-created cards when it detects
a manual card targeting the same workload. After ~30 seconds:

```bash
oc get agentcard -A
# BOUND column should show "true" for both agents
```

---

## Step 8: Enable Enforcement Mode (Optional)

After confirming everything verifies in audit mode, switch to enforcement:

```bash
helm upgrade kagenti-operator ./charts/kagenti-operator \
  -n kagenti-system \
  -f kagenti-operator/demos/openclaw-integration/k8s/values-openclaw-enforce.yaml \
  --set controllerManager.container.image.repository=$(echo $IMG | cut -d: -f1) \
  --set controllerManager.container.image.tag=$(echo $IMG | cut -d: -f2) \
  --set controllerManager.container.cmd=/manager \
  --set controllerManager.container.resources.limits.memory=512Mi \
  --set certmanager.enable=false
```

Verify:

```bash
oc logs -n kagenti-system -l control-plane=controller-manager --tail=5
# Look for: "auditMode":false
```

In enforcement mode, agents that fail signature verification or identity binding
will have restrictive NetworkPolicies applied, isolating them from the network.

---

## Troubleshooting

### A2A bridge cannot read SVID private key (`EACCES`)

The `spiffe-helper` sidecar runs as root and writes `/opt/svid_key.pem` with
`0600` permissions. The `a2a-bridge` container runs as a non-root user by default
and cannot read the key.

**Fix:** Set the `a2a-bridge` container to run as root:

```bash
oc patch deployment <name> -n <namespace> --type='json' -p='[
  {"op":"replace","path":"/spec/template/spec/containers/<INDEX>/securityContext/runAsNonRoot","value":false},
  {"op":"add","path":"/spec/template/spec/containers/<INDEX>/securityContext/runAsUser","value":0}
]'
```

Replace `<INDEX>` with the a2a-bridge container index (check with
`oc get deploy <name> -o jsonpath='{.spec.template.spec.containers[*].name}'`).

This is already applied in the templates for both OpenClaw (`openclaw-deployment.yaml`)
and the NPS agent (`nps-agent-deployment.yaml.envsubst`). If you hit this error on a
deployment that was created before this fix, apply the patch above.

### Operator OOMKilled

The default memory limit is 128Mi, which is insufficient when the operator
watches many namespaces with frequent reconciliation.

**Fix:** Increase the limit:

```bash
oc patch deployment kagenti-controller-manager -n kagenti-system --type='json' \
  -p='[{"op":"replace","path":"/spec/template/spec/containers/0/resources/limits/memory","value":"512Mi"}]'
```

### Operator cannot reach agent service (timeout)

Check for NetworkPolicies in the agent's namespace that block ingress from
`kagenti-system`:

```bash
oc get networkpolicy -n <agent-namespace>
```

If a restrictive policy exists, ensure the `kagenti-system` namespace has the
label the policy expects:

```bash
oc label ns kagenti-system control-plane=kagenti-operator
```

### Helm install fails with cert-manager errors

If cert-manager is not installed on the cluster:

```bash
helm install ... --set certmanager.enable=false
```

If upgrading and the previous release referenced cert-manager CRDs that no
longer exist, force-delete the release secret and do a fresh install:

```bash
oc delete secret sh.helm.release.v1.kagenti-operator.v1 -n kagenti-system
helm install ...   # fresh install instead of upgrade
```

### `CreateContainerError: /ko-app/cmd not found`

The Helm chart defaults to `/ko-app/cmd` (the `ko` builder output path), but
the Dockerfile builds the binary as `/manager`.

**Fix:** Pass `--set controllerManager.container.cmd=/manager` during install.

### ClusterRoleBinding references wrong ClusterRole name

The Helm chart may create a `ClusterRole` with a release-name suffix (e.g.
`kagenti-operator-httproute-kagenti-operator`) but the `ClusterRoleBinding`
references the unsuffixed name (`kagenti-operator-httproute`).

**Fix:** Create an alias ClusterRole:

```bash
oc get clusterrole kagenti-operator-httproute-kagenti-operator -o json | \
  python3 -c "import json,sys; d=json.load(sys.stdin); d['metadata']={'name':'kagenti-operator-httproute'}; del d['metadata'].setdefault('x',None); print(json.dumps(d))" | \
  oc apply -f -
```

### Kustomize patch failures during `setup.sh`

If `setup.sh` fails with "unable to parse SM or JSON patch", the overlay patches
may contain multi-document YAML (`---` separators), which kustomize does not
support in patches. Split multi-document patches into separate files and update
`kustomization.yaml.envsubst`.

### Namespace stuck in Terminating

Force-finalize:

```bash
oc get ns <namespace> -o json | \
  python3 -c "import json,sys; ns=json.load(sys.stdin); ns['spec']['finalizers']=[]; print(json.dumps(ns))" | \
  oc replace --raw /api/v1/namespaces/<namespace>/finalize -f -
```

---

## What Changed in OpenClaw (Reference)

| File | Change |
|---|---|
| `manifests/openclaw/base/openclaw-deployment.yaml` | Added `kagenti.io/type: agent` and `protocol.kagenti.io/a2a` labels; set `a2a-bridge` to `runAsUser: 0`; added SVID env vars and volume mount |
| `manifests/openclaw/base/openclaw-service.yaml` | Added `protocol.kagenti.io/a2a` label |
| `manifests/openclaw/base/a2a-bridge-configmap.yaml` | Added JWS signing with SPIRE SVID (`/opt/svid.pem`, `/opt/svid_key.pem`) |
| `manifests/a2a-infra/spire/clusterspiffeid.yaml` | Added `kagenti-enabled-agents` ClusterSPIFFEID |
| `manifests/openclaw/overlays/openshift/kustomization.yaml.envsubst` | Added `oauth-secrets-patch.yaml` (split from `secrets-patch.yaml`) |
| `manifests/openclaw/overlays/openshift/oauth-secrets-patch.yaml.envsubst` | New file: OAuth secrets patch (was part of `secrets-patch.yaml.envsubst`) |
| `manifests/openclaw/overlays/openshift/secrets-patch.yaml.envsubst` | Removed OAuth secrets (moved to `oauth-secrets-patch.yaml.envsubst`) |
| `manifests/nps-agent/nps-agent-deployment.yaml.envsubst` | Added `protocol.kagenti.io/a2a` label, pod template labels, SVID env vars and volume mount |
| `manifests/nps-agent/nps-agent-service.yaml` | Added `protocol.kagenti.io/a2a` label |
| `manifests/nps-agent/nps-agent-a2a-bridge.yaml` | Added JWS signing with SPIRE SVID |
| `.gitignore` | Added `oauth-secrets-patch.yaml` to ignored generated files |

---

## Helm Values Reference

### Audit mode (`values-openclaw-integration.yaml`)

```yaml
signatureVerification:
  enabled: true
  auditMode: true                    # log-only, no enforcement
  enforceNetworkPolicies: false
  spireTrustDomain: "demo.example.com"
  spireTrustBundle:
    configMapName: "spire-bundle"
    configMapNamespace: "spire-system"
    configMapKey: "bundle.spiffe"
```

### Enforcement mode (`values-openclaw-enforce.yaml`)

```yaml
signatureVerification:
  enabled: true
  auditMode: false                   # enforce: isolate unverified agents
  enforceNetworkPolicies: true
  spireTrustDomain: "demo.example.com"
  spireTrustBundle:
    configMapName: "spire-bundle"
    configMapNamespace: "spire-system"
    configMapKey: "bundle.spiffe"
```

---

## Verification Checklist

After completing all steps, confirm:

- [ ] Operator pod is `1/1 Running` in `kagenti-system`
- [ ] Operator logs show `"Signature verification enabled","auditMode":true` (or `false`)
- [ ] OpenClaw pod is `6/6 Running` in `<prefix>-openclaw`
- [ ] NPS Agent pod is `5/5 Running` in `nps-agent`
- [ ] `oc get agentcard -A` shows both agents with `VERIFIED=true`, `SYNCED=True`
- [ ] (If identity binding) `BOUND=true` for both agents
- [ ] Agent cards are signed: `curl /.well-known/agent.json` returns `signatures` array
