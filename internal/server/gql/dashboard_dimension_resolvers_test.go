package gql

import (
	"context"
	"fmt"
	"testing"
	"time"

	"entgo.io/ent/privacy"
	"github.com/stretchr/testify/require"

	"github.com/looplj/axonhub/internal/ent"
	"github.com/looplj/axonhub/internal/ent/apikey"
	"github.com/looplj/axonhub/internal/ent/channel"
	"github.com/looplj/axonhub/internal/ent/request"
	"github.com/looplj/axonhub/internal/objects"
)

// resetDashboardCaches clears every process-wide dashboard aggregation cache so
// tests seeding their own client never observe another test's rows.
func resetDashboardCaches() {
	InvalidateDashboardDailyUsageCache()
	InvalidateDashboardDimensionCaches()
}

// dimensionSeed describes one usage_log row for the dimension tests.
type dimensionSeed struct {
	channelName string
	modelID     string
	apiKeyName  string
	prompt      int64
	completion  int64
	cached      int64
	reasoning   int64
	cost        float64
}

type dimensionFixture struct {
	resolver *queryResolver
	ctx      context.Context
	client   *ent.Client
}

// seedDimensionUsage creates the channels, API keys and usage logs the
// per-dimension charts aggregate over.
func seedDimensionUsage(t *testing.T, seeds []dimensionSeed) *dimensionFixture {
	t.Helper()

	resolver, ctx, client := setupTestStatsResolver(t)
	t.Cleanup(func() { _ = client.Close() })

	project, err := client.Project.Create().
		SetName("Dimension Project").
		SetDescription("test project").
		Save(ctx)
	require.NoError(t, err)

	user, err := client.User.Create().
		SetEmail("dimension@example.com").
		SetPassword("password").
		SetFirstName("Dim").
		SetLastName("Tester").
		Save(ctx)
	require.NoError(t, err)

	req, err := client.Request.Create().
		SetProjectID(project.ID).
		SetModelID("seed-model").
		SetFormat("openai/chat_completions").
		SetSource(request.SourceAPI).
		SetStatus(request.StatusCompleted).
		SetStream(false).
		SetRequestBody(objects.JSONRawMessage(`{}`)).
		Save(ctx)
	require.NoError(t, err)

	channels := map[string]int{}
	apiKeys := map[string]int{}

	for i, seed := range seeds {
		if _, ok := channels[seed.channelName]; !ok {
			ch, err := client.Channel.Create().
				SetType(channel.TypeOpenai).
				SetName(seed.channelName).
				SetCredentials(objects.ChannelCredentials{APIKey: fmt.Sprintf("key-%d", i)}).
				SetSupportedModels([]string{seed.modelID}).
				SetDefaultTestModel(seed.modelID).
				SetStatus(channel.StatusEnabled).
				Save(ctx)
			require.NoError(t, err)

			channels[seed.channelName] = ch.ID
		}

		if _, ok := apiKeys[seed.apiKeyName]; !ok {
			ak, err := client.APIKey.Create().
				SetName(seed.apiKeyName).
				SetKey(fmt.Sprintf("ah-test-%d-%d", i, time.Now().UnixNano())).
				SetUserID(user.ID).
				SetProjectID(project.ID).
				SetType(apikey.TypeUser).
				Save(ctx)
			require.NoError(t, err)

			apiKeys[seed.apiKeyName] = ak.ID
		}

		_, err := client.UsageLog.Create().
			SetRequestID(req.ID).
			SetProjectID(project.ID).
			SetChannelID(channels[seed.channelName]).
			SetAPIKeyID(apiKeys[seed.apiKeyName]).
			SetModelID(seed.modelID).
			SetPromptTokens(seed.prompt).
			SetCompletionTokens(seed.completion).
			SetTotalTokens(seed.prompt + seed.completion).
			SetPromptCachedTokens(seed.cached).
			SetCompletionReasoningTokens(seed.reasoning).
			SetTotalCost(seed.cost).
			Save(ctx)
		require.NoError(t, err)
	}

	return &dimensionFixture{resolver: resolver, ctx: ctx, client: client}
}

// dimensionSeeds is shaped so that the top entry differs per measure: "beta"
// leads on requests, "alpha" on cost, and "gamma" on tokens. A shared scan that
// applied one ordering to all three charts would fail these assertions.
func dimensionSeeds() []dimensionSeed {
	return []dimensionSeed{
		{channelName: "alpha", modelID: "model-a", apiKeyName: "key-a", prompt: 10, completion: 5, cached: 2, reasoning: 1, cost: 100},
		{channelName: "beta", modelID: "model-b", apiKeyName: "key-b", prompt: 1, completion: 1, cached: 0, reasoning: 0, cost: 1},
		{channelName: "beta", modelID: "model-b", apiKeyName: "key-b", prompt: 1, completion: 1, cached: 0, reasoning: 0, cost: 1},
		{channelName: "beta", modelID: "model-b", apiKeyName: "key-b", prompt: 1, completion: 1, cached: 0, reasoning: 0, cost: 1},
		{channelName: "gamma", modelID: "model-c", apiKeyName: "key-c", prompt: 500, completion: 400, cached: 50, reasoning: 30, cost: 10},
	}
}

