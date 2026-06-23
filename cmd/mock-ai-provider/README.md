# Mock AI Provider

OpenAI-compatible mock provider for testing long-running upstream requests and SSE timeout behavior.

## Run Locally

```powershell
go run ./cmd/mock-ai-provider -addr :18090
```

Health check:

```powershell
Invoke-WebRequest http://127.0.0.1:18090/health
```

## Docker Compose

Start only the mock provider:

```powershell
docker compose -f docker-compose.mock.yaml up -d
```

Or start it from the main deployment file when the required AxonHub environment is already configured:

```powershell
docker compose --profile mock up -d mock-ai-provider
```

The mock provider binary is built into the main AxonHub Docker image at `/app/mock-ai-provider`.
The mock compose service reuses the same `${AXONHUB_IMAGE}:${AXONHUB_TAG}` image and overrides the entrypoint.

When AxonHub runs in the same compose network, configure the channel base URL as:

```text
http://mock-ai-provider:18090/v1
```

From the host machine, use:

```text
http://127.0.0.1:18090/v1
```

## Prompt Controls

Put controls in any chat message content.

```text
mock:duration=360
mock:text=超时测试
```

Supported controls:

| Control | Description |
| --- | --- |
| `mock:duration=360` | Stream for 360 seconds, one character per second. |
| `mock:seconds=360` | Alias for `mock:duration`. |
| `持续360秒` | Chinese duration form. |
| `总时间360秒` | Chinese duration form. |
| `mock:text=超时测试` | Text used for generated characters. |
| `mock:first_byte_delay=310` | Wait 310 seconds before the first streamed character. |
| `mock:stall_after=20` | After 20 streamed characters, pause. |
| `mock:stall=310` | Pause duration used with `mock:stall_after`. |
| `mock:status=500` | Return a mock non-streaming error status. |

## Stream Test

```powershell
curl.exe -N http://127.0.0.1:18090/v1/chat/completions `
  -H "Content-Type: application/json" `
  -d "{\"model\":\"mock-slow\",\"stream\":true,\"messages\":[{\"role\":\"user\",\"content\":\"mock:duration=10`nmock:text=超时测试\"}]}"
```

## AxonHub Test Channel

Add an OpenAI-compatible channel and point its base URL to:

```text
http://mock-ai-provider:18090/v1
```

Use any API key value for the mock provider.
