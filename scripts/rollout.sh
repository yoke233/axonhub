#!/usr/bin/env bash
#
# Roll out AxonHub with docker-rollout.
#
# Assumes the server has already been switched from the legacy fixed-port
# `axonhub` container to the Traefik + scalable `api` compose layout.
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
ROLLOUT_TIMEOUT="${ROLLOUT_TIMEOUT:-180}"

if ! docker rollout --help >/dev/null 2>&1; then
  echo "docker rollout plugin is not installed." >&2
  echo "Install it from https://github.com/wowu/docker-rollout before deploying." >&2
  exit 1
fi

docker compose -p "${PROJECT_NAME}" -f "${COMPOSE_FILE}" config --quiet
docker compose -p "${PROJECT_NAME}" -f "${COMPOSE_FILE}" pull api
docker compose -p "${PROJECT_NAME}" -f "${COMPOSE_FILE}" up -d --no-deps traefik

# Keep rollout's baseline deterministic. If an interrupted deployment left old
# containers around, this brings the steady-state replica count back first.
docker compose -p "${PROJECT_NAME}" -f "${COMPOSE_FILE}" up -d --no-deps --no-recreate \
  --scale "api=${API_REPLICAS}" api

docker rollout -p "${PROJECT_NAME}" -f "${COMPOSE_FILE}" api \
  --timeout "${ROLLOUT_TIMEOUT}"

docker container prune -f --filter "label=com.docker.compose.project=${PROJECT_NAME}" >/dev/null 2>&1 || true

running="$(docker compose -p "${PROJECT_NAME}" -f "${COMPOSE_FILE}" ps -q api | wc -l | tr -d ' ')"
echo "api running after rollout: ${running} (expected ${API_REPLICAS})"