func TestQueryResolver_ChannelDimension_SharedScanKeepsPerChartOrdering(t *testing.T) {
	fixture := seedDimensionUsage(t, dimensionSeeds())

	requests, err := fixture.resolver.RequestStatsByChannel(fixture.ctx, nil)
	require.NoError(t, err)
	require.Len(t, requests, 3)
	require.Equal(t, "beta", requests[0].ChannelName)
	require.Equal(t, 3, requests[0].Count)

	costs, err := fixture.resolver.CostStatsByChannel(fixture.ctx, nil)
	require.NoError(t, err)
	require.Equal(t, "alpha", costs[0].ChannelName)
	require.InDelta(t, 100.0, costs[0].Cost, 0.001)

	tokens, err := fixture.resolver.TokenStatsByChannel(fixture.ctx, nil)
	require.NoError(t, err)
	require.Equal(t, "gamma", tokens[0].ChannelName)
	require.Equal(t, 500, tokens[0].InputTokens)
	require.Equal(t, 400, tokens[0].OutputTokens)
	require.Equal(t, 50, tokens[0].CachedTokens)
	require.Equal(t, 30, tokens[0].ReasoningTokens)
	// Total excludes reasoning, which is already inside completion tokens.
	require.Equal(t, 900, tokens[0].TotalTokens)
}

func TestQueryResolver_ModelDimension_SharedScanKeepsPerChartOrdering(t *testing.T) {
	fixture := seedDimensionUsage(t, dimensionSeeds())

	requests, err := fixture.resolver.RequestStatsByModel(fixture.ctx, nil)
	require.NoError(t, err)
	require.Len(t, requests, 3)
	require.Equal(t, "model-b", requests[0].ModelID)
	require.Equal(t, 3, requests[0].Count)

	costs, err := fixture.resolver.CostStatsByModel(fixture.ctx, nil)
	require.NoError(t, err)
	require.Equal(t, "model-a", costs[0].ModelID)
	require.InDelta(t, 100.0, costs[0].Cost, 0.001)

	tokens, err := fixture.resolver.TokenStatsByModel(fixture.ctx, nil)
	require.NoError(t, err)
	require.Equal(t, "model-c", tokens[0].ModelID)
	require.Equal(t, 900, tokens[0].TotalTokens)
}

func TestQueryResolver_APIKeyDimension_SharedScanKeepsPerChartOrdering(t *testing.T) {
	fixture := seedDimensionUsage(t, dimensionSeeds())

	requests, err := fixture.resolver.RequestStatsByAPIKey(fixture.ctx, nil)
	require.NoError(t, err)
	require.Len(t, requests, 3)
	require.Equal(t, "key-b", requests[0].APIKeyName)
	require.Equal(t, 3, requests[0].Count)

	costs, err := fixture.resolver.CostStatsByAPIKey(fixture.ctx, nil)
	require.NoError(t, err)
	require.Equal(t, "key-a", costs[0].APIKeyName)
	require.InDelta(t, 100.0, costs[0].Cost, 0.001)

	tokens, err := fixture.resolver.TokenStatsByAPIKey(fixture.ctx, nil)
	require.NoError(t, err)
	require.Equal(t, "key-c", tokens[0].APIKeyName)
	require.Equal(t, 900, tokens[0].TotalTokens)
}

// A soft-deleted channel must drop out of the channel charts even when its
// usage logs are still present.
func TestQueryResolver_ChannelDimension_ExcludesDeletedChannels(t *testing.T) {
	fixture := seedDimensionUsage(t, dimensionSeeds())

	target, err := fixture.client.Channel.Query().
		Where(channel.Name("beta")).
		Only(fixture.ctx)
	require.NoError(t, err)

	require.NoError(t, fixture.client.Channel.DeleteOneID(target.ID).Exec(fixture.ctx))

	InvalidateDashboardDimensionCaches()

	requests, err := fixture.resolver.RequestStatsByChannel(fixture.ctx, nil)
	require.NoError(t, err)

	for _, item := range requests {
		require.NotEqual(t, "beta", item.ChannelName)
	}
}

// A warm cache must not become a way around the dashboard scope: the cached
// path skips the ent query that would otherwise apply the privacy decision.
func TestQueryResolver_DimensionStats_DeniesWithoutDashboardScope(t *testing.T) {
	fixture := seedDimensionUsage(t, dimensionSeeds())

	// Populate the cache through an authorized call first.
	_, err := fixture.resolver.RequestStatsByChannel(fixture.ctx, nil)
	require.NoError(t, err)

	anonCtx := ent.NewContext(context.Background(), fixture.client)

	_, err = fixture.resolver.RequestStatsByChannel(anonCtx, nil)
	require.ErrorIs(t, err, privacy.Deny)

	_, err = fixture.resolver.TokenStatsByModel(anonCtx, nil)
	require.ErrorIs(t, err, privacy.Deny)

	_, err = fixture.resolver.CostStatsByAPIKey(anonCtx, nil)
	require.ErrorIs(t, err, privacy.Deny)
}

// The cached rows are shared by every caller, so a chart that sorts them must
// not reorder the cache entry underneath the others.
func TestQueryResolver_DimensionCache_SortingDoesNotMutateSharedRows(t *testing.T) {
	fixture := seedDimensionUsage(t, dimensionSeeds())

	first, err := fixture.resolver.channelUsageStats(fixture.ctx, timeWindowFilter{})
	require.NoError(t, err)

	before := make([]string, len(first))
	for i, row := range first {
		before[i] = row.ChannelName
	}

	// Runs the token ordering, which sorts by billable tokens.
	_, err = fixture.resolver.TokenStatsByChannel(fixture.ctx, nil)
	require.NoError(t, err)

	after, err := fixture.resolver.channelUsageStats(fixture.ctx, timeWindowFilter{})
	require.NoError(t, err)

	for i, row := range after {
		require.Equal(t, before[i], row.ChannelName)
	}
}
