package gql

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/sql"
	"github.com/samber/lo"
	"golang.org/x/sync/singleflight"

	"github.com/looplj/axonhub/internal/authz"
	"github.com/looplj/axonhub/internal/ent"
	"github.com/looplj/axonhub/internal/ent/channelprobe"
	"github.com/looplj/axonhub/internal/ent/request"
	"github.com/looplj/axonhub/internal/ent/usagelog"
	"github.com/looplj/axonhub/internal/log"
	"github.com/looplj/axonhub/internal/pkg/xtime"
	"github.com/looplj/axonhub/internal/scopes"
	"github.com/looplj/axonhub/internal/server/gql/qb"
)

var (
	allTimeCache        *TokenStats
	allTimeCacheTime    time.Time
	allTimeCacheMu      sync.RWMutex
	softTTL             = 1 * time.Hour
	hardTTL             = 24 * time.Hour
	allTimeRefreshGroup singleflight.Group

	requestCountsCache     *requestStatusCounts
	requestCountsCacheTime time.Time
	requestCountsCacheMu   sync.RWMutex
	requestCountsSoftTTL   = 5 * time.Minute
)

// requestStatusCounts holds all-time request counts grouped by status.
type requestStatusCounts struct {
	Total  int
	Failed int
}

// cacheResult holds the result of a cache refresh operation.
type cacheResult struct {
	stats *TokenStats
	time  time.Time
}

// SetTokenStatsCacheTTL sets the cache TTL values for all-time token stats.
func SetTokenStatsCacheTTL(soft, hard time.Duration) {
	allTimeCacheMu.Lock()
	defer allTimeCacheMu.Unlock()
	softTTL = soft
	hardTTL = hard
}

// InvalidateAllTimeTokenStatsCache clears the all-time token stats cache.
func InvalidateAllTimeTokenStatsCache() {
	allTimeCacheMu.Lock()
	allTimeCache = nil
	allTimeCacheTime = time.Time{}
	allTimeCacheMu.Unlock()
}

// InvalidateRequestStatusCountsCache clears the all-time request status counts cache.
func InvalidateRequestStatusCountsCache() {
	requestCountsCacheMu.Lock()
	requestCountsCache = nil
	requestCountsCacheTime = time.Time{}
	requestCountsCacheMu.Unlock()
}

// localDateExpr returns a dialect-specific SQL expression that converts the
// given UTC timestamp column to a local date string 'YYYY-MM-DD'.
func localDateExpr(s *sql.Selector, column string, offsetSeconds int, loc *time.Location) string {
	col := s.C(column)

	switch s.Dialect() {
	case dialect.SQLite:
		return fmt.Sprintf("strftime('%%Y-%%m-%%d', datetime(substr(%s, 1, 19), '%+d seconds'))", col, offsetSeconds)
	case dialect.MySQL:
		return fmt.Sprintf("DATE_FORMAT(CONVERT_TZ(%s, '+00:00', '%s'), '%%Y-%%m-%%d')", col, xtime.FormatUTCOffset(offsetSeconds))
	case dialect.Postgres:
		return fmt.Sprintf("to_char(%s AT TIME ZONE '%s', 'YYYY-MM-DD')", col, loc.String())
	default:
		// Fallback to ANSI-ish cast; many DBs accept this, but not guaranteed
		return fmt.Sprintf("DATE(%s)", col)
	}
}

