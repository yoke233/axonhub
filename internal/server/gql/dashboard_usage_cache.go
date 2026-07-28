package gql

import (
	"context"
	"fmt"
	"sync"
	"time"

	"entgo.io/ent/dialect/sql"
	"golang.org/x/sync/singleflight"

	"github.com/looplj/axonhub/internal/ent/usagelog"
	"github.com/looplj/axonhub/internal/pkg/xtime"
)

// dailyUsageWindowDays is the widest daily window any dashboard resolver needs.
// The 30-day trend chart is the widest; month-to-date and last week always fall
// inside it because a month never starts more than 31 days ago and last week
// never starts more than 14 days ago.
const dailyUsageWindowDays = 31

// dailyUsageBucket is a single local-date bucket of aggregated usage_logs
// metrics. One scan produces every metric the daily dashboard resolvers need.
type dailyUsageBucket struct {
	Date         string  `json:"date"`
	Count        int     `json:"total_count"`
	InputTokens  int     `json:"input_tokens"`
	OutputTokens int     `json:"output_tokens"`
	CachedTokens int     `json:"cached_tokens"`
	TotalTokens  int     `json:"total_tokens"`
	Cost         float64 `json:"total_cost"`
}

// dailyUsage is the cached result of one aggregation scan, including the local
// window start it was computed for so resolvers can slice it safely.
type dailyUsage struct {
	// Start is the local midnight the scan began at.
	Start time.Time
	// Buckets are ordered by date ascending.
	Buckets []dailyUsageBucket
}

// dailyUsageTTL bounds how stale the shared aggregation may get. It matches the
// dashboard's own refresh interval, so a viewer never waits on a scan that a
// concurrent viewer just ran.
const dailyUsageTTL = 60 * time.Second

var (
	dailyUsageCache     *dailyUsage
	dailyUsageCacheKey  string
	dailyUsageCacheTime time.Time
	dailyUsageCacheMu   sync.RWMutex
	dailyUsageGroup     singleflight.Group
)

// InvalidateDashboardDailyUsageCache clears the shared daily usage cache.
func InvalidateDashboardDailyUsageCache() {
	dailyUsageCacheMu.Lock()
	dailyUsageCache = nil
	dailyUsageCacheKey = ""
	dailyUsageCacheTime = time.Time{}
	dailyUsageCacheMu.Unlock()
}

// dailyUsageWindowStart returns the local midnight that the shared scan starts
// at: the earliest instant any daily dashboard aggregation needs.
func dailyUsageWindowStart(loc *time.Location) time.Time {
	nowLocal := xtime.UTCNow().In(loc)
	todayStart := time.Date(nowLocal.Year(), nowLocal.Month(), nowLocal.Day(), 0, 0, 0, 0, loc)

	return todayStart.AddDate(0, 0, -dailyUsageWindowDays+1)
}

// dashboardDailyUsage returns usage_logs aggregated per local date over the
// shared dashboard window.
//
// RequestStats, TokenStats and DailyRequestStats all need per-day counts and
// sums over the same rows, so they share a single scan behind a short TTL
// cache. Without it every dashboard load scans the same slice of usage_logs
// three times, once per resolver, and again for every concurrent viewer.
func (r *queryResolver) dashboardDailyUsage(ctx context.Context) (*dailyUsage, error) {
	// A cache hit skips the ent query, and with it the privacy decision that
	// would otherwise reject callers without dashboard access, so the scope has
	// to be checked explicitly here.
	if err := requireDashboardScope(ctx); err != nil {
		return nil, err
	}

	loc := r.systemService.TimeLocation(ctx)
	startLocal := dailyUsageWindowStart(loc)
	_, offsetSeconds := xtime.UTCNow().In(loc).Zone()

	// The key covers everything that changes the shape of the scan: the window
	// start rolls over at local midnight and the offset changes on DST shifts
	// or a reconfigured system timezone.
	key := fmt.Sprintf("%s|%d", startLocal.Format(time.RFC3339), offsetSeconds)

	dailyUsageCacheMu.RLock()
	cached := dailyUsageCache
	cachedKey := dailyUsageCacheKey
	cacheAge := time.Since(dailyUsageCacheTime)
	dailyUsageCacheMu.RUnlock()

	if cached != nil && cachedKey == key && cacheAge < dailyUsageTTL {
		return cached, nil
	}

	result, err, _ := dailyUsageGroup.Do(key, func() (interface{}, error) {
		var rows []dailyUsageBucket

		err := r.client.UsageLog.Query().
			Where(usagelog.CreatedAtGTE(startLocal.UTC())).
			Modify(func(s *sql.Selector) {
				dateExpr := localDateExpr(s, usagelog.FieldCreatedAt, offsetSeconds, loc)
				s.Select(
					sql.As(dateExpr, "date"),
					sql.As(sql.Count(s.C(usagelog.FieldID)), "total_count"),
					sql.As(fmt.Sprintf("COALESCE(SUM(%s), 0)", s.C(usagelog.FieldPromptTokens)), "input_tokens"),
					sql.As(fmt.Sprintf("COALESCE(SUM(%s), 0)", s.C(usagelog.FieldCompletionTokens)), "output_tokens"),
					sql.As(fmt.Sprintf("COALESCE(SUM(%s), 0)", s.C(usagelog.FieldPromptCachedTokens)), "cached_tokens"),
					sql.As(fmt.Sprintf("COALESCE(SUM(%s), 0)", s.C(usagelog.FieldTotalTokens)), "total_tokens"),
					sql.As(fmt.Sprintf("COALESCE(SUM(%s), 0)", s.C(usagelog.FieldTotalCost)), "total_cost"),
				).
					GroupBy(dateExpr).
					OrderBy("date")
			}).
			Scan(ctx, &rows)
		if err != nil {
			return nil, err
		}

		usage := &dailyUsage{Start: startLocal, Buckets: rows}

		dailyUsageCacheMu.Lock()
		dailyUsageCache = usage
		dailyUsageCacheKey = key
		dailyUsageCacheTime = time.Now().UTC()
		dailyUsageCacheMu.Unlock()

		return usage, nil
	})
	if err != nil {
		return nil, err
	}

	usage, ok := result.(*dailyUsage)
	if !ok {
		return nil, fmt.Errorf("unexpected type from singleflight: %T", result)
	}

	return usage, nil
}
