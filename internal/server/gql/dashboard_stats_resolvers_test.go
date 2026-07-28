package gql

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/looplj/axonhub/internal/authz"
	"github.com/looplj/axonhub/internal/ent"
	"github.com/looplj/axonhub/internal/ent/enttest"
	"github.com/looplj/axonhub/internal/ent/request"
	"github.com/looplj/axonhub/internal/objects"
	"github.com/looplj/axonhub/internal/pkg/xcache"
	"github.com/looplj/axonhub/internal/pkg/xtime"
	"github.com/looplj/axonhub/internal/server/biz"
)

func setupTestStatsResolver(t *testing.T) (*queryResolver, context.Context, *ent.Client) {
	t.Helper()

	client := enttest.NewEntClient(t, "sqlite3", "file:ent?mode=memory&_fk=1")
	systemService := &biz.SystemService{
		Cache: xcache.NewFromConfig[ent.System](xcache.Config{Mode: xcache.ModeMemory}),
	}

	ctx := context.Background()
	ctx = ent.NewContext(ctx, client)
	ctx = authz.WithTestBypass(ctx)

	// The dashboard aggregations are cached process-wide, so they must not leak
	// between tests that each seed their own client.
	resetDashboardCaches()
	t.Cleanup(resetDashboardCaches)

	resolver := &queryResolver{&Resolver{client: client, systemService: systemService}}

	return resolver, ctx, client
}

type statsSeedRow struct {
	at         time.Time
	prompt     int64
	completion int64
	cached     int64
}

func seedUsageLogs(t *testing.T, ctx context.Context, client *ent.Client, rows []statsSeedRow) {
	t.Helper()

	projectEntity, err := client.Project.Create().
		SetName("Stats Project").
		SetDescription("test project").
		Save(ctx)
	require.NoError(t, err)

	req, err := client.Request.Create().
		SetProjectID(projectEntity.ID).
		SetModelID("gpt-test").
		SetFormat("openai/chat_completions").
		SetSource(request.SourceAPI).
		SetStatus(request.StatusCompleted).
		SetStream(false).
		SetRequestBody(objects.JSONRawMessage(`{}`)).
		Save(ctx)
	require.NoError(t, err)

	for _, row := range rows {
		_, err := client.UsageLog.Create().
			SetRequestID(req.ID).
			SetProjectID(projectEntity.ID).
			SetModelID("gpt-test").
			SetPromptTokens(row.prompt).
			SetCompletionTokens(row.completion).
			SetTotalTokens(row.prompt + row.completion).
			SetPromptCachedTokens(row.cached).
			SetCreatedAt(row.at).
			Save(ctx)
		require.NoError(t, err)
	}
}

// statsSeedRows returns rows placed relative to the current calendar period
// boundaries so the tests are independent of the date they run on.
func statsSeedRows(period xtime.CalendarPeriods) []statsSeedRow {
	return []statsSeedRow{
		{at: period.Today.Start.Add(time.Hour), prompt: 10, completion: 20, cached: 5},
		{at: period.ThisWeek.Start.Add(time.Hour), prompt: 100, completion: 200, cached: 50},
		{at: period.LastWeek.Start.Add(time.Hour), prompt: 1000, completion: 2000, cached: 500},
		{at: period.ThisMonth.Start.Add(time.Hour), prompt: 3, completion: 7, cached: 1},
		{at: period.LastWeek.Start.Add(-24 * time.Hour), prompt: 9, completion: 9, cached: 9},
	}
}

func TestQueryResolver_RequestStats_SingleScanMatchesPeriodSemantics(t *testing.T) {
	resolver, ctx, client := setupTestStatsResolver(t)
	defer client.Close()

	period := xtime.GetCalendarPeriods(time.UTC)
	rows := statsSeedRows(period)
	seedUsageLogs(t, ctx, client, rows)

	var wantToday, wantThisWeek, wantLastWeek, wantThisMonth int
	for _, row := range rows {
		if !row.at.Before(period.Today.Start) {
			wantToday++
		}
		if !row.at.Before(period.ThisWeek.Start) {
			wantThisWeek++
		}
		if !row.at.Before(period.LastWeek.Start) && row.at.Before(period.LastWeek.End) {
			wantLastWeek++
		}
		if !row.at.Before(period.ThisMonth.Start) {
			wantThisMonth++
		}
	}

	stats, err := resolver.RequestStats(ctx)
	require.NoError(t, err)
	require.Equal(t, wantToday, stats.RequestsToday)
	require.Equal(t, wantThisWeek, stats.RequestsThisWeek)
	require.Equal(t, wantLastWeek, stats.RequestsLastWeek)
	require.Equal(t, wantThisMonth, stats.RequestsThisMonth)
}

