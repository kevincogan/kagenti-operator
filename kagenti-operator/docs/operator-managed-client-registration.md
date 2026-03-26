# Operator-managed Keycloak client registration

This document describes the split responsibility between **kagenti-operator** and **kagenti-webhook** for registering agent workloads as OAuth clients in Keycloak and delivering credentials to AuthBridge sidecars.

The implementation lives in two repositories today (`kagenti-operator`, `kagenti-extensions` / `kagenti-webhook`); the same feature branch name is used in both until the code is consolidated into a single repo.

---

## 1. Why this change

### 1.1 Problem

By default, the mutating webhook injects a **`kagenti-client-registration`** sidecar (or embeds equivalent behavior inside the combined **authbridge** container). That sidecar:

- Runs **inside every pod**, uses workload identity (SPIFFE when SPIRE is enabled), and talks to Keycloak to register or refresh the OAuth client.
- Competes for startup ordering and resources with the application and other sidecars.

Some deployments want **Envoy / SPIFFE / AuthBridge** injection to stay **pod-local**, but prefer **client lifecycle and secrets** to be handled **centrally** by the platform: one registration path, predictable secret names, and no client-registration container in the pod.

### 1.2 Approach

Workloads **opt out** of webhook-injected client registration with a well-known label. The **operator** then:

1. Registers the workload as a Keycloak client using the **Keycloak admin API** (same conceptual contract as the sidecar).
2. Creates a **Secret** in the workload namespace with `client-id.txt` and `client-secret.txt`.
3. Annotates the pod template so the **webhook** can mount that Secret into every container that already uses the **`shared-data`** volume, at the **same paths** the sidecar used (`/shared/client-id.txt`, `/shared/client-secret.txt`).

The webhook continues to inject **proxy-init**, **envoy** / **authbridge**, and **spiffe-helper** according to existing precedence and feature gates; it only skips the **client-registration** sidecar (or the registration portion of combined authbridge) when the label opts out.

### 1.3 Benefits

- **Fewer containers** when the sidecar path is not desired.
- **Centralized registration** using namespace `keycloak-admin-secret` (already provisioned for the sidecar contract).
- **Deterministic secret naming** derived from namespace and workload name (`kagenti-opreg-<hash>`), with **owner references** to the Deployment or StatefulSet.
- **Safe ordering**: the operator creates the Secret **before** setting the pod-template annotation, so new Pods do not reference a missing Secret.
- **Admission reinvocation**: the webhook uses `reinvocationPolicy: IfNeeded` so a second pass can add Secret volume mounts if the operator annotates the template **after** the first injection.

---

## 2. How it works

### 2.1 Contract (labels and annotations)

| Key | Value | Meaning |
|-----|--------|---------|
| `kagenti.io/client-registration-inject` | `"false"` | Workload opts **out** of webhook-injected client registration; operator is expected to manage registration **if** other conditions hold. |
| `kagenti.io/client-registration-secret-name` | Secret name | Set by the operator on the **pod template**; webhook reads it from **Pod** annotations at admission time and mounts the Secret. |

The string values for the label key and the annotation key are **duplicated** in both repos and must stay in sync:

- Operator: `LabelClientRegistrationInject`, `AnnotationClientRegistrationSecretName` in `clientregistration_controller.go`.
- Webhook: `LabelClientRegistrationInject` in `constants.go`, `AnnotationClientRegistrationSecretName` in `operator_clientreg.go`.

### 2.2 Which workloads the operator reconciles

The **ClientRegistration** controller watches **Deployments** and **StatefulSets** whose pod template labels satisfy:

- `kagenti.io/client-registration-inject` is **exactly** `"false"`.
- `kagenti.io/type` is **`agent`**, or **`tool`** when the cluster feature gate **`injectTools`** is true (tools are skipped if `injectTools` is false).

Other workloads are ignored by this controller.

### 2.3 Webhook behavior

1. **Precedence** (unchanged): `kagenti.io/client-registration-inject=false` disables injection of the client-registration sidecar / registration slice in combined authbridge (`precedence.go`).
2. **After** sidecars and volumes are applied, **`ApplyOperatorClientRegSecretVolumes`** runs for **every** mutation:
   - If the pod (template) annotation `kagenti.io/client-registration-secret-name` is set, the webhook adds a **Secret volume** (`kagenti-operator-clientreg`) and **subPath mounts** for `client-id.txt` and `client-secret.txt` into **each container that already has a `shared-data` volume mount**.
3. **Reinvocation**: if the pod is already considered “injected” (e.g. envoy or proxy-init present) but operator mounts are still missing, **`NeedsOperatorClientRegVolumePatch`** returns true and the webhook applies **only** the operator Secret mounts (`authbridge_webhook.go`).

### 2.4 Operator reconcile flow (simplified)

1. Read **cluster feature gates** (`kagenti-webhook` ConfigMap in the cluster defaults namespace). If `globalEnabled` or `clientRegistration` is false, skip.
2. Read **`authbridge-config`** in the workload namespace (`KEYCLOAK_URL`, `KEYCLOAK_REALM`, `SPIRE_ENABLED`, etc.).
3. Read **`keycloak-admin-secret`** (admin username/password).
4. Compute **Keycloak client ID**:
   - If `SPIRE_ENABLED` is not true: `namespace/workloadName`.
   - If SPIRE is enabled: `spiffe://<trust-domain>/ns/<namespace>/sa/<serviceAccount>` (requires a **non-default** `serviceAccountName` and operator **`--spire-trust-domain`**).
