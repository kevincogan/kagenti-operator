#!/bin/bash

#TAG=$(date +%Y%m%d%H%M%S)
#docker build . --tag local/kagenti-operator:${TAG} --load
#kind load docker-image --name agent-platform local/kagenti-operator:${TAG}
#kubectl -n kagenti-system set image deployment/kagenti-operator kagenti-ui-container=local/kagenti-ui:${TAG}
#kubectl rollout status -n kagenti-system deployment/kagenti-ui
#kubectl get -n kagenti-system pod -l app.kubernetes.io/instance=kagenti-operator

