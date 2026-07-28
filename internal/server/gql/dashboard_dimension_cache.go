package gql

import (
	"context"
	"fmt"
	"sort"
	"time"

	"entgo.io/ent/dialect/sql"
	"entgo.io/ent/privacy"
	lru "github.com/hashicorp/golang-lru/v2"
	"golang.org/x/sync/singleflight"

	"github.com/looplj/axonhub/internal/authz"
	"github.com/looplj/axonhub/internal/ent/channel"
	"github.com/looplj/axonhub/internal/ent/usagelog"
	"github.com/looplj/axonhub/internal/scopes"
)

// The dashboard renders requests, cost and tokens per channel/model/API key as
// three separate charts, and each used to run its own GROUP BY over the same
// usage_logs rows. One aggregation per dimension now feeds all three, cached
// briefly so concurrent viewers and the 60s refresh cycle share a single scan.
const (
	dimensionUsageTTL = 60 * time.Second
	// dimensionCacheSize bounds the cache: custom time ranges are unbounded in
	// principle, so entries must be evicted rather than accumulated.
	dimensionCacheSize = 32
)

// The aggregate measures are repeated per dimension rather than embedded:
// ent's struct scanner skips unexported fields before it considers embedding,
// so an embedded unexported type would silently never be populated.

type channelUsageRow struct {
	ChannelID       int     `json:"channel_id"`
	ChannelName     string  `json:"channel_name"`
	Count           int     `json:"request_count"`
	InputTokens     int64   `json:"input_tokens"`
	OutputTokens    int64   `json:"output_tokens"`
	CachedTokens    int64   `json:"cached_tokens"`
	ReasoningTokens int64   `json:"reasoning_tokens"`
	Cost            float64 `json:"total_cost"`
}

type modelUsageRow struct {
	ModelID         string  `json:"model_id"`
	Count           int     `json:"request_count"`
	InputTokens     int64   `json:"input_tokens"`
	OutputTokens    int64   `json:"output_tokens"`
	CachedTokens    int64   `json:"cached_tokens"`
	ReasoningTokens int64   `json:"reasoning_tokens"`
	Cost            float64 `json:"total_cost"`
}

type apiKeyUsageRow struct {
	APIKeyID        int     `json:"api_key_id"`
	Count           int     `json:"request_count"`
	InputTokens     int64   `json:"input_tokens"`
	OutputTokens    int64   `json:"output_tokens"`
	CachedTokens    int64   `json:"cached_tokens"`
	ReasoningTokens int64   `json:"reasoning_tokens"`
	Cost            float64 `json:"total_cost"`
}

// billableTokens is the ordering key shared by the token charts. Reasoning
// tokens are already counted inside completion tokens, so adding them would
// double-count and bias the ranking toward reasoning-heavy entries.
func (r channelUsageRow) billableTokens() int64 { return r.InputTokens + r.OutputTokens }
func (r modelUsageRow) billableTokens() int64   { return r.InputTokens + r.OutputTokens }
func (r apiKeyUsageRow) billableTokens() int64  { return r.InputTokens + r.OutputTokens }

type dimensionCacheEntry[T any] struct {
	rows []T
	at   time.Time
}

// dimensionCache is a small TTL cache in front of one dimension's aggregation.
type dimensionCache[T any] struct {
	entries *lru.Cache[string, dimensionCacheEntry[T]]
	group   singleflight.Group
}

func newDimensionCache[T any]() *dimensionCache[T] {
	// lru.New only fails on a non-positive size, which is a constant here.
	entries, err := lru.New[string, dimensionCacheEntry[T]](dimensionCacheSize)
	if err != nil {
		panic(fmt.Sprintf("gql: invalid dimension cache size: %v", err))
	}

	return &dimensionCache[T]{entries: entries}
}

func (c *dimensionCache[T]) purge() {
	c.entries.Purge()
}

