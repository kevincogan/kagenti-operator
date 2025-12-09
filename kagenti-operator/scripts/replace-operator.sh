#!/bin/bash

set -e
TAG=$(date +%Y%m%d%H%M%S)

docker build . --tag local/kagenti-operator:${TAG} --load
kind load docker-image --name kagenti local/kagenti-operator:${TAG}
kubectl -n kagenti-system set image deployment/kagenti-controller-manager manager=local/kagenti-operator:${TAG}

# Patch the command to use /manager instead of /ko-app/cmd
kubectl -n kagenti-system patch deployment kagenti-controller-manager --type='json' -p='[
  {
    "op": "replace",
    "path": "/spec/template/spec/containers/0/command/0",
    "value": "/manager"
  }
]'

kubectl rollout status -n kagenti-system deployment/kagenti-controller-manager
kubectl get -n kagenti-system pod -l app.kubernetes.io/name=kagenti-operator-chart
