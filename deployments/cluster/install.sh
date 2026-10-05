#!/bin/sh
# Installs Argo CD on the cluster next door, and exits.
#
# It is the only imperative step in this stack. Everything Argo CD then deploys
# is a commit in a repository, which is the whole demonstration: after this
# runs, nothing is applied by hand again.
#
# Idempotent on purpose. `make cluster-up` on a cluster that already has Argo
# CD must be a no-op rather than an error, because the first thing anybody does
# after reading a log is run the command again.
set -eu

export KUBECONFIG=/tmp/kubeconfig.yaml

# The kubeconfig k3s writes says 127.0.0.1, which is this container and not the
# cluster. Rewriting it here rather than asking k3s for a different one keeps
# the file correct for somebody on the HOST, who does reach it on 127.0.0.1.
sed 's#https://127.0.0.1:6443#https://k3s:6443#' /kube/kubeconfig.yaml > "$KUBECONFIG"

echo "==> cluster"
kubectl get nodes

echo "==> Argo CD ${ARGOCD_VERSION}"
kubectl create namespace argocd --dry-run=client -o yaml | kubectl apply -f -
kubectl apply -n argocd \
  -f "https://raw.githubusercontent.com/argoproj/argo-cd/${ARGOCD_VERSION}/manifests/install.yaml"

# A NodePort, because a published container port is the only way into the UI
# from the host that does not need a port-forward somebody keeps alive. It is
# patched rather than declared so the upstream manifest stays the upstream
# manifest -- pinned, unedited, and replaceable by bumping one variable.
echo "==> the UI on :30080"
kubectl -n argocd patch svc argocd-server --type merge -p \
  '{"spec":{"type":"NodePort","ports":[{"name":"http","port":80,"targetPort":8080,"nodePort":30080}]}}'

# Insecure, which is correct HERE and nowhere else: the UI is served over plain
# HTTP on a throwaway cluster on a developer's machine, and the alternative is
# a self-signed certificate every browser argues with on the first visit.
kubectl -n argocd patch configmap argocd-cmd-params-cm --type merge -p \
  '{"data":{"server.insecure":"true"}}'
kubectl -n argocd rollout restart deployment argocd-server

echo "==> waiting for Argo CD to be ready"
kubectl -n argocd rollout status deployment/argocd-server --timeout=300s
kubectl -n argocd rollout status deployment/argocd-repo-server --timeout=300s

echo
echo "Argo CD is up, with NOTHING deployed. That is the starting point:"
echo "the cluster exists and nobody has applied anything to it."
echo
echo "  make cluster-ui    the URL and the password"
