# Operator Signing Demo

This demo shows automated AgentCard signing by the kagenti operator using its
own SPIFFE identity, with x5c signature verification and trust-domain identity
binding.

## Overview

```
SPIRE Server
     |
     | issues X.509-SVID to operator
     v
Operator Pod                                Agent Pod
+---------------------------+               +---------------------------+
|                           |               |                           |
|  kagenti-operator         |  -- fetch --> |  serves /.well-known/     |
|    HTTP GET agent card    |               |  agent-card.json          |
|    signs with operator    |               |  (rich card with skills)  |
|      SPIFFE identity      |               |                           |
|    verifies x5c chain     |               +---------------------------+
|    validates trust domain |
|    sets Verified + Bound  |
|                           |
+---------------------------+
```

The agent serves its card at `/.well-known/agent-card.json`. The operator
fetches it via HTTP, signs it with a JWS (ES256) using the operator's own
SPIFFE SVID, verifies the signature against the SPIRE trust bundle, and
validates the signer's SPIFFE ID belongs to the configured trust domain.

## Prerequisites

- Kubernetes/OpenShift cluster with SPIRE installed
- `spire-controller-manager` running (for ClusterSPIFFEID support)
- SPIFFE CSI driver available (`csi.spiffe.io`)
- Trust bundle ConfigMap labeled `kagenti.io/defaults=true`
- kagenti-operator deployed with `signatureVerification.enabled=true`

## Setup

### 1. Deploy the Demo

No images to build — the agent uses a standard `python:3.11-slim` image.

```bash
kubectl apply -f demos/agentcard-operator-signing/k8s/namespace.yaml
kubectl apply -f demos/agentcard-operator-signing/k8s/clusterspiffeid.yaml
kubectl apply -f demos/agentcard-operator-signing/k8s/agent-deployment.yaml
kubectl apply -f demos/agentcard-operator-signing/k8s/agentcard.yaml
```

### 2. Wait for the Agent

```bash
kubectl wait --for=condition=available --timeout=120s \
  deployment/weather-agent -n agents
```

### 3. Wait for Signing

The operator signs the card on the next reconciliation cycle (~30s):

```bash
sleep 30
kubectl get agentcard weather-agent-card -n agents
# Expect: VERIFIED=true, BOUND=true, SYNCED=True
```

## Test the Flow

Run the demo script to see signing and verification in action:

```bash
./demos/agentcard-operator-signing/run-demo-commands.sh
```

Expected output:

```
=== 1. Agent Card Served via HTTP ===
  Name:   Weather Agent
  Skills: ['get_weather']

=== 2. Operator Signing Log ===
  Workload:  weather-agent
  SPIFFE ID: spiffe://<domain>/ns/<operator-ns>/sa/controller-manager

=== 3. JWS Protected Header ===
  Algorithm:  ES256
  Type:       JOSE
  Key ID:     <16-char hex>
  x5c certs:  1

=== 4. Operator Verification Status ===
  SignatureVerified       True   (SignatureValid)
  Bound                  True   (Bound)
  Synced                 True   (SyncSucceeded)

=== 5. Identity Binding ===
  SPIFFE ID:      spiffe://<domain>/ns/<operator-ns>/sa/controller-manager
  Identity Match: True
  Bound:          True

=== 6. Signature Label ===
  agent.kagenti.dev/signature-verified: true

=== 7. AgentCard Summary ===
NAME                PROTOCOL   KIND         TARGET          AGENT            VERIFIED   BOUND   SYNCED   ...
weather-agent-card  a2a        Deployment   weather-agent   Weather Agent    true       true    True     ...
```

## How It Works

1. The agent serves its card at `/.well-known/agent-card.json` via HTTP
2. The operator fetches the card during reconciliation
3. The operator signs the card with JWS (ES256) using its own SPIFFE SVID
4. The operator verifies the signature against the SPIRE trust bundle
5. The SPIFFE ID from the leaf certificate's SAN URI is extracted
6. If the SPIFFE ID belongs to the configured trust domain, the card is marked as Bound
7. The `agent.kagenti.dev/signature-verified` label is set on the workload
8. The signed card is stored in the AgentCard CR status

## Cleanup

```bash
./demos/agentcard-operator-signing/teardown-demo.sh
```
