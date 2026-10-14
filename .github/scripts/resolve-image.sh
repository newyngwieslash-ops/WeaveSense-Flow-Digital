#!/usr/bin/env bash
set -euo pipefail

image_ref=${1:?image reference is required}
error_file=$(mktemp)
trap 'rm -f "$error_file"' EXIT

if digest=$(docker buildx imagetools inspect "$image_ref" --format '{{.Manifest.Digest}}' 2>"$error_file"); then
  [[ "$digest" =~ ^sha256:[0-9a-f]{64}$ ]] || { echo "Invalid existing image digest" >&2; exit 1; }
  printf 'found=true\ndigest=%s\n' "$digest" >> "${GITHUB_OUTPUT:?}"
elif grep -Fxq "ERROR: $image_ref: not found" "$error_file" \
  || grep -Eqi '^ERROR:.*: (manifest unknown|name unknown)(:.*)?$' "$error_file" \
  || grep -Eqi 'unexpected status from HEAD request.*: 403 Forbidden' "$error_file" \
  || grep -Fxqi 'denied' "$error_file"; then
  # GHCR 对尚未创建的包常返回 403 Forbidden / denied，而不是 not found。
  # 把它当作缓存未命中，交给后续 build-push；真正的推送鉴权失败会在那一步暴露。
  echo 'found=false' >> "${GITHUB_OUTPUT:?}"
else
  # 网络、限流等错误不是缓存未命中，必须阻止发布。
  cat "$error_file" >&2
  exit 1
fi
