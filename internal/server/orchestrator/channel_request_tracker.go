package orchestrator

import (
	"sync"
	"time"
)

// ChannelRequestTracker tracks per-channel request and token counts
// within a fixed 1-minute sliding window for rate limiting.
// It also manages cooldown periods for channels that received 429 errors.
type ChannelRequestTracker struct {
	mu           sync.RWMutex
	counters     map[int]*rateLimitWindow // channelID -> per-minute window
	dailyTokens  map[int]*dailyTokenWindow // channelID -> per-UTC-day token counter
	cooldowns    map[int]time.Time         // channelID -> cooldown expiration time
}

type rateLimitWindow struct {
	requests    int64
	tokens      int64
	windowStart time.Time
}

// dailyTokenWindow tracks token usage in a UTC-day rolling window. Used to enforce
// per-channel daily budgets (e.g. preventing a codex subscription account from blowing
// through its quota in a single hour and getting flagged).
type dailyTokenWindow struct {
	tokens int64
	day    string // UTC date in YYYY-MM-DD form
}

// NewChannelRequestTracker creates a new rate limit tracker.
func NewChannelRequestTracker() *ChannelRequestTracker {
	return &ChannelRequestTracker{
		counters:    make(map[int]*rateLimitWindow),
		dailyTokens: make(map[int]*dailyTokenWindow),
		cooldowns:   make(map[int]time.Time),
	}
}

func currentUTCDay() string {
	return time.Now().UTC().Format("2006-01-02")
}

// AddDailyTokens adds token usage to the per-channel UTC-day rolling counter.
// Negative or zero increments are ignored.
func (t *ChannelRequestTracker) AddDailyTokens(channelID int, tokens int64) {
	if tokens <= 0 {
		return
	}

	day := currentUTCDay()
	t.mu.Lock()
	defer t.mu.Unlock()

	w, ok := t.dailyTokens[channelID]
	if !ok || w.day != day {
		w = &dailyTokenWindow{day: day}
		t.dailyTokens[channelID] = w
	}
	w.tokens += tokens
}

// GetDailyTokenCount returns the token count consumed today (UTC) for a channel.
// Returns 0 if the channel has no usage today or the previous day's counter has
// rolled over.
func (t *ChannelRequestTracker) GetDailyTokenCount(channelID int) int64 {
	t.mu.RLock()
	w, ok := t.dailyTokens[channelID]
	t.mu.RUnlock()
	if !ok {
		return 0
	}
	if w.day != currentUTCDay() {
		return 0
	}
	return w.tokens
}

// getOrResetWindow returns the current window for a channel, resetting if expired.
func (t *ChannelRequestTracker) getOrResetWindow(channelID int) *rateLimitWindow {
	now := time.Now()
	windowStart := now.Truncate(time.Minute)

	w, ok := t.counters[channelID]
	if !ok || w.windowStart != windowStart {
		w = &rateLimitWindow{windowStart: windowStart}
		t.counters[channelID] = w
	}

	return w
}

// IncrementRequest increments the request count for a channel.
func (t *ChannelRequestTracker) IncrementRequest(channelID int) {
	t.mu.Lock()
	defer t.mu.Unlock()

	w := t.getOrResetWindow(channelID)
	w.requests++
}

// AddTokens adds token count for a channel. Updates BOTH the per-minute window
// (used for TPM enforcement) and the per-UTC-day window (used for DailyTokenLimit
// enforcement). Negative or zero increments are ignored.
func (t *ChannelRequestTracker) AddTokens(channelID int, tokens int64) {
	if tokens <= 0 {
		return
	}

	day := currentUTCDay()
	t.mu.Lock()
	defer t.mu.Unlock()

	w := t.getOrResetWindow(channelID)
	w.tokens += tokens

	dw, ok := t.dailyTokens[channelID]
	if !ok || dw.day != day {
		dw = &dailyTokenWindow{day: day}
		t.dailyTokens[channelID] = dw
	}
	dw.tokens += tokens
}

// GetRequestCount returns the current request count for a channel in the current window.
func (t *ChannelRequestTracker) GetRequestCount(channelID int) int64 {
	t.mu.RLock()
	defer t.mu.RUnlock()

	w, ok := t.counters[channelID]
	if !ok {
		return 0
	}

	windowStart := time.Now().Truncate(time.Minute)
	if w.windowStart != windowStart {
		return 0
	}

	return w.requests
}

// GetTokenCount returns the current token count for a channel in the current window.
func (t *ChannelRequestTracker) GetTokenCount(channelID int) int64 {
	t.mu.RLock()
	defer t.mu.RUnlock()

	w, ok := t.counters[channelID]
	if !ok {
		return 0
	}

	windowStart := time.Now().Truncate(time.Minute)
	if w.windowStart != windowStart {
		return 0
	}

	return w.tokens
}

// SetCooldown sets a cooldown period for a channel until the specified time.
// It only extends the cooldown; a shorter value will not overwrite an existing longer one.
func (t *ChannelRequestTracker) SetCooldown(channelID int, until time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if existing, ok := t.cooldowns[channelID]; ok && existing.After(until) {
		return
	}

	t.cooldowns[channelID] = until
}

// IsCoolingDown checks if a channel is currently in a cooldown period.
// It also performs lazy cleanup by removing expired cooldown entries.
func (t *ChannelRequestTracker) IsCoolingDown(channelID int) bool {
	_, ok := t.GetCooldownUntil(channelID)
	return ok
}

// GetCooldownUntil returns the cooldown expiration time for a channel.
// Returns false if the channel is not in cooldown or the cooldown has expired.
func (t *ChannelRequestTracker) GetCooldownUntil(channelID int) (time.Time, bool) {
	t.mu.RLock()
	until, ok := t.cooldowns[channelID]
	t.mu.RUnlock()

	if !ok {
		return time.Time{}, false
	}

	// Check if cooldown has expired
	now := time.Now()
	if now.After(until) {
		t.clearExpiredCooldown(channelID, until, now)
		return time.Time{}, false
	}

	return until, true
}

// clearExpiredCooldown removes an expired cooldown entry only if it still matches
// the value observed by the caller, preventing races with newer SetCooldown writes.
func (t *ChannelRequestTracker) clearExpiredCooldown(channelID int, observedUntil, now time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()

	currentUntil, ok := t.cooldowns[channelID]
	if !ok {
		return
	}

	if currentUntil.Equal(observedUntil) && now.After(currentUntil) {
		delete(t.cooldowns, channelID)
	}
}
