#!/bin/bash

set -euo pipefail

CLUSTER_NAME=${CLUSTER_NAME:-kagenti}
NAMESPACE=${NAMESPACE:-kagenti-system}

cd "$(dirname "$0")/../kagenti-operator"

TAG=$(date +%Y%m%d%H%M%S)
echo "Building kagenti-operator:${TAG}..."
podman build . --tag "kagenti-operator:${TAG}"

echo "Loading image into kind cluster ${CLUSTER_NAME}..."
kind load --name "${CLUSTER_NAME}" docker-image "localhost/kagenti-operator:${TAG}"

if ! kubectl get namespace "${NAMESPACE}" &>/dev/null; then
  echo "Creating namespace ${NAMESPACE}..."
  kubectl create namespace "${NAMESPACE}"
fi

echo "Updating deployment in namespace ${NAMESPACE}..."
kubectl -n "${NAMESPACE}" set image deployment/kagenti-controller-manager manager="localhost/kagenti-operator:${TAG}"

echo "Waiting for rollout..."
kubectl rollout status -n "${NAMESPACE}" deployment/kagenti-controller-manager

echo "Current pods:"
kubectl get pods -n "${NAMESPACE}" -l control-plane=controller-manager
