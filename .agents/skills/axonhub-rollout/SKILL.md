---
name: axonhub-rollout
description: AxonHub production rolling deployment workflow. Use when asked to build/publish AxonHub, push Docker images, SSH to root@172.24.156.177, update /opt/1panel/docker/compose/axonhub, run rollout.sh, verify /readyz, or roll back the 1Panel Docker Compose deployment.
---

# AxonHub Rollout

## Scope

Use this skill for AxonHub production deployment from `D:\project\axonhub` to:

- Server: `root@172.24.156.177`
- Compose dir: `/opt/1panel/docker/compose/axonhub`
- Local push registry: `registry.cn-shanghai.aliyuncs.com/xiaoin/axonhub`
- Server pull registry: `registry-vpc.cn-shanghai.aliyuncs.com/xiaoin/axonhub`
- Service to roll: `api`
- Health endpoint: `http://127.0.0.1:8123/readyz`

Default to **local Docker build and push**. Do not build on the server unless the user explicitly asks.

## Preflight

1. Confirm branch and tree:

```powershell
git status --short --branch
git rev-parse --short=8 HEAD
```

2. If there are uncommitted code changes relevant to the deployment, commit and push before building.

3. Confirm server state:

```powershell
ssh root@172.24.156.177 'cd /opt/1panel/docker/compose/axonhub && sed -n "/^AXONHUB_IMAGE=/p;/^AXONHUB_TAG=/p;/^API_REPLICAS=/p" .env && docker compose -p axonhub -f docker-compose.yaml ps'
```

4. If a mistaken remote build is running, stop it before local build:

```powershell
ssh root@172.24.156.177 'ps -eo pid,ppid,cmd | sed -n "/docker buildx build/p;/buildkit\/executor/p"'
ssh root@172.24.156.177 'kill <pid> 2>/dev/null || true'
```

## Build And Publish Locally

Pick a timestamp tag in `yyyyMMdd-HHmm` format and short commit:

```powershell
$tag = Get-Date -Format 'yyyyMMdd-HHmm'
$version = git rev-parse --short=8 HEAD
```

Build Linux amd64 and push to the public Aliyun registry:

```powershell
docker buildx build --platform linux/amd64 --build-arg AXONHUB_BUILD_VERSION=$version -t registry.cn-shanghai.aliyuncs.com/xiaoin/axonhub:$tag --push .
```

If the server uses the VPC registry, verify the same tag is visible there:

```powershell
ssh root@172.24.156.177 "docker manifest inspect registry-vpc.cn-shanghai.aliyuncs.com/xiaoin/axonhub:$tag >/dev/null && echo vpc-registry-tag-ok"
```

## Roll Out On Server

Use single quotes around remote shell snippets when they contain `$` or `$(...)` so local PowerShell does not expand them first.

```powershell
$tag = '<new-tag>'
ssh root@172.24.156.177 "cd /opt/1panel/docker/compose/axonhub && cp .env .env.bak.$tag && sed -i 's/^AXONHUB_TAG=.*/AXONHUB_TAG=$tag/' .env && sed -n '/^AXONHUB_IMAGE=/p;/^AXONHUB_TAG=/p;/^API_REPLICAS=/p' .env && ./scripts/rollout.sh"
```

Expected rollout behavior:

- Pulls new `api` image.
- Starts a new `axonhub-api-N` container.
- Waits until health is `healthy`.
- Touches the old container drain file.
- Stops and removes the old `api` container.

## Verify

Run all checks:

```powershell
ssh root@172.24.156.177 'cd /opt/1panel/docker/compose/axonhub && docker compose -p axonhub -f docker-compose.yaml ps'
ssh root@172.24.156.177 'curl -fsS -i http://127.0.0.1:8123/readyz | sed -n "1,20p"'
ssh root@172.24.156.177 'docker logs --since 2m $(docker compose -p axonhub -f /opt/1panel/docker/compose/axonhub/docker-compose.yaml ps -q api) 2>&1 | tail -n 80'
```

Success criteria:

- `api` image tag is the new tag.
- Container is `healthy`.
- `/readyz` returns `HTTP/1.1 200 OK`.
- Logs show `run server` and no startup crash loop.

## Rollback

If the new container fails health or `/readyz`, restore the previous tag from `.env.bak.<tag>` or set it manually:

```powershell
$oldTag = '<previous-tag>'
ssh root@172.24.156.177 "cd /opt/1panel/docker/compose/axonhub && sed -i 's/^AXONHUB_TAG=.*/AXONHUB_TAG=$oldTag/' .env && ./scripts/rollout.sh"
```

Then repeat verification.

## Notes

- The repo rule says not to run lint/build unless explicitly requested. A user asking for deployment has implicitly requested the Docker image build required for deployment; do not run unrelated lint/build commands.
- Do not use `docker compose up -d api` for releases; use `scripts/rollout.sh`.
- Use `docker-compose.yaml` with `-p axonhub`; the directory also has legacy `docker-compose.yml`, and Docker Compose may otherwise choose the wrong file.
- The mock provider service may stay on an older tag; only the `api` service is rolled unless the user asks otherwise.