5. **Register or fetch** the client via Keycloak admin API (`internal/keycloak`).
6. **Create or update** the credentials Secret; set **owner** to the Deployment/StatefulSet.
7. **Patch** the pod template annotation `kagenti.io/client-registration-secret-name` to the deterministic secret name.

### 2.5 Feature flags

| Component | Flag / gate | Role |
|-----------|-------------|------|
| Operator | `--enable-operator-client-registration` (default **true**) | Master switch for the ClientRegistration controller. |
| Operator | `--spire-trust-domain` | Required for SPIFFE-shaped client IDs when `authbridge-config` has `SPIRE_ENABLED=true`. |
| Webhook | `--enable-client-registration` | Cluster-wide gate for client-registration **injection** (precedence still applies). |
| Webhook | Feature gates ConfigMap | `clientRegistration`, `injectTools`, `globalEnabled`, etc., same as for injected sidecars. |

---

## 3. Requirements

### 3.1 Platform / namespace

- **`authbridge-config`** ConfigMap in the workload namespace with at least `KEYCLOAK_URL`, `KEYCLOAK_REALM`, and consistent `SPIRE_ENABLED` with the mesh.
- **`keycloak-admin-secret`** in the same namespace with `KEYCLOAK_ADMIN_USERNAME` and `KEYCLOAK_ADMIN_PASSWORD`.
- **Webhook** and **operator** versions that both implement this contract (deploy together).

### 3.2 Workload

- **Deployment** or **StatefulSet** (not bare Pods for operator ownership of Secrets).
- Pod template labels: `kagenti.io/client-registration-inject: "false"` and `kagenti.io/type: agent` or `tool` (subject to `injectTools`).
- For **SPIRE-enabled** namespaces: `spec.template.spec.serviceAccountName` must be a **dedicated** ServiceAccount (not `default`).

### 3.3 Operator configuration

- When `authbridge-config` sets `SPIRE_ENABLED=true`, configure **`--spire-trust-domain`** to match the SPIRE server trust domain (same value as used for workload SPIFFE IDs).
- Ensure the operator can read **`authbridge-config`** and **`keycloak-admin-secret`** in agent namespaces (RBAC is extended for ConfigMaps and Secrets as needed).

### 3.4 Webhook configuration

- **`reinvocationPolicy: IfNeeded`** on the mutating webhook so late annotations still get mounts.
- Pod template must eventually carry **`kagenti.io/client-registration-secret-name`** once the operator has reconciled; until then, auth consumers on `shared-data` may not see credentials (operator retries with backoff).

---

## 4. Migration strategy

### 4.1 Recommended rollout order

1. **Upgrade operator** (with ClientRegistration controller and Keycloak client package).
2. **Upgrade webhook** (operator Secret mounts + reinvocation path).
3. **Configure** `--spire-trust-domain` on the operator if agent namespaces use SPIRE (`SPIRE_ENABLED=true`).

Rolling webhook before operator can leave workloads with `client-registration-inject=false` **without** registration until the operator is available; rolling operator before webhook can create Secrets and annotations **without** mounts until the new webhook is live. Short overlap is acceptable if you migrate workloads **after** both are deployed.

### 4.2 Adopting operator-managed registration per workload

1. Ensure the namespace has `authbridge-config` and `keycloak-admin-secret`.
2. On the workload pod template, set **`kagenti.io/client-registration-inject: "false"`**.
3. If SPIRE is on, set a **dedicated** `serviceAccountName`.
4. **Restart** or roll the workload so the webhook sees the new template and the operator reconciles.

The operator will create or reuse the Keycloak client and Secret; the webhook will inject mounts on create or on reinvocation.

### 4.3 Rollback

- Remove **`kagenti.io/client-registration-inject: "false"`** (or set client-registration injection back to the default path) and **remove** the operator annotation if present.
- Roll pods so the **client-registration sidecar** (or combined authbridge with registration) runs again.
- Optionally delete operator-created Secrets named `kagenti-opreg-*` after confirming Keycloak clients are recreated by the sidecar path if needed.

Disabling **`--enable-operator-client-registration`** stops new reconciliation but does not remove existing annotations or Secrets; clean those up if you need a full rollback.

### 4.4 Keycloak client identity

Switching from **sidecar** to **operator** registration may change the **client ID** string (e.g. from SPIFFE-based to `namespace/name` when SPIRE is off, or same SPIFFE shape when SPIRE is on). Plan for **one-time** Keycloak client cleanup or renamed clients if both paths ran for the same logical workload.

### 4.5 Future consolidation

When webhook and operator live in one repository, keep this document as the single **source of truth** for the contract; co-locate constants in one package to avoid drift between annotation/label keys.

---

## 5. Related code

| Area | Location |
|------|-----------|
| Operator reconciler | `internal/controller/clientregistration_controller.go` |
| Keycloak admin client | `internal/keycloak/` |
| Operator entrypoint / flags | `cmd/main.go` |
| Webhook mounts + reinvocation | `internal/webhook/injector/operator_clientreg.go`, `pod_mutator.go`, `internal/webhook/v1alpha1/authbridge_webhook.go` |
| Injection precedence | `internal/webhook/injector/precedence.go` |

---

## 6. Operational notes

- If logs show **`cannot resolve Keycloak client id yet`** with reason **`--spire-trust-domain is required`**, configure the operator trust domain to match SPIRE (see platform docs / `kagenti-deps` `spire.trustDomain` on Kind installs).
- Operator reads **`authbridge-config`** via an **uncached API reader** because ConfigMaps may be excluded from the controller-runtime cache for scalability; this matches how the webhook resolves namespace config.
