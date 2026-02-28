#!/usr/bin/env bash
##
# OpenClaw + Kagenti Operator AgentCard Integration Demo
#
# Prerequisites:
#   - OpenShift cluster with kagenti-operator installed
#   - SPIRE infrastructure deployed (manifests/a2a-infra/spire/)
#   - OpenClaw deployed with A2A enabled (scripts/setup.sh --with-a2a)
#   - kagenti-operator Helm chart installed with signature verification values
#
# This script verifies the integration is working by checking:
#   1. OpenClaw deployment has correct labels
#   2. AgentCard CRs are auto-created by the sync controller
#   3. Agent card is fetchable and signed
#   4. Signature verification status
#   5. Identity binding status (if configured)
#   6. Network policies (if enforcement is enabled)

set -eu

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
OPENCLAW_NS="${OPENCLAW_NS:-openclaw}"
NPS_NS="${NPS_NS:-nps-agent}"
OPERATOR_NS="${OPERATOR_NS:-kagenti-system}"

GREEN='\033[0;32m'
RED='\033[0;31m'
YELLOW='\033[1;33m'
NC='\033[0m'

pass() { echo -e "  ${GREEN}✓${NC} $1"; }
fail() { echo -e "  ${RED}✗${NC} $1"; }
warn() { echo -e "  ${YELLOW}!${NC} $1"; }
header() { echo -e "\n${GREEN}=== $1 ===${NC}"; }

# --- 1. Check Prerequisites ---
header "1. Checking Prerequisites"

if kubectl get deployment kagenti-controller-manager -n "$OPERATOR_NS" &>/dev/null; then
  pass "Kagenti operator is running in $OPERATOR_NS"
else
  fail "Kagenti operator not found in $OPERATOR_NS"
  echo "  Install with: helm install kagenti-operator ./charts/kagenti-operator -n $OPERATOR_NS --create-namespace"
  exit 1
fi

if kubectl get deployment openclaw -n "$OPENCLAW_NS" &>/dev/null; then
  pass "OpenClaw deployment found in $OPENCLAW_NS"
else
  fail "OpenClaw deployment not found in $OPENCLAW_NS"
  echo "  Deploy with: cd openclaw-infra && ./scripts/setup.sh --with-a2a"
  exit 1
fi

if kubectl get crd agentcards.agent.kagenti.dev &>/dev/null; then
  pass "AgentCard CRD is installed"
else
  fail "AgentCard CRD not found"
  exit 1
fi

# --- 2. Verify Labels ---
header "2. Verifying Deployment Labels"

DEPLOY_LABELS=$(kubectl get deployment openclaw -n "$OPENCLAW_NS" -o jsonpath='{.metadata.labels}')
if echo "$DEPLOY_LABELS" | grep -q '"kagenti.io/type":"agent"'; then
  pass "Deployment has kagenti.io/type=agent"
else
  fail "Deployment missing kagenti.io/type=agent label"
fi

POD_LABELS=$(kubectl get deployment openclaw -n "$OPENCLAW_NS" -o jsonpath='{.spec.template.metadata.labels}')
if echo "$POD_LABELS" | grep -q '"kagenti.io/type":"agent"'; then
  pass "Pod template has kagenti.io/type=agent"
else
  fail "Pod template missing kagenti.io/type=agent label"
fi

if echo "$POD_LABELS" | grep -q 'protocol.kagenti.io/a2a'; then
  pass "Pod template has protocol.kagenti.io/a2a"
else
  fail "Pod template missing protocol.kagenti.io/a2a label"
fi

# --- 3. Check SPIRE SVID ---
header "3. Checking SPIRE SVID Availability"

OPENCLAW_POD=$(kubectl get pod -n "$OPENCLAW_NS" -l app=openclaw -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || true)
if [ -n "$OPENCLAW_POD" ]; then
  # Check via the a2a-bridge container (which has Node.js and needs the SVID).
  # The spiffe-helper container is distroless and has no shell utilities.
  SVID_CHECK=$(kubectl exec "$OPENCLAW_POD" -n "$OPENCLAW_NS" -c a2a-bridge -- \
    node -e "try { require('fs').accessSync('/opt/svid.pem'); console.log('ok'); } catch(e) { console.log('missing'); }" 2>/dev/null || echo "error")
  KEY_CHECK=$(kubectl exec "$OPENCLAW_POD" -n "$OPENCLAW_NS" -c a2a-bridge -- \
    node -e "try { require('fs').accessSync('/opt/svid_key.pem'); console.log('ok'); } catch(e) { console.log('missing'); }" 2>/dev/null || echo "error")

  if [ "$SVID_CHECK" = "ok" ]; then
    pass "SVID certificate readable by a2a-bridge (/opt/svid.pem)"
  else
    warn "SVID certificate not found — signing will be disabled until SPIRE issues an SVID"
  fi

  if [ "$KEY_CHECK" = "ok" ]; then
    pass "SVID private key readable by a2a-bridge (/opt/svid_key.pem)"
  else
    warn "SVID private key not readable — a2a-bridge may need runAsUser: 0 (see troubleshooting)"
  fi
