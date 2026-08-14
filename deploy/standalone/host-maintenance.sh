#!/usr/bin/env bash
set -euo pipefail

apply=false
prune_build_cache=false
for argument in "$@"; do
  case "$argument" in
    --apply) apply=true ;;
    --prune-build-cache) prune_build_cache=true ;;
    *) echo "usage: $0 [--apply] [--prune-build-cache]" >&2; exit 2 ;;
  esac
done

command -v docker >/dev/null 2>&1 || { echo "docker is required" >&2; exit 1; }

declare -a container_targets=()
while IFS= read -r container_id; do
  [[ "$container_id" =~ ^[a-f0-9]{12,64}$ ]] || continue
  name="$(docker inspect --format '{{.Name}}' "$container_id")"
  state="$(docker inspect --format '{{.State.Status}}' "$container_id")"
  if [[ "$name" == /ocrypt-* && ( "$state" == exited || "$state" == created || "$state" == dead ) ]]; then
    container_targets+=("$container_id")
    printf 'stopped container: %s %s %s\n' "$container_id" "$state" "$name"
  fi
done < <(docker container ls --all --quiet)

declare -a image_targets=()
while IFS= read -r image_id; do
  [[ "$image_id" =~ ^sha256:[a-f0-9]{64}$ ]] || continue
  source_label="$(docker image inspect --format '{{index .Config.Labels "org.opencontainers.image.source"}}' "$image_id")"
  if [[ "$source_label" == "https://github.com/calmshv-star/ocrypt" ]]; then
    image_targets+=("$image_id")
    printf 'dangling ocrypt image: %s\n' "$image_id"
  fi
done < <(docker image ls --filter dangling=true --quiet --no-trunc)

if [[ "$apply" != true ]]; then
  printf 'dry run: %d stopped containers and %d dangling ocrypt images; pass --apply to remove them\n' "${#container_targets[@]}" "${#image_targets[@]}"
  docker system df
  exit 0
fi

for container_id in "${container_targets[@]}"; do
  docker container rm "$container_id" >/dev/null
done
for image_id in "${image_targets[@]}"; do
  docker image rm "$image_id" >/dev/null
done
if [[ "$prune_build_cache" == true ]]; then
  docker builder prune --force --filter until=168h
fi

printf 'removed %d stopped ocrypt containers and %d dangling ocrypt images\n' "${#container_targets[@]}" "${#image_targets[@]}"
docker system df
