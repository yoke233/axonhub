package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParseMockConfig(t *testing.T) {
	cfg := parseMockConfig("总时间360秒\nmock:text=超时测试\nmock:first_byte_delay=3\nmock:stall_after=4\nmock:stall=5")

	if cfg.Duration != 360 {
		t.Fatalf("Duration = %d, want 360", cfg.Duration)
	}
	if cfg.Text != "超时测试" {
		t.Fatalf("Text = %q, want 超时测试", cfg.Text)
	}
	if cfg.FirstByteDelay != 3 {
		t.Fatalf("FirstByteDelay = %d, want 3", cfg.FirstByteDelay)
	}
	if cfg.StallAfter != 4 {
		t.Fatalf("StallAfter = %d, want 4", cfg.StallAfter)
	}
	if cfg.Stall != 5 {
		t.Fatalf("Stall = %d, want 5", cfg.Stall)
	}
}

func TestMockControlPromptUsesLatestUserMessage(t *testing.T) {
	prompt := mockControlPrompt([]chatMessage{
		{Role: "user", Content: "mock:duration=360"},
		{Role: "assistant", Content: "这是一段旧回复"},
		{Role: "user", Content: "mock:duration=10 mock:text=超时测试123"},
	})
	cfg := parseMockConfig(prompt)

	if cfg.Duration != 10 {
		t.Fatalf("Duration = %d, want 10", cfg.Duration)
	}
	if cfg.Text != "超时测试123" {
		t.Fatalf("Text = %q, want 超时测试123", cfg.Text)
	}
}

func TestHandleHealth(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()

	handleHealth(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if strings.TrimSpace(rec.Body.String()) != "ok" {
		t.Fatalf("body = %q, want ok", rec.Body.String())
	}
}

func TestHandleChatCompletionsNonStream(t *testing.T) {
	body := `{"model":"mock-slow","stream":false,"messages":[{"role":"user","content":"mock:duration=0\nmock:text=测试"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	rec := httptest.NewRecorder()

	handleChatCompletions(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"object":"chat.completion"`) {
		t.Fatalf("body does not contain chat completion: %s", rec.Body.String())
	}
}

func TestHandleChatCompletionsStreamDone(t *testing.T) {
	body := `{"model":"mock-slow","stream":true,"messages":[{"role":"user","content":"mock:duration=0"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	rec := httptest.NewRecorder()

	handleChatCompletions(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); !strings.Contains(got, "text/event-stream") {
		t.Fatalf("Content-Type = %q, want text/event-stream", got)
	}
	if !strings.Contains(rec.Body.String(), "data: [DONE]") {
		t.Fatalf("body does not contain done event: %s", rec.Body.String())
	}
}
