#!/usr/bin/env python3
"""
Test whether an OpenAI-compatible endpoint accepts explicit cache markers
after tool results.

Edit API_KEY / BASE_URL / MODEL below, or set environment variables:

  $env:DASHSCOPE_API_KEY = "sk-..."
  $env:OPENAI_BASE_URL = "https://dashscope.aliyuncs.com/compatible-mode/v1"
  $env:MODEL = "qwen3.7-max"
  python scripts/cache/test_explicit_cache_tool.py

The script sends each case twice:
1. cache_control directly on the role=tool message content array.
2. role=tool content as a string, cache_control on the next role=user message.

The second case is the safer fallback if the endpoint or client rejects array
content on tool messages.
"""

from __future__ import annotations

import argparse
import json
import os
import ssl
import sys
import time
import urllib.error
import urllib.request
from typing import Any


# Fill these in, or use the environment variables shown above.
API_KEY = os.getenv("DASHSCOPE_API_KEY", "PUT_YOUR_API_KEY_HERE")
BASE_URL = os.getenv("OPENAI_BASE_URL", "PUT_YOUR_BASE_URL_HERE")
MODEL = os.getenv("MODEL", "qwen3.7-max")


LONG_SYSTEM_TEXT = (
    "你是一个显式缓存兼容性测试助手。"
    "请严格根据后续工具返回内容回答，不要编造。"
    "以下填充文本用于让缓存前缀超过一千零二十四个 token。"
    "缓存测试要求相同前缀在两次请求中完全一致。"
    "字段顺序、工具调用 ID、工具返回内容都必须保持稳定。"
    "\n"
) * 420

TOOL_RESULT_TEXT = (
    "工具返回：项目 axonhub 是统一 AI API 网关。"
    "本段模拟一次较长且稳定的工具输出，用于测试 role=tool 后的显式缓存 marker。"
    "如果服务端接受 tool message 上的 cache_control，第一次请求应创建缓存，"
    "第二次请求应出现 cached_tokens 或 cache_read_input_tokens。"
    "\n"
) * 120


def endpoint_url(base_url: str) -> str:
    base = base_url.strip().rstrip("/")
    if base.endswith("/chat/completions"):
        return base
    return f"{base}/chat/completions"


def usage_numbers(resp: dict[str, Any]) -> dict[str, int | None]:
    usage = resp.get("usage") or {}
    details = usage.get("prompt_tokens_details") or {}

    return {
        "prompt_tokens": usage.get("prompt_tokens"),
        "cache_creation_input_tokens": details.get("cache_creation_input_tokens"),
        "cached_tokens": details.get("cached_tokens"),
        "cache_read_input_tokens": details.get("cache_read_input_tokens"),
    }


def request_chat(payload: dict[str, Any], timeout: int) -> tuple[int, dict[str, Any]]:
    body = json.dumps(payload, ensure_ascii=False).encode("utf-8")
    req = urllib.request.Request(
        endpoint_url(BASE_URL),
        data=body,
        method="POST",
        headers={
            "Authorization": f"Bearer {API_KEY}",
            "Content-Type": "application/json",
        },
    )

    try:
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            raw = resp.read().decode("utf-8", errors="replace")
            return resp.status, json.loads(raw)
    except urllib.error.HTTPError as exc:
        raw = exc.read().decode("utf-8", errors="replace")
        try:
            parsed: dict[str, Any] = json.loads(raw)
        except json.JSONDecodeError:
            parsed = {"raw_error": raw}
        return exc.code, parsed
    except urllib.error.URLError as exc:
        return 0, {
            "network_error": str(exc.reason),
            "hint": (
                "The request did not reach the API handler. If you see an SSL "
                "EOF/WRONG_VERSION_NUMBER error on a custom :5000 endpoint, the "
                "server is usually plain HTTP. Try changing BASE_URL from "
                "https://... to http://...."
            ),
        }
    except ssl.SSLError as exc:
        return 0, {
            "network_error": str(exc),
            "hint": (
                "TLS handshake failed before the API request was sent. Check "
                "whether the endpoint actually supports HTTPS on this port; "
                "otherwise use http://."
            ),
        }


def tool_call_id(case_tag: str) -> str:
    return f"call_cache_test_{case_tag}"


def stable_tool_call_message(case_tag: str) -> dict[str, Any]:
    return {
        "role": "assistant",
        "content": None,
        "tool_calls": [
            {
                "id": tool_call_id(case_tag),
                "type": "function",
                "function": {
                    "name": "read_project_context",
                    "arguments": json.dumps(
                        {"target": "explicit-cache-tool-result", "case": case_tag},
                        separators=(",", ":"),
                    ),
                },
            }
        ],
    }


