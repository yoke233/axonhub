# AxonHub Docker Rollout Deployment

This compose layout is for `/opt/1panel/docker/compose/axonhub`.

## Structure

- `traefik` owns the host port `${HTTP_PUBLISH_PORT:-8123}`.
- `api` has no `container_name` and no host `ports`, so docker-rollout can run old and new containers at the same time.
- `api` exposes port `8090` only on the compose network.
- `/readyz` returns `503` when `/tmp/drain` exists, so Traefik stops sending new requests to the old container.
- Docker `stop_grace_period` is longer than the 30 minute LLM route timeout, and `AXONHUB_SERVER_APP_STOP_TIMEOUT` is slightly shorter than Docker's grace.

## Install docker-rollout

```sh
mkdir -p ~/.docker/cli-plugins
curl -fsSL https://raw.githubusercontent.com/wowu/docker-rollout/main/docker-rollout \
  -o ~/.docker/cli-plugins/docker-rollout
chmod +x ~/.docker/cli-plugins/docker-rollout
docker rollout --help
```

## First Switch From The Legacy Container

The current legacy compose has one fixed `axonhub` container binding `8123:8090`.
Traefik needs the same host port, so the first switch requires one short planned
restart window:

```sh
cd /opt/1panel/docker/compose/axonhub

docker compose -f docker-compose.yaml config --quiet
docker compose -f docker-compose.yaml pull api
docker compose -f docker-compose.yaml stop axonhub || true
docker compose -f docker-compose.yaml up -d traefik api
docker compose -f docker-compose.yaml ps
```

After this switch, do not use `docker compose up -d api` for releases.

## Later Releases

```sh
cd /opt/1panel/docker/compose/axonhub
./scripts/rollout.sh
```

The script:

- validates compose config;
- pulls the new `api` image;
- ensures Traefik is running;
- normalizes `api` back to `${API_REPLICAS:-1}` before rollout;
- runs `docker rollout api`;
- prunes interrupted rollout leftovers for this compose project.

Worst-case rollout time is approximately:

```text
ROLLOUT_TIMEOUT + API_DRAIN_PROPAGATION_SECONDS + API_STOP_GRACE_PERIOD
```

With the defaults, reserve at least 35 minutes for the CI/CD deployment step.
