package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	defaultAddr     = ":18090"
	defaultDuration = 30
	defaultText     = "这是一段用于模拟上游AI服务慢速流式输出的内容。"
)

type chatRequest struct {
	Model    string        `json:"model"`
	Stream   bool          `json:"stream"`
	Messages []chatMessage `json:"messages"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

type mockConfig struct {
	Duration       int
	FirstByteDelay int
	StallAfter     int
	Stall          int
	Text           string
	Status         int
}

func main() {
	addr := flag.String("addr", defaultAddr, "listen address")
	flag.Parse()

	mux := http.NewServeMux()
	mux.HandleFunc("/health", handleHealth)
	mux.HandleFunc("/v1/models", handleModels)
	mux.HandleFunc("/v1/chat/completions", handleChatCompletions)

	server := &http.Server{
		Addr:              *addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	log.Printf("mock ai provider listening on %s", *addr)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

func handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("ok"))
}

func handleModels(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"object": "list",
		"data": []any{
			map[string]any{
				"id":       "mock-slow",
				"object":   "model",
				"created":  time.Now().Unix(),
				"owned_by": "axonhub-mock",
			},
		},
	})
}

func handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	defer r.Body.Close()

	var req chatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("invalid json: %v", err), http.StatusBadRequest)
		return
	}
	if req.Model == "" {
		req.Model = "mock-slow"
	}

	cfg := parseMockConfig(mockControlPrompt(req.Messages))
	if cfg.Status >= 400 {
		writeJSON(w, cfg.Status, map[string]any{
			"error": map[string]any{
				"message": fmt.Sprintf("mock status %d", cfg.Status),
				"type":    "mock_error",
				"code":    cfg.Status,
			},
		})
		return
	}

	if req.Stream {
		streamChatCompletion(w, r.Context(), req.Model, cfg)
		return
	}

	select {
	case <-r.Context().Done():
		return
	case <-time.After(time.Duration(cfg.Duration) * time.Second):
	}

	content := repeatRunes(cfg.Text, max(cfg.Duration, 1))
	writeJSON(w, http.StatusOK, map[string]any{
		"id":      "chatcmpl-mock",
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   req.Model,
		"choices": []any{
			map[string]any{
				"index": 0,
				"message": map[string]any{
					"role":    "assistant",
					"content": content,
				},
				"finish_reason": "stop",
			},
		},
	})
}

func streamChatCompletion(w http.ResponseWriter, ctx context.Context, model string, cfg mockConfig) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	if cfg.FirstByteDelay > 0 {
		if !sleepContext(ctx, time.Duration(cfg.FirstByteDelay)*time.Second) {
			return
		}
	}

	runes := []rune(cfg.Text)
	if len(runes) == 0 {
		runes = []rune(defaultText)
	}

	for i := 0; i < cfg.Duration; i++ {
		if cfg.StallAfter > 0 && cfg.Stall > 0 && i == cfg.StallAfter {
			if !sleepContext(ctx, time.Duration(cfg.Stall)*time.Second) {
				return
			}
		}

		if !sleepContext(ctx, time.Second) {
			return
		}

		ch := string(runes[i%len(runes)])
		if !writeSSE(w, flusher, streamChunk(model, map[string]any{"content": ch}, nil)) {
			return
		}
	}

	_ = writeSSE(w, flusher, streamChunk(model, map[string]any{}, "stop"))
	_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	flusher.Flush()
}

func streamChunk(model string, delta map[string]any, finishReason any) map[string]any {
	return map[string]any{
		"id":      "chatcmpl-mock",
		"object":  "chat.completion.chunk",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []any{
			map[string]any{
				"index":         0,
				"delta":         delta,
				"finish_reason": finishReason,
			},
		},
	}
}

func writeSSE(w http.ResponseWriter, flusher http.Flusher, payload any) bool {
	data, err := json.Marshal(payload)
	if err != nil {
		return false
	}
	if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
		return false
	}
	flusher.Flush()
	return true
}

func parseMockConfig(prompt string) mockConfig {
	cfg := mockConfig{
		Duration: defaultDuration,
		Text:     defaultText,
	}

	cfg.Duration = parseInt(prompt, cfg.Duration,
		`mock:duration=(\d+)`,
		`mock:seconds=(\d+)`,
		`持续\s*(\d+)\s*秒`,
		`总时间\s*(\d+)\s*秒`,
	)
	cfg.FirstByteDelay = parseInt(prompt, 0,
		`mock:first_byte_delay=(\d+)`,
		`mock:firstByteDelay=(\d+)`,
	)
	cfg.StallAfter = parseInt(prompt, 0,
		`mock:stall_after=(\d+)`,
		`mock:stallAfter=(\d+)`,
	)
	cfg.Stall = parseInt(prompt, 0,
		`mock:stall=(\d+)`,
	)
	cfg.Status = parseInt(prompt, 0,
		`mock:status=(\d+)`,
	)
	cfg.Text = parseText(prompt, cfg.Text)

	if cfg.Duration < 0 {
		cfg.Duration = defaultDuration
	}

	return cfg
}

func parseInt(s string, def int, patterns ...string) int {
	for _, pattern := range patterns {
		re := regexp.MustCompile(pattern)
		matches := re.FindAllStringSubmatch(s, -1)
		if len(matches) == 0 {
			continue
		}
		last := matches[len(matches)-1]
		if len(last) < 2 {
			continue
		}
		value, err := strconv.Atoi(last[1])
		if err != nil {
			return def
		}
		return value
	}
	return def
}

func parseText(prompt string, def string) string {
	re := regexp.MustCompile(`(?m)mock:text=([^\r\n]+)`)
	matches := re.FindStringSubmatch(prompt)
	if len(matches) < 2 {
		return def
	}
	text := strings.TrimSpace(matches[1])
	if nextControl := strings.Index(text, " mock:"); nextControl >= 0 {
		text = strings.TrimSpace(text[:nextControl])
	}
	if text == "" {
		return def
	}
	return text
}

func mockControlPrompt(messages []chatMessage) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role != "" && messages[i].Role != "user" {
			continue
		}
		if text := messageText(messages[i]); text != "" {
			return text
		}
	}

	if len(messages) == 0 {
		return ""
	}

	return messageText(messages[len(messages)-1])
}

func messageText(message chatMessage) string {
	switch content := message.Content.(type) {
	case string:
		return content
	case []any:
		var parts []string
		for _, item := range content {
			if text := textFromContentPart(item); text != "" {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, "\n")
	default:
		if content != nil {
			return fmt.Sprint(content)
		}
		return ""
	}
}

func textFromContentPart(part any) string {
	obj, ok := part.(map[string]any)
	if !ok {
		return ""
	}
	text, _ := obj["text"].(string)
	return text
}

func repeatRunes(s string, count int) string {
	runes := []rune(s)
	if len(runes) == 0 || count <= 0 {
		return ""
	}

	var b strings.Builder
	for i := 0; i < count; i++ {
		b.WriteRune(runes[i%len(runes)])
	}
	return b.String()
}

func sleepContext(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}