def base_messages(case_tag: str) -> list[dict[str, Any]]:
    return [
        {"role": "system", "content": f"[case:{case_tag}]\n{LONG_SYSTEM_TEXT}"},
        {"role": "user", "content": "请读取项目上下文，然后回答缓存兼容性问题。"},
        stable_tool_call_message(case_tag),
    ]


def payload_tool_marker(question: str) -> dict[str, Any]:
    case_tag = "tool_marker"
    messages = base_messages(case_tag)
    messages.extend(
        [
            {
                "role": "tool",
                "tool_call_id": tool_call_id(case_tag),
                "content": [
                    {
                        "type": "text",
                        "text": TOOL_RESULT_TEXT,
                        "cache_control": {"type": "ephemeral"},
                    }
                ],
            },
            {"role": "user", "content": question},
        ]
    )
    return {
        "model": MODEL,
        "messages": messages,
        "temperature": 0,
        "extra_body": {"enable_thinking": False},
    }


def payload_user_marker_after_tool(question: str) -> dict[str, Any]:
    case_tag = "user_marker_after_tool"
    messages = base_messages(case_tag)
    messages.extend(
        [
            {
                "role": "tool",
                "tool_call_id": tool_call_id(case_tag),
                "content": TOOL_RESULT_TEXT,
            },
            {
                "role": "user",
                "content": [
                    {
                        "type": "text",
                        "text": (
                            "以上是稳定的工具返回内容。请把从请求开头到这里的"
                            "完整上下文作为显式缓存前缀。"
                        ),
                        "cache_control": {"type": "ephemeral"},
                    }
                ],
            },
            {"role": "assistant", "content": "已读取并固定上述工具返回内容。"},
            {"role": "user", "content": question},
        ]
    )
    return {
        "model": MODEL,
        "messages": messages,
        "temperature": 0,
        "extra_body": {"enable_thinking": False},
    }


def print_response(label: str, round_no: int, status: int, resp: dict[str, Any]) -> None:
    print(f"\n[{label}] round {round_no}: HTTP {status}")
    if status >= 400:
        print(json.dumps(resp, ensure_ascii=False, indent=2)[:4000])
        return

    print(json.dumps(usage_numbers(resp), ensure_ascii=False, indent=2))
    content = (
        ((resp.get("choices") or [{}])[0].get("message") or {}).get("content")
        if isinstance(resp.get("choices"), list)
        else None
    )
    if content:
        print(f"assistant: {str(content)[:300]}")


def run_case(label: str, make_payload: Any, timeout: int, sleep_seconds: float) -> None:
    questions = [
        "第一次请求：请用一句话说明工具返回内容。",
        "第二次请求：请用一句话说明缓存是否应该命中。",
    ]
    for idx, question in enumerate(questions, start=1):
        status, resp = request_chat(make_payload(question), timeout)
        print_response(label, idx, status, resp)
        if status >= 400:
            print(f"[{label}] stopped because the endpoint rejected this payload.")
            return
        if idx == 1 and sleep_seconds > 0:
            time.sleep(sleep_seconds)


def validate_config() -> None:
    missing = []
    if not API_KEY or API_KEY == "PUT_YOUR_API_KEY_HERE":
        missing.append("API_KEY or DASHSCOPE_API_KEY")
    if not BASE_URL or BASE_URL == "PUT_YOUR_BASE_URL_HERE":
        missing.append("BASE_URL or OPENAI_BASE_URL")
    if missing:
        raise SystemExit(
            "Missing config: "
            + ", ".join(missing)
            + "\nEdit the constants at the top of this file or set env vars."
        )


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument(
        "--case",
        choices=["tool-marker", "user-marker", "both"],
        default="both",
        help="Which cache marker placement to test.",
    )
    parser.add_argument("--timeout", type=int, default=90)
    parser.add_argument(
        "--sleep",
        type=float,
        default=1.0,
        help="Seconds to wait between request 1 and 2.",
    )
    args = parser.parse_args()

    validate_config()
    print(f"endpoint: {endpoint_url(BASE_URL)}")
    print(f"model: {MODEL}")

    if args.case in ("tool-marker", "both"):
        run_case(
            "cache_control directly on role=tool content",
            payload_tool_marker,
            args.timeout,
            args.sleep,
        )

    if args.case in ("user-marker", "both"):
        run_case(
            "cache_control on next role=user after tool",
            payload_user_marker_after_tool,
            args.timeout,
            args.sleep,
        )

    return 0


if __name__ == "__main__":
    sys.exit(main())
