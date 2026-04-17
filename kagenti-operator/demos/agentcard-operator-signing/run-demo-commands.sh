#!/usr/bin/env bash
##
# Run verification commands for the operator signing demo.
# Assumes setup is complete (see demo.md).
#

set -euo pipefail

NAMESPACE="${NAMESPACE:-agents}"
OPERATOR_NS="${OPERATOR_NS:-kagenti-system}"
OPERATOR_DEPLOY="${OPERATOR_DEPLOY:-kagenti-controller-manager}"

echo "=== 1. Agent Card Served via HTTP ==="
POD=$(kubectl get pods -n "$NAMESPACE" -l app.kubernetes.io/name=weather-agent \
  --field-selector=status.phase=Running -o jsonpath='{.items[0].metadata.name}')
kubectl exec -n "$NAMESPACE" "$POD" -c agent -- \
  python3 -c "import urllib.request, json; d=json.loads(urllib.request.urlopen('http://localhost:8080/.well-known/agent-card.json').read()); print(f'  Name:   {d[\"name\"]}'); print(f'  Skills: {[s[\"name\"] for s in d.get(\"skills\",[])]}')"
echo ""

echo "=== 2. Operator Signing Log ==="
kubectl logs -n "$OPERATOR_NS" deployment/"$OPERATOR_DEPLOY" 2>&1 | \
  grep "Operator signed agent card" | grep "weather-agent" | tail -1 | \
  python3 -c "import sys,json; d=json.loads(sys.stdin.read()); print(f'  Workload:  {d[\"workload\"]}'); print(f'  SPIFFE ID: {d[\"spiffeID\"]}')" 2>/dev/null || \
  echo "  (no signing log found — wait for reconciliation)"
echo ""

echo "=== 3. JWS Protected Header ==="
kubectl get agentcard weather-agent-card -n "$NAMESPACE" \
  -o jsonpath='{.status.card.signatures[0].protected}' | python3 -c "
import sys, base64, json
b64 = sys.stdin.read().strip()
if b64:
    header = json.loads(base64.urlsafe_b64decode(b64 + '=='))
    print(f'  Algorithm:  {header.get(\"alg\")}')
    print(f'  Type:       {header.get(\"typ\")}')
    print(f'  Key ID:     {header.get(\"kid\")}')
    print(f'  x5c certs:  {len(header.get(\"x5c\", []))}')
else:
    print('  (no signature yet — wait for reconciliation)')
"
echo ""

echo "=== 4. Operator Verification Status ==="
kubectl get agentcard weather-agent-card -n "$NAMESPACE" \
  -o jsonpath='{.status.conditions}' | python3 -c "
import sys, json
for c in json.loads(sys.stdin.read()):
    if c['type'] in ('SignatureVerified', 'Bound', 'Synced'):
        print(f'  {c[\"type\"]:20s} {c[\"status\"]:5s}  ({c[\"reason\"]})')
"
echo ""

echo "=== 5. Identity Binding ==="
kubectl get agentcard weather-agent-card -n "$NAMESPACE" \
  -o jsonpath='{.status}' | python3 -c "
import sys, json
s = json.loads(sys.stdin.read())
print(f'  SPIFFE ID:      {s.get(\"signatureSpiffeId\", \"(none)\")}')
print(f'  Identity Match: {s.get(\"signatureIdentityMatch\")}')
print(f'  Bound:          {s.get(\"bindingStatus\", {}).get(\"bound\")}')
"
echo ""

echo "=== 6. Signature Label ==="
LABEL=$(kubectl get deployment weather-agent -n "$NAMESPACE" \
  -o jsonpath='{.spec.template.metadata.labels.agent\.kagenti\.dev/signature-verified}')
echo "  agent.kagenti.dev/signature-verified: ${LABEL:-<not set>}"
echo ""

echo "=== 7. AgentCard Summary ==="
kubectl get agentcard -n "$NAMESPACE"
