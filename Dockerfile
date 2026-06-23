FROM --platform=$BUILDPLATFORM node:22-alpine AS frontend-builder

WORKDIR /build
RUN corepack enable && corepack prepare pnpm@10 --activate
COPY frontend/package.json frontend/pnpm-lock.yaml ./
RUN --mount=type=cache,target=/root/.local/share/pnpm/store \
    pnpm install --frozen-lockfile --ignore-scripts

COPY ./frontend .
ENV NODE_OPTIONS="--max-old-space-size=4096"
RUN pnpm build

# Copy dist to a stage with the target platform to avoid architecture mismatch
FROM alpine AS frontend-dist
COPY --from=frontend-builder /build/dist /dist

FROM golang:alpine AS backend-builder

ARG AXONHUB_BUILD_VERSION=dev

WORKDIR /build

RUN apk add --no-cache git ca-certificates tzdata

COPY go.mod go.sum ./
COPY llm/go.mod llm/go.sum llm/
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    GOTOOLCHAIN=auto go mod download

COPY . .
COPY --from=frontend-dist /dist /build/internal/server/static/dist

ENV GO111MODULE=on \
    CGO_ENABLED=0 \
    GOOS=linux

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    GOTOOLCHAIN=auto go build \
    -tags=nomsgpack \
    -ldflags "-s -w -X 'github.com/looplj/axonhub/internal/build.Version=${AXONHUB_BUILD_VERSION}' -X 'github.com/looplj/axonhub/internal/build.BuildTime=$(date -u +%Y-%m-%dT%H:%M:%SZ)'" \
    -o axonhub \
    ./cmd/axonhub

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    GOTOOLCHAIN=auto go build \
    -o mock-ai-provider \
    ./cmd/mock-ai-provider

FROM alpine

RUN apk add --no-cache ca-certificates tzdata

WORKDIR /app
COPY --from=backend-builder /build/axonhub /app/axonhub
COPY --from=backend-builder /build/mock-ai-provider /app/mock-ai-provider

EXPOSE 8090 18090
ENTRYPOINT ["/app/axonhub"]
