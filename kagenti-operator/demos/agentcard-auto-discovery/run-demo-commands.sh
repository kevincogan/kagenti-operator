#!/usr/bin/env bash
##
# Auto-discovery demo: sync controller creates AgentCards for labeled workloads.
#

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
NAMESPACE="${NAMESPACE:-agents}"

echo "=== 1. Before: AgentCards in namespace ==="
kubectl get agentcard -n "$NAMESPACE" --no-headers 2>/dev/null || echo "  (none)"
echo ""

echo "=== 2. Deploying echo-agent (labeled, no AgentCard CR) ==="
kubectl apply -f "${SCRIPT_DIR}/k8s/echo-agent.yaml"
echo ""

echo "Waiting for pod to become ready..."
kubectl rollout status deployment/echo-agent -n "$NAMESPACE" --timeout=120s
echo ""

echo "Waiting 30s for sync controller to discover the workload..."
sleep 30

echo "=== 3. Auto-Created AgentCards ==="
kubectl get agentcard -n "$NAMESPACE"
echo ""

AUTOCARD=$(kubectl get agentcard -n "$NAMESPACE" -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}' | grep echo || true)
if [ -n "$AUTOCARD" ]; then
  echo "=== 4. Auto-Created Card Details ==="
  kubectl get agentcard "$AUTOCARD" -n "$NAMESPACE" -o jsonpath='{.metadata.name}' | xargs -I{} echo "  Name:      {}"
  kubectl get agentcard "$AUTOCARD" -n "$NAMESPACE" -o jsonpath='  TargetRef: {.spec.targetRef.kind}/{.spec.targetRef.name}'
  echo ""
else
  echo "=== 4. Auto-Created Card Details ==="
  echo "  (no auto-created card found for echo-agent)"
fi
echo ""

echo "=== 5. Cleanup ==="
kubectl delete -f "${SCRIPT_DIR}/k8s/echo-agent.yaml" --ignore-not-found=true 2>/dev/null || true
if [ -n "$AUTOCARD" ]; then
  kubectl delete agentcard "$AUTOCARD" -n "$NAMESPACE" --ignore-not-found=true 2>/dev/null || true
fi
sleep 5
echo "  echo-agent resources deleted"
