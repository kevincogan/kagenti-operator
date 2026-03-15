#!/usr/bin/env bash
#
# Teardown for the auto-discovery demo.
# Removes the echo-agent and any auto-created AgentCards.
#

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
NAMESPACE="${NAMESPACE:-agents}"

echo "=== AgentCard Auto-Discovery Demo Teardown ==="
echo ""

echo "Deleting echo-agent resources..."
kubectl delete -f "${SCRIPT_DIR}/k8s/echo-agent.yaml" --ignore-not-found=true 2>/dev/null || true

echo "Deleting any auto-created AgentCards for echo-agent..."
AUTOCARD=$(kubectl get agentcard -n "$NAMESPACE" -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}' 2>/dev/null | grep echo || true)
if [ -n "$AUTOCARD" ]; then
  kubectl delete agentcard "$AUTOCARD" -n "$NAMESPACE" --ignore-not-found=true 2>/dev/null || true
fi

echo ""
echo "=== Teardown Complete ==="
