#!/usr/bin/env bash
#
# Roll out AxonHub API containers behind Traefik without requiring a docker
# rollout plugin.
set -euo pipefail

cd "$(dirname "$0")/.."

if [ -f .env ]; then
  while IFS= read -r line || [ -n "$line" ]; do
    line="${line%$'\r'}"
    case "$line" in
      "" | \#*) continue ;;
      *=*) ;;
      *) continue ;;
    esac

    key="${line%%=*}"
    value="${line#*=}"
    if [[ ! "$key" =~ ^[A-Za-z_][A-Za-z0-9_]*$ ]]; then
      continue
    fi

    if [ "${#value}" -ge 2 ]; then
      first="${value:0:1}"
      last="${value: -1}"
      if { [ "$first" = '"' ] && [ "$last" = '"' ]; } ||
        { [ "$first" = "'" ] && [ "$last" = "'" ]; }; then
        value="${value:1:${#value}-2}"
      fi
    fi

    if [ -z "${!key+x}" ]; then
      export "$key=$value"
    fi
  done < .env
fi

COMPOSE_FILE="${COMPOSE_FILE:-docker-compose.yaml}"
PROJECT_NAME="${COMPOSE_PROJECT_NAME:-axonhub}"
API_REPLICAS="${API_REPLICAS:-1}"
AXONHUB_DRAIN_FILE="${AXONHUB_DRAIN_FILE:-/tmp/drain}"
API_DRAIN_PROPAGATION_SECONDS="${API_DRAIN_PROPAGATION_SECONDS:-10}"
API_STOP_GRACE_SECONDS="${API_STOP_GRACE_SECONDS:-1860}"

docker compose -p "${PROJECT_NAME}" -f "${COMPOSE_FILE}" config --quiet
docker compose -p "${PROJECT_NAME}" -f "${COMPOSE_FILE}" pull api
docker compose -p "${PROJECT_NAME}" -f "${COMPOSE_FILE}" up -d --no-deps traefik

mapfile -t old_ids < <(docker compose -p "${PROJECT_NAME}" -f "${COMPOSE_FILE}" ps -q api)

if [ "${#old_ids[@]}" -eq 0 ]; then
  docker compose -p "${PROJECT_NAME}" -f "${COMPOSE_FILE}" up -d --no-deps --scale "api=${API_REPLICAS}" api
  docker compose -p "${PROJECT_NAME}" -f "${COMPOSE_FILE}" ps api
  exit 0
fi

target_count=$(("${#old_ids[@]}" + API_REPLICAS))
docker compose -p "${PROJECT_NAME}" -f "${COMPOSE_FILE}" up -d --no-deps --no-recreate \
  --scale "api=${target_count}" api

wait_for_healthy() {
  local id="$1"
  local name
  name="$(docker inspect -f '{{.Name}}' "$id" | sed 's#^/##')"

  for _ in $(seq 1 90); do
    health="$(docker inspect -f '{{if .State.Health}}{{.State.Health.Status}}{{else}}healthy{{end}}' "$id" 2>/dev/null || true)"
    echo "${name} health=${health}"
    if [ "$health" = "healthy" ]; then
      return 0
    fi
    sleep 2
  done

  echo "${name} did not become healthy" >&2
  return 1
}

old_set=" ${old_ids[*]} "
mapfile -t all_ids < <(docker compose -p "${PROJECT_NAME}" -f "${COMPOSE_FILE}" ps -q api)
new_ids=()
for id in "${all_ids[@]}"; do
  if [[ "$old_set" != *" ${id} "* ]]; then
    new_ids+=("$id")
  fi
done

if [ "${#new_ids[@]}" -lt "${API_REPLICAS}" ]; then
  echo "expected at least ${API_REPLICAS} new api containers, got ${#new_ids[@]}" >&2
  exit 1
fi

for id in "${new_ids[@]}"; do
  wait_for_healthy "$id"
done

for id in "${old_ids[@]}"; do
  name="$(docker inspect -f '{{.Name}}' "$id" | sed 's#^/##')"
  echo "draining ${name}"
  docker exec "$id" sh -c "touch '${AXONHUB_DRAIN_FILE}'" >/dev/null 2>&1 || true
done

sleep "${API_DRAIN_PROPAGATION_SECONDS}"

for id in "${old_ids[@]}"; do
  name="$(docker inspect -f '{{.Name}}' "$id" | sed 's#^/##')"
  echo "stopping ${name}"
  docker stop -t "${API_STOP_GRACE_SECONDS}" "$id" >/dev/null
  docker rm "$id" >/dev/null 2>&1 || true
done

docker container prune -f --filter "label=com.docker.compose.project=${PROJECT_NAME}" >/dev/null 2>&1 || true
docker compose -p "${PROJECT_NAME}" -f "${COMPOSE_FILE}" ps api