// requestStatusCounts returns all-time total/failed request counts with
// stale-while-revalidate caching. The full-table GROUP BY over requests is
// expensive, so it runs at most once per soft TTL and stale values are served
// while a background refresh runs.
func (r *queryResolver) requestStatusCounts(ctx context.Context) (*requestStatusCounts, error) {
	refresh := func(ctx context.Context) (*requestStatusCounts, error) {
		// Use singleflight to prevent concurrent refresh attempts
		result, err, _ := allTimeRefreshGroup.Do("requestStatusCounts", func() (interface{}, error) {
			var statusCounts []struct {
				Status request.Status `json:"status"`
				Count  int            `json:"count"`
			}
			if err := r.client.Request.Query().
				GroupBy(request.FieldStatus).
				Aggregate(ent.Count()).
				Scan(ctx, &statusCounts); err != nil {
				return nil, err
			}

			counts := &requestStatusCounts{}
			for _, sc := range statusCounts {
				counts.Total += sc.Count
				if sc.Status == request.StatusFailed {
					counts.Failed = sc.Count
				}
			}

			requestCountsCacheMu.Lock()
			requestCountsCache = counts
			requestCountsCacheTime = time.Now().UTC()
			requestCountsCacheMu.Unlock()

			return counts, nil
		})
		if err != nil {
			return nil, err
		}
		counts, ok := result.(*requestStatusCounts)
		if !ok {
			return nil, fmt.Errorf("unexpected type from singleflight: %T", result)
		}

		return counts, nil
	}

	requestCountsCacheMu.RLock()
	cached := requestCountsCache
	cacheAge := time.Since(requestCountsCacheTime)
	requestCountsCacheMu.RUnlock()

	if cached != nil && cacheAge < hardTTL {
		// If cache is stale (exceeded soft TTL), trigger async refresh
		if cacheAge >= requestCountsSoftTTL {
			go func() {
				bgCtx := authz.WithScopeDecision(context.Background(), scopes.ScopeReadDashboard)
				defer func() {
					if rec := recover(); rec != nil {
						log.Error(bgCtx, "panic in request status counts cache refresh", log.Any("panic", rec))
					}
				}()
				if _, err := refresh(bgCtx); err != nil {
					log.Error(bgCtx, "async request status counts cache refresh failed",
						log.Cause(err),
						log.Duration("cache_age", cacheAge),
					)
				}
			}()
		}

		return cached, nil
	}

	// Cache is missing or expired (exceeded hard TTL) - compute synchronously
	return refresh(ctx)
}

type scoredItem[T any] struct {
	stats      T
	confidence string
	score      int
}

func safeIntFromInt64(v int64) int {
	const (
		maxInt = int(^uint(0) >> 1)
		minInt = -maxInt - 1
	)

	if v > int64(maxInt) {
		return maxInt
	}

	if v < int64(minInt) {
		return minInt
	}

	return int(v)
}

func buildDateExpression(dialectName string, timestampCol string, offsetSeconds int, locName string) string {
	switch dialectName {
	case dialect.SQLite:
		return fmt.Sprintf("strftime('%%Y-%%m-%%d', datetime(%s, 'unixepoch', '%+d seconds'))", timestampCol, offsetSeconds)
	case dialect.MySQL:
		offsetStr := xtime.FormatUTCOffset(offsetSeconds)
		return fmt.Sprintf("DATE_FORMAT(CONVERT_TZ(FROM_UNIXTIME(%s), '+00:00', '%s'), '%%Y-%%m-%%d')", timestampCol, offsetStr)
	case dialect.Postgres:
		return fmt.Sprintf("to_char(to_timestamp(%s) AT TIME ZONE '%s', 'YYYY-MM-DD')", timestampCol, locName)
	default:
		return fmt.Sprintf("DATE(%s)", timestampCol)
	}
}

func buildProbeQuerySelects(s *sql.Selector, dateExpr string) []string {
	avgTokensCol := s.C(channelprobe.FieldAvgTokensPerSecond)
	totalRequestsCol := s.C(channelprobe.FieldTotalRequestCount)
	avgTTFTCol := s.C(channelprobe.FieldAvgTimeToFirstTokenMs)
	channelIDCol := s.C(channelprobe.FieldChannelID)

	throughputExpr := fmt.Sprintf(
		"SUM(CASE WHEN %s IS NOT NULL THEN %s * %s ELSE 0 END) / NULLIF(SUM(CASE WHEN %s IS NOT NULL THEN %s ELSE 0 END), 0)",
		avgTokensCol, avgTokensCol, totalRequestsCol, avgTokensCol, totalRequestsCol,
	)
	ttftExpr := fmt.Sprintf(
		"SUM(CASE WHEN %s IS NOT NULL THEN %s * %s ELSE 0 END) / NULLIF(SUM(CASE WHEN %s IS NOT NULL THEN %s ELSE 0 END), 0)",
		avgTTFTCol, avgTTFTCol, totalRequestsCol, avgTTFTCol, totalRequestsCol,
	)

	return []string{
		sql.As(dateExpr, "date"),
		sql.As(channelIDCol, "channel_id"),
		sql.As(sql.Sum(totalRequestsCol), "request_count"),
		sql.As(throughputExpr, "throughput"),
		sql.As(ttftExpr, "ttft_ms"),
	}
}

