#!/bin/sh
# Validate templates with public example configuration only; never contact a cluster.
set -eu
cd "$(dirname "$0")/.."
mkdir -p .verification
work=$(mktemp -d "$PWD/.verification/deploy.XXXXXX")
trap 'rm -rf "$work"' EXIT HUP INT TERM

docker compose --env-file /dev/null -f compose.yaml config --quiet
docker compose --env-file /dev/null -f deploy/docker/compose.yaml config --quiet
for config in deploy/docker/config.example.yaml deploy/kubernetes/config.example.yaml; do
    ./bin/kallisti-rss -config "$config" -check-config
done

mkdir -p "$work/kubernetes"
for file in kustomization.yaml namespace.yaml deployment.yaml pvc.yaml networkpolicy.yaml; do
    cp "deploy/kubernetes/$file" "$work/kubernetes/$file"
done
cp deploy/kubernetes/config.example.yaml "$work/kubernetes/config.yaml"
cp -R deploy/kubernetes/components "$work/kubernetes/components"
go run sigs.k8s.io/kustomize/kustomize/v5@v5.8.1 build "$work/kubernetes" > "$work/rendered.yaml"
go run github.com/yannh/kubeconform/cmd/kubeconform@v0.7.0 \
    -strict -summary -kubernetes-version 1.35.0 "$work/rendered.yaml"

# Render again with the optional egress component enabled.
printf 'components:\n  - components/egress-public\n' >> "$work/kubernetes/kustomization.yaml"
go run sigs.k8s.io/kustomize/kustomize/v5@v5.8.1 build "$work/kubernetes" > "$work/rendered-egress.yaml"
grep -q 'name: kallisti-rss-egress' "$work/rendered-egress.yaml"
go run github.com/yannh/kubeconform/cmd/kubeconform@v0.7.0 \
    -strict -summary -kubernetes-version 1.35.0 "$work/rendered-egress.yaml"
printf 'Compose, example config and Kubernetes schema validation passed.\n'