func (c *dimensionCache[T]) load(
	ctx context.Context,
	key string,
	compute func(context.Context) ([]T, error),
) ([]T, error) {
	if entry, ok := c.entries.Get(key); ok && time.Since(entry.at) < dimensionUsageTTL {
		return entry.rows, nil
	}

	result, err, _ := c.group.Do(key, func() (interface{}, error) {
		rows, err := compute(ctx)
		if err != nil {
			return nil, err
		}

		c.entries.Add(key, dimensionCacheEntry[T]{rows: rows, at: time.Now()})

		return rows, nil
	})
	if err != nil {
		return nil, err
	}

	rows, ok := result.([]T)
	if !ok {
		return nil, fmt.Errorf("unexpected type from singleflight: %T", result)
	}

	return rows, nil
}

var (
	channelUsageCache = newDimensionCache[channelUsageRow]()
	modelUsageCache   = newDimensionCache[modelUsageRow]()
	apiKeyUsageCache  = newDimensionCache[apiKeyUsageRow]()
)

// InvalidateDashboardDimensionCaches clears the per-dimension usage caches.
func InvalidateDashboardDimensionCaches() {
	channelUsageCache.purge()
	modelUsageCache.purge()
	apiKeyUsageCache.purge()
}

// dimensionTopN returns the top `limit` rows by the given ordering. It sorts a
// copy: the input slice is shared by every caller holding the cache entry and
// must not be reordered in place.
func dimensionTopN[T any](rows []T, limit int, better func(a, b T) bool) []T {
	sorted := make([]T, len(rows))
	copy(sorted, rows)

	sort.SliceStable(sorted, func(i, j int) bool {
		return better(sorted[i], sorted[j])
	})

	if len(sorted) > limit {
		sorted = sorted[:limit]
	}

	return sorted
}

// mergeChannelRowsByName collapses same-named channels into one row, matching
// the request/cost charts which group by channel name rather than by id.
func mergeChannelRowsByName(rows []channelUsageRow) []channelUsageRow {
	merged := make([]channelUsageRow, 0, len(rows))
	index := make(map[string]int, len(rows))

	for _, row := range rows {
		if at, ok := index[row.ChannelName]; ok {
			merged[at].Count += row.Count
			merged[at].InputTokens += row.InputTokens
			merged[at].OutputTokens += row.OutputTokens
			merged[at].CachedTokens += row.CachedTokens
			merged[at].ReasoningTokens += row.ReasoningTokens
			merged[at].Cost += row.Cost

			continue
		}

		index[row.ChannelName] = len(merged)
		merged = append(merged, row)
	}

	return merged
}

// cacheKey identifies the rows a filter selects. Relative windows ("day",
// "week") resolve to concrete local boundaries, so the key rolls over on its
// own at midnight.
func (f timeWindowFilter) cacheKey() string {
	if !f.apply {
		return "allTime"
	}

	return fmt.Sprintf("%d|%d", f.since.UnixNano(), f.until.UnixNano())
}

// selectUsageTotals adds the shared aggregate columns to a selector.
func selectUsageTotals(s *sql.Selector, extra ...string) {
	columns := append(extra,
		sql.As(sql.Count(s.C(usagelog.FieldID)), "request_count"),
		sql.As(fmt.Sprintf("COALESCE(SUM(%s), 0)", s.C(usagelog.FieldPromptTokens)), "input_tokens"),
		sql.As(fmt.Sprintf("COALESCE(SUM(%s), 0)", s.C(usagelog.FieldCompletionTokens)), "output_tokens"),
		sql.As(fmt.Sprintf("COALESCE(SUM(%s), 0)", s.C(usagelog.FieldPromptCachedTokens)), "cached_tokens"),
		sql.As(fmt.Sprintf("COALESCE(SUM(%s), 0)", s.C(usagelog.FieldCompletionReasoningTokens)), "reasoning_tokens"),
		sql.As(fmt.Sprintf("COALESCE(SUM(%s), 0)", s.C(usagelog.FieldTotalCost)), "total_cost"),
	)

	s.Select(columns...)
}