func calculateConfidenceAndSort[T any](
	results []T,
	getRequestCount func(T) int64,
	getThroughput func(T) float64,
	limit int,
) []scoredItem[T] {
	if len(results) == 0 {
		return nil
	}

	requestCounts := lo.Map(results, func(item T, _ int) int {
		return int(getRequestCount(item))
	})
	sort.Ints(requestCounts)

	var median float64

	mid := len(requestCounts) / 2
	if len(requestCounts)%2 == 0 {
		median = float64(requestCounts[mid-1]+requestCounts[mid]) / 2
	} else {
		median = float64(requestCounts[mid])
	}

	scoredResults := lo.Map(results, func(item T, _ int) scoredItem[T] {
		conf := qb.CalculateConfidenceLevel(int(getRequestCount(item)), median)
		score := 0

		switch conf {
		case "high":
			score = 3
		case "medium":
			score = 2
		case "low":
			score = 1
		}

		return scoredItem[T]{
			stats:      item,
			confidence: conf,
			score:      score,
		}
	})

	filtered := lo.Filter(scoredResults, func(item scoredItem[T], _ int) bool {
		return item.confidence == "high" || item.confidence == "medium"
	})

	resultsToShow := scoredResults
	if len(filtered) >= limit {
		resultsToShow = filtered
	}

	sort.Slice(resultsToShow, func(i, j int) bool {
		if resultsToShow[i].score != resultsToShow[j].score {
			return resultsToShow[i].score > resultsToShow[j].score
		}

		return getThroughput(resultsToShow[i].stats) > getThroughput(resultsToShow[j].stats)
	})

	if len(resultsToShow) > limit {
		resultsToShow = resultsToShow[:limit]
	}

	return resultsToShow
}

// getTopModelsForAPIKeys returns top 3 models by total tokens for multiple API keys in a single query
func (r *queryResolver) getTopModelsForAPIKeys(ctx context.Context, apiKeyIDs []int, input *APIKeyTokenUsageStatsInput) map[int][]*ModelTokenUsageStats {
	if len(apiKeyIDs) == 0 {
		return make(map[int][]*ModelTokenUsageStats)
	}

	query := r.client.UsageLog.Query().
		Where(usagelog.APIKeyIDIn(apiKeyIDs...))

	if input != nil {
		if input.CreatedAtGTE != nil {
			query = query.Where(usagelog.CreatedAtGTE(*input.CreatedAtGTE))
		}
		if input.CreatedAtLTE != nil {
			query = query.Where(usagelog.CreatedAtLTE(*input.CreatedAtLTE))
		}
	}

	type modelStats struct {
		APIKeyID        int    `json:"api_key_id"`
		ModelID         string `json:"model_id"`
		InputTokens     int64  `json:"input_tokens"`
		OutputTokens    int64  `json:"output_tokens"`
		CachedTokens    int64  `json:"cached_tokens"`
		ReasoningTokens int64  `json:"reasoning_tokens"`
		TotalTokens     int64  `json:"total_tokens"`
	}

	var allResults []modelStats

	err := query.Modify(func(s *sql.Selector) {
		s.Select(
			s.C(usagelog.FieldAPIKeyID),
			s.C(usagelog.FieldModelID),
			sql.As(fmt.Sprintf("COALESCE(SUM(%s), 0)", s.C(usagelog.FieldPromptTokens)), "input_tokens"),
			sql.As(fmt.Sprintf("COALESCE(SUM(%s), 0)", s.C(usagelog.FieldCompletionTokens)), "output_tokens"),
			sql.As(fmt.Sprintf("COALESCE(SUM(%s), 0)", s.C(usagelog.FieldPromptCachedTokens)), "cached_tokens"),
			sql.As(fmt.Sprintf("COALESCE(SUM(%s), 0)", s.C(usagelog.FieldCompletionReasoningTokens)), "reasoning_tokens"),
			// Order by billable total (input + output). Reasoning is already inside
			// completion_tokens, so adding it would double-count and bias the
			// per-API-key top-N model ranking toward reasoning-heavy models.
			sql.As(fmt.Sprintf("COALESCE(SUM(%s), 0) + COALESCE(SUM(%s), 0)",
				s.C(usagelog.FieldPromptTokens),
				s.C(usagelog.FieldCompletionTokens)), "total_tokens"),
		).GroupBy(s.C(usagelog.FieldAPIKeyID), s.C(usagelog.FieldModelID)).
			OrderBy(sql.Desc("total_tokens"))
	}).Scan(ctx, &allResults)
	if err != nil {
		log.Warn(ctx, "failed to get top models for API keys", log.Cause(err))
		return make(map[int][]*ModelTokenUsageStats)
	}

	// Group by API key and take top 3 per key
	resultMap := make(map[int][]*ModelTokenUsageStats)
	for _, result := range allResults {
		if len(resultMap[result.APIKeyID]) < 3 {
			resultMap[result.APIKeyID] = append(resultMap[result.APIKeyID], &ModelTokenUsageStats{
				ModelID:         result.ModelID,
				InputTokens:     safeIntFromInt64(result.InputTokens),
				OutputTokens:    safeIntFromInt64(result.OutputTokens),
				CachedTokens:    safeIntFromInt64(result.CachedTokens),
				ReasoningTokens: safeIntFromInt64(result.ReasoningTokens),
			})
		}
	}

	return resultMap
}

