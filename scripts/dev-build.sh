#!/bin/bash

set -e

cd "$(dirname "$0")/../kagenti-operator"

TAG=$(date +%Y%m%d%H%M%S)
echo "Building kagenti-operator:${TAG}..."
docker build . --tag local/kagenti-operator:${TAG} --load

echo "Loading image into kind cluster..."
kind load --name kagenti docker-image local/kagenti-operator:${TAG}

echo "Updating deployment..."
kubectl -n kagenti-system set image deployment/kagenti-controller-manager manager=local/kagenti-operator:${TAG}

# Local Dockerfile builds to /manager, but production images (built with ko) use /ko-app/cmd.
# Override the command for local dev. See: docs/identity-binding-quickstart.md
kubectl -n kagenti-system patch deployment kagenti-controller-manager \
  --type='json' -p='[{"op": "replace", "path": "/spec/template/spec/containers/0/command", "value": ["/manager"]}]'

echo "Waiting for rollout..."
kubectl rollout status -n kagenti-system deployment/kagenti-controller-manager

echo "Current pods:"
kubectl get pods -n kagenti-system -l control-plane=controller-manager