else
  warn "No openclaw pod running yet"
fi

# --- 4. Fetch Agent Card ---
header "4. Fetching Agent Card"

if [ -n "$OPENCLAW_POD" ]; then
  CARD=$(kubectl exec "$OPENCLAW_POD" -n "$OPENCLAW_NS" -c a2a-bridge -- \
    node -e "
      const http = require('http');
      http.get('http://127.0.0.1:8080/.well-known/agent.json', res => {
        let d=''; res.on('data',c=>d+=c); res.on('end',()=>console.log(d));
      }).on('error', e => { console.error(e.message); process.exit(1); });
    " 2>/dev/null || true)

  if [ -n "$CARD" ]; then
    CARD_NAME=$(echo "$CARD" | python3 -c "import sys,json; print(json.load(sys.stdin).get('name',''))" 2>/dev/null || true)
    HAS_SIG=$(echo "$CARD" | python3 -c "import sys,json; d=json.load(sys.stdin); print('yes' if d.get('signatures') else 'no')" 2>/dev/null || true)

    pass "Agent card fetched: name=$CARD_NAME"
    if [ "$HAS_SIG" = "yes" ]; then
      pass "Agent card is SIGNED (JWS with x5c chain)"
    else
      warn "Agent card is UNSIGNED — SVID may not be available yet"
    fi
  else
    fail "Could not fetch agent card from A2A bridge"
  fi
else
  warn "Skipping (no pod running)"
fi

# --- 5. Check AgentCard CRs ---
header "5. Checking AgentCard Custom Resources"

echo "  Waiting 15s for sync controller to discover workloads..."
sleep 15

CARDS=$(kubectl get agentcard -n "$OPENCLAW_NS" --no-headers 2>/dev/null || true)
if [ -n "$CARDS" ]; then
  pass "AgentCard CRs found in $OPENCLAW_NS:"
  kubectl get agentcard -n "$OPENCLAW_NS" -o wide 2>/dev/null | sed 's/^/    /'
else
  warn "No AgentCard CRs found yet — sync controller may need more time"
  echo "  Check operator logs: kubectl logs -n $OPERATOR_NS -l control-plane=controller-manager --tail=50"
fi

# --- 6. Check Signature Verification Status ---
header "6. Signature Verification Status"

AUTOCARD=$(kubectl get agentcard -n "$OPENCLAW_NS" -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}' 2>/dev/null | head -1 || true)
if [ -n "$AUTOCARD" ]; then
  VALID_SIG=$(kubectl get agentcard "$AUTOCARD" -n "$OPENCLAW_NS" -o jsonpath='{.status.validSignature}' 2>/dev/null || true)
  PROTOCOL=$(kubectl get agentcard "$AUTOCARD" -n "$OPENCLAW_NS" -o jsonpath='{.status.protocol}' 2>/dev/null || true)
  LAST_SYNC=$(kubectl get agentcard "$AUTOCARD" -n "$OPENCLAW_NS" -o jsonpath='{.status.lastSyncTime}' 2>/dev/null || true)
  SPIFFE_ID=$(kubectl get agentcard "$AUTOCARD" -n "$OPENCLAW_NS" -o jsonpath='{.status.signatureSpiffeId}' 2>/dev/null || true)
  BINDING=$(kubectl get agentcard "$AUTOCARD" -n "$OPENCLAW_NS" -o jsonpath='{.status.bindingStatus}' 2>/dev/null || true)
  CARD_NAME_STATUS=$(kubectl get agentcard "$AUTOCARD" -n "$OPENCLAW_NS" -o jsonpath='{.status.card.name}' 2>/dev/null || true)

  echo "  Card:            $AUTOCARD"
  echo "  Protocol:        $PROTOCOL"
  echo "  Last Sync:       $LAST_SYNC"
  echo "  Agent Name:      $CARD_NAME_STATUS"

  if [ "$VALID_SIG" = "true" ]; then
    pass "Signature: VALID"
    echo "  SPIFFE ID:       $SPIFFE_ID"
  elif [ "$VALID_SIG" = "false" ]; then
    fail "Signature: INVALID"
    SYNCED_COND=$(kubectl get agentcard "$AUTOCARD" -n "$OPENCLAW_NS" -o jsonpath='{.status.conditions[?(@.type=="Synced")].message}' 2>/dev/null || true)
    echo "  Reason:          $SYNCED_COND"
  else
    warn "Signature verification not yet evaluated"
  fi

  if [ -n "$BINDING" ]; then
    echo "  Binding Status:  $BINDING"
  fi

  echo ""
  echo "  Full conditions:"
  kubectl get agentcard "$AUTOCARD" -n "$OPENCLAW_NS" -o jsonpath='{range .status.conditions[*]}    {.type}: {.status} ({.reason}) - {.message}{"\n"}{end}' 2>/dev/null || true
else
  warn "No AgentCard CR to check"
fi

# --- 7. Check Network Policies ---
header "7. Network Policy Enforcement"

NP=$(kubectl get networkpolicy -n "$OPENCLAW_NS" -l managed-by=kagenti-operator --no-headers 2>/dev/null || true)
if [ -n "$NP" ]; then
  pass "Kagenti-managed NetworkPolicies found:"
  kubectl get networkpolicy -n "$OPENCLAW_NS" -l managed-by=kagenti-operator 2>/dev/null | sed 's/^/    /'
else
  warn "No kagenti-managed NetworkPolicies (enforceNetworkPolicies may be disabled)"
fi

# --- 8. NPS Agent ---
header "8. NPS Agent Integration"

if kubectl get deployment nps-agent -n "$NPS_NS" &>/dev/null; then
  pass "NPS Agent deployment found in $NPS_NS"

  NPS_POD=$(kubectl get pod -n "$NPS_NS" -l app=nps-agent -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || true)
  if [ -n "$NPS_POD" ]; then
    NPS_SVID_CHECK=$(kubectl exec "$NPS_POD" -n "$NPS_NS" -c a2a-bridge -- \
      node -e "try { require('fs').accessSync('/opt/svid.pem'); require('fs').accessSync('/opt/svid_key.pem'); console.log('ok'); } catch(e) { console.log('missing'); }" 2>/dev/null || echo "error")
    if [ "$NPS_SVID_CHECK" = "ok" ]; then
      pass "NPS A2A bridge can access SVID certificate and key"
    else
      warn "NPS A2A bridge cannot access SVID — check svid-output volume mount and permissions"
    fi

    NPS_CARD=$(kubectl exec "$NPS_POD" -n "$NPS_NS" -c a2a-bridge -- \
      node -e "
        const http = require('http');
        http.get('http://127.0.0.1:8080/.well-known/agent.json', res => {
          let d=''; res.on('data',c=>d+=c); res.on('end',()=>console.log(d));
        }).on('error', e => { console.error(e.message); process.exit(1); });
      " 2>/dev/null || true)

    if [ -n "$NPS_CARD" ]; then
      NPS_HAS_SIG=$(echo "$NPS_CARD" | python3 -c "import sys,json; d=json.load(sys.stdin); print('yes' if d.get('signatures') else 'no')" 2>/dev/null || true)
      if [ "$NPS_HAS_SIG" = "yes" ]; then
        pass "NPS agent card is SIGNED"
      else
        warn "NPS agent card is UNSIGNED"
      fi
    fi
  fi

  NPS_CARDS=$(kubectl get agentcard -n "$NPS_NS" --no-headers 2>/dev/null || true)
  if [ -n "$NPS_CARDS" ]; then
    pass "NPS Agent AgentCard CRs:"
    kubectl get agentcard -n "$NPS_NS" -o wide 2>/dev/null | sed 's/^/    /'

    NPS_AUTOCARD=$(kubectl get agentcard -n "$NPS_NS" -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || true)
    if [ -n "$NPS_AUTOCARD" ]; then
      NPS_VALID_SIG=$(kubectl get agentcard "$NPS_AUTOCARD" -n "$NPS_NS" -o jsonpath='{.status.validSignature}' 2>/dev/null || true)
      NPS_SPIFFE_ID=$(kubectl get agentcard "$NPS_AUTOCARD" -n "$NPS_NS" -o jsonpath='{.status.signatureSpiffeId}' 2>/dev/null || true)
      if [ "$NPS_VALID_SIG" = "true" ]; then
        pass "NPS Signature: VALID (SPIFFE ID: $NPS_SPIFFE_ID)"
      elif [ "$NPS_VALID_SIG" = "false" ]; then
        fail "NPS Signature: INVALID"
      else
        warn "NPS Signature verification not yet evaluated"
      fi
    fi
  else
    warn "No AgentCard CRs for NPS agent yet"
  fi
else
  warn "NPS Agent deployment not found in $NPS_NS (skipping)"
fi

# --- Summary ---
header "Summary"
echo "  OpenClaw namespace:  $OPENCLAW_NS"
echo "  NPS Agent namespace: $NPS_NS"
echo "  Operator namespace:  $OPERATOR_NS"
TOTAL_CARDS=$(kubectl get agentcard --all-namespaces --no-headers 2>/dev/null | wc -l | tr -d ' ')
echo "  Total AgentCards:    $TOTAL_CARDS"
echo ""
echo "  All AgentCards:"
kubectl get agentcard --all-namespaces -o wide 2>/dev/null | sed 's/^/    /'
echo ""
echo "  Next steps:"
echo "    - If in audit mode, check operator logs for signature verification results"
echo "    - To enable enforcement: helm upgrade with values-openclaw-enforce.yaml"
echo "    - To add identity binding: envsubst < k8s/openclaw-agentcard.yaml.envsubst | kubectl apply -f -"
echo ""
