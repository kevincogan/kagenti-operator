# SPIRE Signing Demo

This demo shows automated AgentCard signing via a SPIRE init-container and operator-side x5c signature verification with trust-domain identity binding.

## Overview

```
                  SPIRE Server
                       |
                  issues X.509-SVID
                       |
                       v
Agent Pod                                    Operator Pod
+---------------------------+                +---------------------------+
|                           |                |                           |
|  init: sign-agentcard     |                |  agentcard-operator       |
|    fetches SVID from SPIRE|                |    fetches /.well-known/  |
|    signs card with JWS    |                |    verifies x5c chain     |
|    writes to shared vol   |                |    validates trust domain |
|                           |                |    sets Verified + Bound  |
|  main: serves signed card |  <-- fetch --- |                           |
|    at /.well-known/       |                |                           |
+---------------------------+                +---------------------------+
```

The operator verifies the JWS signature using the x5c certificate chain embedded in the protected header, then validates that the leaf certificate's SPIFFE ID belongs to the configured trust domain.

## Prerequisites

- Kubernetes cluster with SPIRE installed (e.g. `kagenti/deployments/run_install.sh --env dev`)
- `spire-controller-manager` running (for ClusterSPIFFEID support)
- SPIFFE CSI driver available (`csi.spiffe.io`)
- Trust bundle ConfigMap in the cluster (e.g. `spire-bundle` in `spire-system`)
- kagenti-operator deployed with signature verification flags (see step 2 below)

## Setup

### 1. Build Images

Build the agentcard-signer init-container image and load it into Kind:

```bash
cd kagenti-operator/

# Build the signer image
make build-signer

# Load into Kind (default cluster name is "kagenti")
make load-signer-image

# Or specify a different cluster name
make load-signer-image KIND_CLUSTER_NAME=<your-cluster-name>
```

### 2. Configure the Operator

The operator must be started with these flags for signature verification:

```
--require-a2a-signature=true
--spire-trust-domain=<your-trust-domain>
--spire-trust-bundle-configmap=spire-bundle
--spire-trust-bundle-configmap-namespace=spire-system
--enforce-network-policies=true
```

If using the Helm chart, set these in your values override.

### 3. Deploy the Demo

```bash
kubectl apply -f demos/agentcard-spire-signing/k8s/namespace.yaml
kubectl apply -f demos/agentcard-spire-signing/k8s/clusterspiffeid.yaml
kubectl apply -f demos/agentcard-spire-signing/k8s/agent-deployment.yaml
kubectl apply -f demos/agentcard-spire-signing/k8s/agentcard.yaml
```

### 4. Wait for Pods

```bash
kubectl wait --for=condition=available --timeout=120s deployment/weather-agent -n agents
```

## Test the Flow

Run the demo script to see signing and verification in action:

```bash
./demos/agentcard-spire-signing/run-demo-commands.sh
```

Expected output:

```
=== 1. Init-Container Signing Logs ===
{"level":"info","msg":"starting agentcard signer",...}
{"level":"info","msg":"fetched SVID","spiffe_id":"spiffe://<domain>/ns/agents/sa/weather-agent-sa",...}
{"level":"info","msg":"signed card written successfully",...}

=== 2. Signed Card Verification ===
  Name:       Weather Agent
  Signed:     True
  Signatures: 1

=== 3. JWS Protected Header ===
  Algorithm:  ES256
  Type:       JOSE
  Key ID:     <16-char hex>
  x5c certs:  1

=== 4. Operator Verification Status ===
  SignatureVerified: True  (SignatureValid)
  Bound:             True  (Bound)
  Synced:            True  (SyncSucceeded)

=== 5. Identity Binding ===
  SPIFFE ID:      spiffe://<domain>/ns/agents/sa/weather-agent-sa
  Identity Match: True
  Bound:          True

=== 6. Signature Label ===
  agent.kagenti.dev/signature-verified: true

=== 7. AgentCard Summary ===
NAME                PROTOCOL   KIND         TARGET          AGENT            VERIFIED   BOUND   SYNCED   ...
weather-agent-card  a2a        Deployment   weather-agent   Weather Agent    true       true    True     ...
```

## How It Works

1. The `sign-agentcard` init-container fetches an X.509-SVID from SPIRE via the Workload API
2. It signs the unsigned AgentCard JSON with JWS (ES256), embedding the certificate chain in the `x5c` header
3. The signed card is written to a shared `emptyDir` volume
4. The main container serves the signed card at `/.well-known/agent-card.json`
5. The operator fetches the card, verifies the JWS signature against the SPIRE trust bundle
6. The operator extracts the SPIFFE ID from the leaf certificate's SAN URI
7. If the SPIFFE ID belongs to the configured trust domain, the card is marked as Bound
8. The `agent.kagenti.dev/signature-verified` label is set on the workload

## Cleanup

Use the teardown script to delete all demo resources:

```bash
./demos/agentcard-spire-signing/teardown-demo.sh
```

Or manually:

```bash
kubectl delete -f demos/agentcard-spire-signing/k8s/agentcard.yaml
kubectl delete -f demos/agentcard-spire-signing/k8s/agent-deployment.yaml
kubectl delete -f demos/agentcard-spire-signing/k8s/clusterspiffeid.yaml
kubectl delete -f demos/agentcard-spire-signing/k8s/namespace.yaml
```