// requireDashboardScope guards the cached aggregations. A cache hit skips the
// ent query and its privacy decision, so the scope must be checked explicitly.
// It returns privacy.Deny so the rejection still surfaces as FORBIDDEN, exactly
// as it would have come back from the query itself.
func requireDashboardScope(ctx context.Context) error {
	if !authz.HasScope(ctx, scopes.ScopeReadDashboard) {
		return privacy.Deny
	}

	return nil
}

// channelUsageStats aggregates usage_logs per non-deleted channel.
func (r *queryResolver) channelUsageStats(ctx context.Context, filter timeWindowFilter) ([]channelUsageRow, error) {
	if err := requireDashboardScope(ctx); err != nil {
		return nil, err
	}

	return channelUsageCache.load(ctx, filter.cacheKey(), func(ctx context.Context) ([]channelUsageRow, error) {
		var rows []channelUsageRow

		err := r.client.UsageLog.Query().
			Modify(func(s *sql.Selector) {
				channelTable := sql.Table(channel.Table)
				s.Join(channelTable).On(
					s.C(usagelog.FieldChannelID),
					channelTable.C(channel.FieldID),
				)

				// Only non-deleted channels.
				s.Where(sql.EQ(channelTable.C(channel.FieldDeletedAt), 0))

				filter.applySelector(s, s.C(usagelog.FieldCreatedAt))

				s.GroupBy(channelTable.C(channel.FieldID), channelTable.C(channel.FieldName))

				selectUsageTotals(s,
					sql.As(channelTable.C(channel.FieldID), "channel_id"),
					sql.As(channelTable.C(channel.FieldName), "channel_name"),
				)
			}).
			Scan(ctx, &rows)
		if err != nil {
			return nil, err
		}

		return rows, nil
	})
}

// modelUsageStats aggregates usage_logs per model.
func (r *queryResolver) modelUsageStats(ctx context.Context, filter timeWindowFilter) ([]modelUsageRow, error) {
	if err := requireDashboardScope(ctx); err != nil {
		return nil, err
	}

	return modelUsageCache.load(ctx, filter.cacheKey(), func(ctx context.Context) ([]modelUsageRow, error) {
		var rows []modelUsageRow

		err := r.client.UsageLog.Query().
			Modify(func(s *sql.Selector) {
				filter.applySelector(s, s.C(usagelog.FieldCreatedAt))

				s.GroupBy(s.C(usagelog.FieldModelID))

				selectUsageTotals(s, s.C(usagelog.FieldModelID))
			}).
			Scan(ctx, &rows)
		if err != nil {
			return nil, err
		}

		return rows, nil
	})
}

// apiKeyUsageStats aggregates usage_logs per API key.
func (r *queryResolver) apiKeyUsageStats(ctx context.Context, filter timeWindowFilter) ([]apiKeyUsageRow, error) {
	if err := requireDashboardScope(ctx); err != nil {
		return nil, err
	}

	return apiKeyUsageCache.load(ctx, filter.cacheKey(), func(ctx context.Context) ([]apiKeyUsageRow, error) {
		var rows []apiKeyUsageRow

		// Aggregate directly on usage_logs.api_key_id. Joining to requests is
		// unnecessary (the column is already on usage_logs) and breaks once the
		// requests table is pruned by GC retention while usage_logs are kept.
		err := r.client.UsageLog.Query().
			Where(usagelog.APIKeyIDNotNil()).
			Modify(func(s *sql.Selector) {
				filter.applySelector(s, s.C(usagelog.FieldCreatedAt))

				s.GroupBy(s.C(usagelog.FieldAPIKeyID))

				selectUsageTotals(s, sql.As(s.C(usagelog.FieldAPIKeyID), "api_key_id"))
			}).
			Scan(ctx, &rows)
		if err != nil {
			return nil, err
		}

		return rows, nil
	})
}