func TestQueryResolver_TokenStats_SingleScanMatchesPeriodSemantics(t *testing.T) {
	resolver, ctx, client := setupTestStatsResolver(t)
	defer client.Close()

	InvalidateAllTimeTokenStatsCache()
	t.Cleanup(InvalidateAllTimeTokenStatsCache)

	period := xtime.GetCalendarPeriods(time.UTC)
	rows := statsSeedRows(period)
	seedUsageLogs(t, ctx, client, rows)

	sumIf := func(match func(statsSeedRow) bool) (input, output, cached int) {
		for _, row := range rows {
			if match(row) {
				input += int(row.prompt)
				output += int(row.completion)
				cached += int(row.cached)
			}
		}
		return input, output, cached
	}

	wantInToday, wantOutToday, wantCachedToday := sumIf(func(r statsSeedRow) bool {
		return !r.at.Before(period.Today.Start)
	})
	wantInWeek, wantOutWeek, wantCachedWeek := sumIf(func(r statsSeedRow) bool {
		return !r.at.Before(period.ThisWeek.Start)
	})
	wantInMonth, wantOutMonth, wantCachedMonth := sumIf(func(r statsSeedRow) bool {
		return !r.at.Before(period.ThisMonth.Start)
	})
	wantInAll, wantOutAll, wantCachedAll := sumIf(func(statsSeedRow) bool { return true })

	stats, err := resolver.TokenStats(ctx)
	require.NoError(t, err)

	require.Equal(t, wantInToday, stats.TotalInputTokensToday)
	require.Equal(t, wantOutToday, stats.TotalOutputTokensToday)
	require.Equal(t, wantCachedToday, stats.TotalCachedTokensToday)

	require.Equal(t, wantInWeek, stats.TotalInputTokensThisWeek)
	require.Equal(t, wantOutWeek, stats.TotalOutputTokensThisWeek)
	require.Equal(t, wantCachedWeek, stats.TotalCachedTokensThisWeek)

	require.Equal(t, wantInMonth, stats.TotalInputTokensThisMonth)
	require.Equal(t, wantOutMonth, stats.TotalOutputTokensThisMonth)
	require.Equal(t, wantCachedMonth, stats.TotalCachedTokensThisMonth)

	require.Equal(t, wantInAll, stats.TotalInputTokensAllTime)
	require.Equal(t, wantOutAll, stats.TotalOutputTokensAllTime)
	require.Equal(t, wantCachedAll, stats.TotalCachedTokensAllTime)
}

func TestQueryResolver_DashboardOverview_UsesCachedStatusCounts(t *testing.T) {
	resolver, ctx, client := setupTestStatsResolver(t)
	defer client.Close()

	InvalidateRequestStatusCountsCache()
	t.Cleanup(InvalidateRequestStatusCountsCache)

	projectEntity, err := client.Project.Create().
		SetName("Overview Project").
		SetDescription("test project").
		Save(ctx)
	require.NoError(t, err)

	createRequest := func(status request.Status) {
		t.Helper()

		_, err := client.Request.Create().
			SetProjectID(projectEntity.ID).
			SetModelID("gpt-test").
			SetFormat("openai/chat_completions").
			SetSource(request.SourceAPI).
			SetStatus(status).
			SetStream(false).
			SetRequestBody(objects.JSONRawMessage(`{}`)).
			Save(ctx)
		require.NoError(t, err)
	}

	createRequest(request.StatusCompleted)
	createRequest(request.StatusCompleted)
	createRequest(request.StatusFailed)

	overview, err := resolver.DashboardOverview(ctx)
	require.NoError(t, err)
	require.Equal(t, 3, overview.TotalRequests)
	require.Equal(t, 1, overview.FailedRequests)

	// Counts are served from the SWR cache within the soft TTL, so a new
	// request must not be reflected immediately.
	createRequest(request.StatusCompleted)

	overview, err = resolver.DashboardOverview(ctx)
	require.NoError(t, err)
	require.Equal(t, 3, overview.TotalRequests)

	// After invalidation the fresh counts are computed synchronously.
	InvalidateRequestStatusCountsCache()

	overview, err = resolver.DashboardOverview(ctx)
	require.NoError(t, err)
	require.Equal(t, 4, overview.TotalRequests)
	require.Equal(t, 1, overview.FailedRequests)
}
