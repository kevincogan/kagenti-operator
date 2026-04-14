# RFC: Sigstore Integration for AgentCard Supply-Chain Verification

| | |
|---|---|
| **Status** | Draft |
| **Author** | [Kevin Cogan](mailto:kcogan@redhat.com), [Dean Kelly](mailto:dekelly@redhat.com) |
| **Target repo** | [kagenti/kagenti-operator](https://github.com/kagenti/kagenti-operator) |
| **Date** | Apr 2, 2026 |
| **Related** | [Agent Attestation Epic #612](https://github.com/kagenti/kagenti/issues/612), [AgentCard UI Integration PR #1038](https://github.com/kagenti/kagenti/pull/1038), [AgentTrust Capability Validation RFC](agenttrust-capability-validation-integration.md), [SPIRE Stacking and Composition #6640](https://github.com/spiffe/spire/issues/6640), [SPIRE K8s Attestor Sigstore Support](https://github.com/spiffe/spire/blob/main/doc/plugin_agent_workloadattestor_k8s.md#sigstore-experimental-feature) |

---

## 1. Summary

This RFC proposes integrating [Sigstore](https://sigstore.dev) into the kagenti-operator's AgentCard CR feature as a **supply-chain provenance layer** alongside the existing SPIRE/SPIFFE runtime identity system.

The integration adds three capabilities:

1. **Container image verification via SPIRE**: gate SVID issuance on image signature validity using the SPIRE K8s attestor's built-in Sigstore support
2. **A2A agent card blob signing and verification**: produce and verify `.sigstore.json` Sigstore bundles for A2A agent cards, with Rekor transparency log audit trails
3. **OCI attestations** (future): attach A2A agent card data as in-toto attestations to agent container images

All capabilities are **optional, disabled by default**, and additive to the existing SPIRE-based signing. No existing behavior changes when Sigstore features are not enabled.

### Terminology

This RFC refers to three distinct artifacts that share the name "agent card." To avoid ambiguity:

| Term used in this RFC | What it is | Format |
|---|---|---|
| **A2A agent card** | The JSON file served by the agent at `/.well-known/agent-card.json`, defined by the [A2A protocol](https://google.github.io/A2A/). Describes capabilities, skills, and supported protocols. Contains no platform attributes (no image digests, namespaces, or ServiceAccounts). | `agent-card.json` |
| **AgentCard CR** | The Kubernetes custom resource (`agent.kagenti.dev/AgentCard`) managed by the kagenti-operator. Wraps the A2A agent card content and adds platform-level metadata: verification status, signature results, Sigstore bundle status, and conditions. | Kubernetes CR in etcd |
| **Sigstore bundle** | A Sigstore bundle produced by the CI/CD pipeline that cryptographically proves the A2A agent card blob came from a trusted build process. It is a provenance artifact *about* the card, not a card itself. | `agent-card.sigstore.json` |

### Trust assurances at a glance

This RFC introduces three independent trust layers. Each answers a different question, is signed by a different entity, and can be enabled or disabled separately:

| Layer | What is verified | Who verifies | When | Trust question answered | Artifact |
|---|---|---|---|---|---|
| **Supply-chain provenance** | The A2A agent card blob bytes | CI/CD pipeline via `cosign sign-blob` (Fulcio OIDC identity) | Build time | "Was this card produced by a trusted pipeline?" | `agent-card.sigstore.json` |
| **Runtime workload binding** | The binding between the A2A agent card and the live workload | Kagenti operator using its SPIFFE SVID | Reconcile time | "Has the platform verified this card was declared by a legitimate workload?" | JWS x5c signature on `agent-card.json` |
| **Image provenance** | The container image signature | SPIRE K8s attestor (Sigstore experimental feature) | SVID issuance time | "Was this container image built by a trusted pipeline?" | Sigstore selectors on SPIRE registration entry |

These layers are independent — each writes its result to a separate field on the AgentCard CR status, and failure in one does not affect the others (unless enforcement is enabled and audit mode is off; see Phase 3).

**What is not covered by this RFC:** Validating whether the *capabilities* an agent claims (skills, models, protocols) are actually accurate. The operator signature means "a legitimate workload made these claims," not "these claims are true." Capability validation is handled by **[AgentTrust](agenttrust-capability-validation-integration.md)**, a separate component covered by its own RFC. AgentTrust addresses Signal 2 (Capability) from the [#612 epic](https://github.com/kagenti/kagenti/issues/612); this RFC addresses Signal 4 (Provenance) and strengthens Signal 1 (Identity).

### Context

This RFC was updated following the [PR #1038 review discussion](https://github.com/kagenti/kagenti/pull/1038), which established two key architectural decisions:

* **Signing authority moves to the operator.** The `agentcard-signer` init-container (self-attestation model) is being replaced by operator-level signing, where the operator inspects workload facts and signs cards with its own SPIFFE identity. See the [PR #1038 review](https://github.com/kagenti/kagenti/pull/1038#issuecomment-mrsabath) for the trust model analysis that motivated this change.
* **Image verification uses SPIRE natively.** The SPIRE K8s attestor's [Sigstore experimental feature](https://github.com/spiffe/spire/blob/main/doc/plugin_agent_workloadattestor_k8s.md#sigstore-experimental-feature) gates SVID issuance on image signature validity, removing the need for a separate Policy Controller admission webhook.

## 2. Motivation

### What exists today

The operator provides **runtime trust** via SPIRE/SPIFFE:

* The operator verifies JWS x5c signatures on A2A agent cards against the SPIRE trust bundle via the `X5CProvider`
* Identity binding verifies the SPIFFE ID belongs to the configured trust domain
* NetworkPolicy enforcement restricts network access for unverified agents

Today, the agent signs its own A2A agent card via an `agentcard-signer` init-container using its workload SPIRE identity. The operator verifies that signature but does not produce it. This is a self-attestation model: the agent vouches for itself. As described in the Context section above, signing authority is moving to the operator to eliminate self-attestation.

This answers: **"Is this card signed by a trusted identity?"**

### What is missing

There is no verifiable link between the running agent and the CI/CD pipeline that built it. SPIRE proves the pod has the right ServiceAccount, but says nothing about whether the container image or A2A agent card was produced by a trusted build process. There is also no tamper-evident audit log of signing events.

This gap is already recognized in the Kagenti project:

* `docs/releasing.md` lists "Artifact signing and provenance: Sign container images with Sigstore/cosign and generate SLSA provenance attestations" as future work
* `docs/user-stories.md` includes the user story: "integrate my build system with AgentCard CR resources to attach build provenance signatures"
* `docs/architecture/agent-sandbox.md` includes `Sigstore attestation` in the trust verification chain
* The [Agent Attestation Epic #612](https://github.com/kagenti/kagenti/issues/612) defines Provenance as Signal 4, currently unimplemented

### What Sigstore adds

Sigstore answers: **"Was this artifact produced by a trusted pipeline, and can I prove it?"**

| Sigstore Component | Purpose | Relevance |
| :---- | :---- | :---- |
| **Cosign** (`sign-blob` / `verify-blob`) | Sign arbitrary files with keyless (Fulcio OIDC) or key-based crypto | Sign the A2A agent card blob (`agent-card.json`) |
| **Fulcio** | Issue short-lived X.509 certs bound to OIDC identities | Tie A2A agent card signatures to a verifiable CI/CD identity |
| **Rekor** | Immutable transparency log recording all signing events | Audit trail proving when a card was signed and by whom |

### Why both layers are needed

| Concern | SPIRE (existing) | Sigstore (proposed) |
| :---- | :---- | :---- |
| Trust question | "Is this pod trusted right now?" | "Was this artifact built by a trusted pipeline?" |
| Certificate issuer | SPIRE CA (cluster-internal) | Fulcio CA (public or private instance) |
| Identity in cert | SPIFFE ID | OIDC subject (GitHub Actions, k8s SA) |
| Audit trail | None | Rekor transparency log |
| mTLS between workloads | Yes (via Istio/Envoy) | No |
| CI/CD identity binding | No | Yes |
| Image provenance | No | Yes |

The two layers are complementary. SPIRE stays as the runtime identity layer; Sigstore adds supply-chain provenance.

## 3. Architecture

### Dual-layer trust model

**How the two signatures relate:** The same A2A agent card (`agent-card.json`) receives two independent signatures that answer different trust questions:

| Signature | Who Signed | When | Trust question answered | Status field |
| :---- | :---- | :---- | :---- | :---- |
| Sigstore bundle (`agent-card.sigstore.json`) | CI/CD pipeline | Build time | Was this card produced by a trusted pipeline? | `status.sigstoreBundleVerified` |
| JWS x5c  (`agent-card.json`) | Kagenti Operator | Reconcile time	 | Has the platform verified this card matches the running workload? | `status.validSignature` |

The operator does not blindly trust the agent's self-declared A2A agent card. During reconciliation, the operator fetches the A2A agent card, inspects the workload's pod spec (image digest, ServiceAccount, namespace), validates the card content against those observed facts, and then signs it. The agent's original A2A agent card is the input; the operator-signed card is the verified output. The workload gets read-only access to the signed result.

Both verification results are written to the AgentCard CR status as separate fields and conditions. The `Ready` condition only becomes `False` when a verification is enabled, fails, and is not in audit mode (see Phase 3).

## 4. Design Decisions

| # | Decision | Choice | Rationale |
|---|----------|--------|-----------|
| D1 | Sigstore verification interface | New `BundleVerifier` interface, not reuse `Provider` | Interface Segregation: Sigstore operates on raw bytes + bundle, not A2A JWS structures |
| D2 | Fetcher return type | `FetchResult` struct with `CardData` + `RawCardJSON` + `BundleJSON` | Single interface change, extensible, preserves raw bytes for digest verification |
| D3 | Dual provider wiring | Two separate fields on reconciler, sequential calls | Simpler than composite pattern, easier to debug, independent status reporting |
| D4 | Sigstore audit mode | Separate `--sigstore-audit-mode` flag | Mirrors existing SPIRE `--signature-audit-mode` for enterprise rollout parity |
| D5 | Ready condition gating | Only block Ready when verification enabled + fails + not in audit mode | Backward compatible; absent bundles do not block (graceful adoption) |
| D6 | Image verification approach | SPIRE K8s attestor Sigstore feature, not Policy Controller | Zero new infrastructure; gates SVID issuance natively rather than adding a separate admission webhook |
| D7 | Signing authority | Operator signs cards with its own SPIFFE identity | Eliminates self-attestation; operator observes workload facts and signs based on observed reality |
| D8 | Operator Sigstore verification | Use `sigstore-go` library | Library is the right choice for a long-running process; no CLI dependency in operator image |
| D9 | Sigstore blob signing in CI | `cosign sign-blob` in CI pipeline | CI is the natural place for supply-chain provenance signing; card is signed before deployment |
| D10 | Operator SPIFFE registration | `ClusterSPIFFEID` CR via spire-controller-manager | Works on both vanilla SPIRE and ZTWIM (OpenShift); declarative, self-healing, no manual CLI steps |

### D1: BundleVerifier interface

The existing `Provider` interface is shaped for A2A JWS verification:

```go
type Provider interface {
    VerifySignature(ctx context.Context, cardData *AgentCardData, signatures []AgentCardSignature) (*VerificationResult, error)
    Name() string
    BundleHash() string
}
```

Sigstore bundle verification operates on **raw artifact bytes + a `.sigstore.json` bundle**. Forcing it into the JWS-shaped interface would be a leaky abstraction. Instead, introduce a clean parallel interface:

```go
type BundleVerifier interface {
    VerifyBundle(ctx context.Context, artifactBytes []byte, bundleBytes []byte) (*VerificationResult, error)
    Name() string
}
```

Both return the same `VerificationResult` struct, so the reconciler and status-writing code can handle them uniformly.

### D2: FetchResult struct

The current `Fetcher.Fetch()` returns only `*AgentCardData` (parsed A2A agent card JSON, raw bytes discarded). Sigstore verification needs the **raw card JSON bytes** for digest verification against the Sigstore bundle, plus the **bundle bytes** themselves.

```go
type FetchResult struct {
    CardData    *agentv1alpha1.AgentCardData
    RawCardJSON []byte // preserved for digest-based verification
    BundleJSON  []byte // Sigstore .sigstore.json bundle (nil when absent)
}

type Fetcher interface {
    Fetch(ctx context.Context, protocol, serviceURL, agentName, namespace string) (*FetchResult, error)
}
```

The ConfigMapFetcher retains raw bytes before unmarshaling and reads the optional `agent-card.sigstore.json` Sigstore bundle key from the same ConfigMap. The `DefaultFetcher` (HTTP path) retains raw response bytes; `BundleJSON` is nil for HTTP-fetched cards.

### D3: Dual providers in the reconciler

The reconciler gets two separate verifier fields rather than a composite:

```go
type AgentCardReconciler struct {
    // ... existing fields ...
    SignatureProvider  signature.Provider       // SPIRE x5c (existing)
    SigstoreVerifier   signature.BundleVerifier // Sigstore bundle (new, nil when disabled)
    SigstoreAuditMode  bool
}
```

Called sequentially in `Reconcile()`:

```go
// Existing x5c verification (unchanged)
var verificationResult *signature.VerificationResult
if r.RequireSignature {
    verificationResult, _ = r.verifySignature(ctx, result.CardData)
}

// New Sigstore verification
var sigstoreResult *signature.VerificationResult
if r.SigstoreVerifier != nil && result.BundleJSON != nil {
    sigstoreResult = r.verifySigstoreBundle(ctx, result.RawCardJSON, result.BundleJSON)
}
```

Both results are written to separate status fields and conditions.

### D6: SPIRE Sigstore attestor over Policy Controller

The SPIRE K8s workload attestor has [built-in Sigstore support](https://github.com/spiffe/spire/blob/main/doc/plugin_agent_workloadattestor_k8s.md#sigstore-experimental-feature) that verifies container image signatures as part of workload attestation. When enabled, SPIRE refuses to issue an SVID to workloads running unsigned images.

This is preferred over deploying Sigstore's Policy Controller because:

* **Zero new infrastructure**: SPIRE is already deployed in the Kagenti platform
* **Stronger enforcement**: SVID denial prevents the workload from participating in mTLS, not just blocking pod creation
* **Alignment with long-term vision**: the [stacking attestors proposal (spiffe/spire#6640)](https://github.com/spiffe/spire/issues/6640) extends this pattern so attestors can chain by consuming each other's selectors, enabling card hash and capability verification without duplicating pod resolution logic

### D7: Operator as signing authority

The [PR #1038 review](https://github.com/kagenti/kagenti/pull/1038) identified that the init-container signing model is self-attestation: the workload uses its own SPIRE identity to sign its own capability claims. This creates a trust model where a compromised agent image can influence what gets signed.

The agreed direction is operator-level signing:

* The operator gets its own SPIFFE identity (platform-level key)
* On AgentCard CR reconciliation, the operator inspects the workload's pod spec, image digests, labels, and ServiceAccount
* The operator validates the A2A agent card based on **observed facts**, not self-reported claims
* The operator signs with its own identity and writes the signed card to the AgentCard CR status
* The workload gets **read-only** access to its signed card

This eliminates the self-attestation problem. The existing verification and binding infrastructure carries over unchanged; only the signing entity changes.

## 5. Implementation Phases

### Phase 1: SPIRE Sigstore Image Verification

**Scope**: SPIRE agent configuration only. Zero operator Go code changes.

Enable the SPIRE K8s attestor's Sigstore feature to verify container image signatures as part of workload attestation. When enabled, SPIRE adds Sigstore-derived selectors (e.g., `k8s:image-signature-subject`, `k8s:image-signature-issuer`) that registration entries can require.

**SPIRE agent configuration**:

```
WorkloadAttestor "k8s" {
    plugin_data {
        experimental {
            sigstore {
                rekor_url = "https://rekor.sigstore.dev"
                allowed_identities = {
                    "https://token.actions.githubusercontent.com" = [
                        "https://github.com/kagenti/*"
                    ]
                }
            }
        }
    }
}
```

**Registration entry using Sigstore selectors**:

```shell
spire-server entry create \
  -spiffeID spiffe://kagenti.dev/agent/weather-agent \
  -parentID spiffe://kagenti.dev/spire/agent/... \
  -selector k8s:ns:team1 \
  -selector k8s:sa:weather-agent \
  -selector k8s:image-signature-subject:https://github.com/kagenti/weather-agent
```

Workloads running unsigned images will not receive SVIDs, preventing them from participating in mTLS or obtaining a trusted identity.

**Prerequisite**: SPIRE must be deployed with the K8s workload attestor. The Sigstore feature is experimental in SPIRE and must be enabled in the agent configuration.

**Helm integration**: The Kagenti platform chart (or SPIRE sub-chart) exposes Sigstore attestor settings. The operator chart itself does not change for Phase 1.

### Phase 2: Operator Signing + Agent Card Blob Verification

**Scope**: Go code, CRD, Helm chart.

This phase has two parts: (a) moving signing authority to the operator, and (b) adding Sigstore bundle verification for supply-chain provenance.

#### Part A: Operator signing

The operator signs A2A agent cards with its own SPIFFE identity instead of delegating to an init-container. This requires:

1. **Operator SPIFFE registration**: A `ClusterSPIFFEID` resource targets the operator pod by label, granting it a distinct X.509-SVID (platform-level identity, not workload-level). This works on both vanilla SPIRE (spire-controller-manager) and OpenShift ZTWIM, since [ZTWIM deploys the same spire-controller-manager](https://github.com/openshift/spiffe-spire-controller-manager) with the same `ClusterSPIFFEID` CRD:

```yaml
apiVersion: spire.spiffe.io/v1alpha1
kind: ClusterSPIFFEID
metadata:
  name: kagenti-operator
spec:
  spiffeIDTemplate: "spiffe://{{ .TrustDomain }}/ns/{{ .PodMeta.Namespace }}/sa/{{ .PodSpec.ServiceAccountName }}"
  podSelector:
    matchLabels:
      control-plane: controller-manager
  namespaceSelector:
    matchLabels:
      kubernetes.io/metadata.name: kagenti-system
```

   This is deployed as a Helm template in the operator chart, so it is created automatically on install.

2. **Signing in the reconciler**: After fetching the agent's A2A agent card (from ConfigMap or HTTP) and validating it against the workload's pod spec, the operator signs it with its SVID private key, writes the signed card to a ConfigMap, and records the verification result on the AgentCard CR status
3. **Workload read-only access**: The workload's RBAC permits only reading (not writing) its signed AgentCard CR

The operator's signing logic inspects the workload's pod spec to bind observed facts (image digest, ServiceAccount, namespace) into the signed card. This replaces the self-attestation model where the workload signed its own claims.

#### Part B: Sigstore bundle verification

**Go dependency**: Add `github.com/sigstore/sigstore-go` (v1.1.x) to `kagenti-operator/go.mod`. Provides `pkg/bundle`, `pkg/verify`, and `pkg/root`.

**SigstoreProvider implementation**: New file `internal/signature/sigstore.go` implementing `BundleVerifier`:

1. Load trusted material via `root.FetchTrustedRoot()` (public Sigstore) or `root.NewTrustedRootFromJSON()` (private instance, trust root in ConfigMap)
2. Parse bundle bytes via `bundle.LoadJSONFromReader()`
3. Build verifier with `verify.WithTransparencyLog(1)` and `verify.WithObserverTimestamps(1)`
4. Build policy with `verify.WithArtifact()` (raw card JSON) and `verify.WithCertificateIdentity()` (expected OIDC issuer/subject)
5. Return `VerificationResult` with verified status, Fulcio identity, and Rekor log index

**CRD changes**:

New spec field on `AgentCardSpec`:

```go
type SigstoreVerification struct {
    CertificateIdentity  string `json:"certificateIdentity,omitempty"`
    CertificateOIDCIssuer string `json:"certificateOIDCIssuer,omitempty"`
    RekorURL             string `json:"rekorURL,omitempty"`
}
```

New status fields on `AgentCardStatus`:

```go
SigstoreBundleVerified *bool  `json:"sigstoreBundleVerified,omitempty"`
SigstoreIdentity       string `json:"sigstoreIdentity,omitempty"`
RekorLogIndex          string `json:"rekorLogIndex,omitempty"`
```

New condition type: `SigstoreVerified`.

New printer column:

```
// +kubebuilder:printcolumn:name="Sigstore",type="boolean",JSONPath=".status.sigstoreBundleVerified"
```

**CLI flags**: Following the existing `--require-a2a-signature` / `--signature-audit-mode` pattern:

| Flag | Default | Description |
|------|---------|-------------|
| `--enable-sigstore-verification` | `false` | Enable Sigstore bundle verification |
| `--sigstore-audit-mode` | `false` | Log failures but do not block |
| `--sigstore-certificate-identity` | `""` | Expected OIDC subject in Fulcio certificates |
| `--sigstore-certificate-oidc-issuer` | `""` | Expected OIDC issuer URL |
| `--sigstore-rekor-url` | `https://rekor.sigstore.dev` | Rekor transparency log URL |
| `--sigstore-trusted-root-configmap` | `""` | ConfigMap with TrustedRoot JSON (private instances) |
| `--sigstore-trusted-root-configmap-namespace` | `""` | Namespace of the TrustedRoot ConfigMap |

**Helm values**:

```yaml
sigstore:
  cardVerification:
    enabled: false
    auditMode: false
    certificateIdentity: ""
    certificateOIDCIssuer: ""
    rekorURL: "https://rekor.sigstore.dev"
    trustedRoot:
      configMapName: ""
      configMapNamespace: ""
```

**Sigstore blob signing**: A2A agent cards are signed with Sigstore in CI (not the operator). The CI pipeline runs `cosign sign-blob` on the A2A agent card (`agent-card.json`) before deployment:

* Uses GitHub Actions OIDC token (or projected SA token) as the identity for Fulcio
* Produces a Sigstore bundle (`agent-card.sigstore.json`) alongside the A2A agent card
* Both are stored in the ConfigMap that the operator reads

The operator verifies Sigstore bundles but does not produce them. The CI pipeline is the supply-chain provenance signer; the operator is the runtime identity signer. These are separate concerns.

**Metrics**: Existing metrics (`a2a_signature_verification_total`, `a2a_signature_verification_duration_seconds`) already accept a `provider` label, so `RecordVerification("sigstore", ...)` works out of the box. Additional Sigstore-specific metrics:

| Metric | Type | Description |
|--------|------|-------------|
| `kagenti_sigstore_verification_total` | Counter | Bundle verification attempts by result and reason |
| `kagenti_sigstore_verification_duration_seconds` | Histogram | Bundle verification latency |
| `kagenti_sigstore_trusted_root_age_seconds` | Gauge | Age of the cached Sigstore trusted root |

### Phase 3: Dual-Layer Status Conditions and Backend Integration

**Scope**: Reconciler logic, backend API, UI.

#### Ready condition gate logic

Currently `Ready` is always `True`. With Sigstore, it becomes conditional:

| Sigstore state | `SigstoreVerified` condition | Effect on `Ready` |
|----------------|------------------------------|-------------------|
| Not enabled (`SigstoreVerifier == nil`) | Not set | No effect (backward compatible) |
| Enabled, bundle absent | `False` / `BundleNotFound` | No effect (graceful adoption) |
| Enabled, bundle present, verification passes | `True` / `Verified` | No effect |
| Enabled, bundle present, verification fails, audit mode on | `False` / `VerificationFailedAudit` | No effect |
| Enabled, bundle present, verification fails, audit mode off | `False` / `VerificationFailed` | `Ready` = `False` |

This mirrors the existing SPIRE audit mode semantics exactly.

#### kubectl output

```
$ kubectl get agentcards
NAME                    PROTOCOL  TARGET          VERIFIED  SIGSTORE  BOUND  SYNCED  AGE
weather-deployment-card a2a       weather-agent   true      true      true   True    5m
echo-deployment-card    a2a       echo-agent      true      false     true   True    3m
```

#### Backend integration

The kagenti backend (`kagenti/backend/app/`) surfaces AgentCard CR status to the UI. New fields to extract from the AgentCard CR status:

* `AgentCardStatusResponse`: add `sigstoreVerified`, `sigstoreIdentity`, `rekorLogIndex`
* `AgentSummary` list enrichment: add `sigstoreVerified` for the catalog view
* UI changes (displaying new fields) follow as a separate PR

### Phase 4: OCI Attestations and SPIRE Attestor Composition (Future)

> Included for context on the long-term architectural direction; not in scope for this RFC.

This phase covers two related long-term directions.

#### OCI attestations

Attach A2A agent card data as in-toto attestations to OCI images at build time:

```shell
cosign attest --predicate agent-card.json \
  --type https://kagenti.dev/agentcard/v1 \
  ghcr.io/kagenti/my-agent@sha256:...
```

The operator would fetch and verify attestations from the OCI registry, cross-referencing against the live A2A agent card data to detect drift. This requires Shipwright/Tekton Chains integration and is deferred to a follow-up design document.

#### SPIRE attestor composition

The long-term endgame, as described in [@mrsabath's PR #1038 review](https://github.com/kagenti/kagenti/pull/1038) and the [spiffe/spire#6640 discussion](https://github.com/spiffe/spire/issues/6640), is a composition model where attestors consume each other's selectors as inputs, building a chain of verified facts rather than each starting from scratch.

[kfox1111's proposal](https://github.com/spiffe/spire/issues/6640) refines the original stacking concept: instead of attestors running independently in parallel, each attestor can read selectors emitted by earlier attestors and build on their work. This eliminates duplicated logic and enables a clean separation of concerns.

**How composition would work for Kagenti:**

| Step | Attestor | Action | Selectors emitted |
|------|----------|--------|-------------------|
| 1 | K8s attestor (with Sigstore enabled) | Attests pod identity and image signature | `k8s:ns:team1`, `k8s:sa:weather-agent`, `k8s:image-signature-subject:https://github.com/kagenti/...` |
| 2 | Kagenti agent attestor (consumes k8s selectors) | Reads the AgentCard CR via pod ownerReference, verifies operator signature | `agent:name:weather-agent`, `agent:skill:weather-lookup`, `agent:model:gpt-4o` |

**Registration entry combining selectors from both attestors:**

```shell
spire-server entry create \
  -spiffeID spiffe://kagenti.dev/agent/weather-agent \
  -parentID spiffe://kagenti.dev/spire/agent/... \
  -selector k8s:ns:team1 \
  -selector k8s:image-signature-subject:https://github.com/kagenti/... \
  -selector agent:skill:weather-lookup
```

With the v2 attestor interface proposed by [@kfox1111](https://github.com/spiffe/spire/issues/6640#issuecomment-4189394765), the Sigstore image verification could also be split into its own generic attestor that reads `container:image:` from any runtime attestor (k8s, docker, or podman) and emits `cosign:valid:`. This would decouple image signature checking from the k8s attestor entirely.

**Why composition simplifies the Kagenti attestor design:**

Without composition, the Kagenti agent attestor would need to independently resolve PID to container to pod to AgentCard CR, duplicating what the k8s attestor already does. With composition, it consumes the k8s attestor's pod selectors as input and focuses only on agent-specific verification: reading the AgentCard CR, verifying the operator's signature, and emitting capability selectors.

**In this model:**

* SPIRE refuses to issue an SVID unless **all** attestor conditions pass
* No separate admission webhook or signing step is needed
* The SVID carries the full trust chain: the workload is in the right namespace, running a signed image, with a validated agent card
* Each attestor does one job well and builds on the previous attestor's verified output
* This aligns with the architecture described in the [#612 epic](https://github.com/kagenti/kagenti/issues/612): K8s attestor, Sigstore attestor, AI Agent attestor, SVID/claims

The Sigstore image attestor (Phase 1 of this RFC) is the first step toward this architecture. Three workstreams can proceed in parallel without blocking each other:

1. **Attestor composition / v2 interface design**: co-author the upstream proposal with the SPIRE community and @kfox1111
2. **Configurable error handling**: contribute the `on_attestor_error = "fail"` option to SPIRE so all configured attestors must succeed (independently useful, smaller scope)
3. **Kagenti agent attestor prototype**: build against the current SPIRE plugin interface as an external plugin, then refactor when the v2 interface lands

## 6. How This Fits in the Plan

### #612 signal mapping

| # | Signal | Status | This RFC |
|---|--------|--------|----------|
| 1 | Identity | Implemented (SPIRE x5c) | Prerequisite: card must be authenticated before provenance is verified |
| 2 | Capability | Not started | Covered by [AgentTrust RFC](agenttrust-capability-validation-integration.md) |
| 3 | Policy | Not started (OPA Gatekeeper) | Not covered |
| **4** | **Provenance** | **Designed** | **Primary target: Sigstore fills this** |
| 5 | Endorsement | Not started | Not covered |
| 6 | History | Partial (TraceSpec exists) | Partially covered: Rekor transparency log provides signing audit trail |
| 7 | Liveness | Not started | Phase 4 attestor composition contributes toward continuous verification |

### Implementation order across trust signals

| Step | What | Dependency | Effort |
|------|------|------------|--------|
| 1 | Move signer to operator | None | Medium (Go) |
| 2 | Enable SPIRE Sigstore attestor (this RFC Phase 1) | Parallel with step 1 | Low (config only) |
| 3 | Sigstore card blob verification (this RFC Phase 2B) | After step 1 | Medium-high (Go) |
| 4 | AgentTrust integration ([separate RFC](https://docs.google.com/document/d/1qK_Uu43Udl0bZvF5pDnilTa3ch_JFS21sCzOoSamXME/edit?tab=t.0)) | Parallel with step 3 | Low (operator side), high (AgentTrust side) |
| 5 | Static capability allowlist in webhook | After step 4 | Medium (Go) |

Steps 1-3 ensure the A2A agent card that AgentTrust validates against is authentic and has verified provenance. Step 4 can proceed in parallel because the operator-side changes for capability validation are independent of signing changes.

### Relationship to PR #1038

The [PR #1038 review discussion](https://github.com/kagenti/kagenti/pull/1038) established:

* Self-signing (init-container model) is experimental and parked pending the operator-signing pivot
* The UI and API surface from PR #1038 will be rebuilt once operator-signed cards are in place
* The verification, binding, and NetworkPolicy infrastructure carries over regardless of who signs

This RFC's Phase 2A (operator signing) is the prerequisite that unblocks the UI/API work.

## 7. RBAC Impact

The operator ClusterRole (`kagenti-manager-role`) already has full CRUD on ConfigMaps cluster-wide. **No RBAC changes are needed** for reading the Sigstore TrustedRoot ConfigMap or the Sigstore bundle data from the signed card ConfigMap.

For Phase 2A (operator signing), the operator needs a SPIRE registration entry granting it an X.509-SVID. This is a SPIRE configuration change, not a Kubernetes RBAC change.

## 8. Files Modified

### Phase 1 (SPIRE config only)

| File | Change |
|------|--------|
| SPIRE agent configuration | Enable Sigstore attestor in K8s workload attestor plugin |
| SPIRE registration entries | Add `k8s:image-signature-subject` selectors for agent workloads |

### Phase 2 (Go + Helm)

| File | Change |
|------|--------|
| `kagenti-operator/go.mod` | Add `sigstore-go` dependency |
| `kagenti-operator/internal/agentcard/fetcher.go` | `FetchResult` struct, extract bundle from ConfigMap |
| `kagenti-operator/internal/signature/provider.go` | Add `BundleVerifier` interface, extend `Config` |
| `kagenti-operator/internal/signature/sigstore.go` | New `SigstoreProvider` implementing `BundleVerifier` |
| `kagenti-operator/internal/signature/sigstore_test.go` | Unit tests |
| `kagenti-operator/internal/signature/metrics.go` | Add `kagenti_sigstore_*` Prometheus metrics |
| `kagenti-operator/api/v1alpha1/agentcard_types.go` | Add `SigstoreVerification` spec, status fields, printer column |
| `kagenti-operator/config/crd/bases/agent.kagenti.dev_agentcards.yaml` | Regenerated CRD |
| `charts/kagenti-operator/crds/agent.kagenti.dev_agentcards.yaml` | Copy of regenerated CRD |
| `kagenti-operator/cmd/main.go` | Add Sigstore CLI flags, wire `BundleVerifier` |
| `kagenti-operator/internal/controller/agentcard_controller.go` | Add operator signing logic in reconciler |
| `charts/kagenti-operator/values.yaml` | Add `sigstore.cardVerification` block |
| `charts/kagenti-operator/templates/manager/manager.yaml` | Add Sigstore flag conditionals |

### Phase 3 (Go + Python)

| File | Change |
|------|--------|
| `kagenti-operator/internal/controller/agentcard_controller.go` | Dual-provider verification, `SigstoreVerified` condition, `Ready` gating |
| `kagenti/backend/app/models/responses.py` | Add `sigstoreVerified`, `sigstoreIdentity`, `rekorLogIndex` |
| `kagenti/backend/app/routers/agents.py` | Extract new status fields from AgentCard CR |

## 9. Alternatives Considered

### Replace SPIRE with Sigstore entirely

Rejected. SPIRE provides runtime identity (mTLS, continuous attestation, trust domain federation) that Sigstore does not. Sigstore provides supply-chain provenance that SPIRE does not. They are complementary.

### Force Sigstore into the existing Provider interface

Rejected. The `Provider` interface is shaped for A2A agent card JWS verification (`*AgentCardData` + `[]AgentCardSignature`). Sigstore operates on raw bytes + Sigstore bundle. Forcing it in would be a leaky abstraction. A separate `BundleVerifier` interface follows Interface Segregation.

### Use cosign CLI for verification in the operator

Rejected. The operator is a long-running process; shelling out to a CLI on every reconcile adds latency, process overhead, and a binary dependency. The `sigstore-go` library is the right choice for programmatic verification.

### Use Sigstore Policy Controller for image verification

Not selected for Phase 1. The Policy Controller is a valid approach but requires deploying a separate admission webhook with its own lifecycle, upgrades, and failure modes. The SPIRE K8s attestor's Sigstore feature achieves the same goal (blocking unsigned images) with zero new infrastructure, since SPIRE is already deployed. The Policy Controller remains viable for environments that do not use SPIRE or where the SPIRE Sigstore attestor feature is not available.

### Keep init-container signing model

Rejected per [PR #1038 review consensus](https://github.com/kagenti/kagenti/pull/1038). The init-container model is self-attestation: the workload signs its own A2A agent card capability claims using its own SPIRE identity. A malicious agent image can influence what gets signed. Moving signing to the operator eliminates this trust gap by having an independent platform component attest to workload facts. See [@mrsabath's trust model analysis](https://github.com/kagenti/kagenti/pull/1038) for the detailed rationale.

### Bundle Sigstore Policy Controller as a sub-chart

Rejected. The Policy Controller is a cluster-wide admission webhook with its own lifecycle, upgrades, and failure modes. If used, it should be installed and managed independently, not bundled in the operator chart.

## 10. Open Questions

1. **Per-card vs. operator-level Sigstore identity**: Should `spec.sigstoreVerification.certificateIdentity` override the operator-level `--sigstore-certificate-identity` flag (mirroring how `spec.identityBinding.trustDomain` overrides `--spire-trust-domain`)?

2. **Private Sigstore instance support**: For air-gapped deployments, the `SigstoreProvider` needs to load a custom TrustedRoot from a ConfigMap instead of fetching the public Sigstore root via TUF. The design supports this, but the ConfigMap format (Sigstore protobuf JSON) and refresh mechanism need validation.

3. **SPIRE Sigstore attestor maturity**: The Sigstore feature in the SPIRE K8s attestor is marked experimental. Phase 1 should track the upstream feature status and have a fallback plan (Policy Controller) if the feature is removed or changed.