type timeWindowFilter struct {
	since time.Time
	until time.Time
	apply bool
}

func (f timeWindowFilter) hasStart() bool {
	return f.apply && !f.since.IsZero()
}

func (f timeWindowFilter) hasEnd() bool {
	return f.apply && !f.until.IsZero()
}

func (f timeWindowFilter) applySelector(s *sql.Selector, column string) {
	if f.hasStart() {
		s.Where(sql.GTE(column, f.since))
	}
	if f.hasEnd() {
		s.Where(sql.LTE(column, f.until))
	}
}

// parseTimeWindow parses a time window string and returns the matching filter.
// Supported values: "day", "week", "month", "allTime", empty string, or
// "custom|<start RFC3339>|<end RFC3339>". Custom bounds may omit one side.
// Unknown values default to allTime behavior (no filtering).
func (r *queryResolver) parseTimeWindow(ctx context.Context, timeWindow *string) timeWindowFilter {
	loc := r.systemService.TimeLocation(ctx)
	period := xtime.GetCalendarPeriods(loc)

	if timeWindow == nil {
		return timeWindowFilter{}
	}

	raw := strings.TrimSpace(*timeWindow)
	if raw == "" || raw == "allTime" {
		return timeWindowFilter{}
	}

	if strings.HasPrefix(raw, "custom|") {
		parts := strings.SplitN(raw, "|", 3)
		if len(parts) != 3 {
			return timeWindowFilter{}
		}

		var filter timeWindowFilter
		if parts[1] != "" {
			since, err := time.Parse(time.RFC3339Nano, parts[1])
			if err != nil {
				return timeWindowFilter{}
			}
			filter.since = since
		}
		if parts[2] != "" {
			until, err := time.Parse(time.RFC3339Nano, parts[2])
			if err != nil {
				return timeWindowFilter{}
			}
			filter.until = until
		}
		if !filter.since.IsZero() && !filter.until.IsZero() && filter.until.Before(filter.since) {
			return timeWindowFilter{}
		}
		filter.apply = !filter.since.IsZero() || !filter.until.IsZero()

		return filter
	}

	switch raw {
	case "day":
		return timeWindowFilter{since: period.Today.Start, apply: true}
	case "week":
		return timeWindowFilter{since: period.ThisWeek.Start, apply: true}
	case "month":
		return timeWindowFilter{since: period.ThisMonth.Start, apply: true}
	default:
		return timeWindowFilter{}
	}
}
