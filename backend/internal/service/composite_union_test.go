package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// Model-union MVP: one public model served by several platforms that all sit
// behind the same gateway is pooled instead of refused. Platforms behind
// different gateways stay unsupported, because the request handler is chosen
// from the resolved platform before an account is selected.

func TestCompositePlatformsShareGatewayFamily(t *testing.T) {
	for _, tc := range []struct {
		name      string
		platforms []string
		want      bool
	}{
		{"single platform is never a union", []string{PlatformDeepseek}, false},
		{"openai-gateway family", []string{PlatformDeepseek, PlatformOpenCodeGo}, true},
		{"whole openai family", []string{PlatformDeepseek, PlatformKimi, PlatformZhipu, PlatformMiniMax, PlatformOpenCodeGo}, true},
		{"anthropic alone in its family", []string{PlatformAnthropic, PlatformOpenAI}, false},
		{"anthropic plus openai family", []string{PlatformAnthropic, PlatformDeepseek}, false},
		{"two non-openai gateways are still two gateways", []string{PlatformAnthropic, PlatformAntigravity}, false},
		{"unknown platform refuses", []string{PlatformDeepseek, "not-a-platform"}, false},
		{"blank entry refuses", []string{PlatformDeepseek, ""}, false},
		{"duplicates collapse to one family", []string{PlatformDeepseek, PlatformDeepseek}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, compositePlatformsShareGatewayFamily(tc.platforms))
		})
	}
}

func TestResolveCompositeModelOwnershipPoolsSameGatewayFamily(t *testing.T) {
	const groupID = 7
	repo := &compositeOwnershipAccountRepo{
		accounts: []Account{
			{ID: 1, Platform: PlatformOpenCodeGo, Credentials: map[string]any{
				"model_mapping": map[string]any{"deepseek-flash": "deepseek-v4.1-flash"}}},
			{ID: 2, Platform: PlatformDeepseek, Credentials: map[string]any{
				"model_mapping": map[string]any{"deepseek-flash": "deepseek-v4.1-flash"}}},
			{ID: 3, Platform: PlatformKimi, Credentials: map[string]any{
				"model_mapping": map[string]any{"deepseek-flash": "deepseek-v4.1-flash"}}},
		},
	}
	svc := &GatewayService{accountRepo: repo}

	ownership, err := svc.resolveCompositeModelOwnership(context.Background(), groupID, "deepseek-flash")
	require.NoError(t, err)
	require.True(t, ownership.Matched)
	require.False(t, ownership.Ambiguous)
	// Sorted, so the anchor does not depend on Go's map iteration order.
	require.Equal(t, []string{PlatformDeepseek, PlatformKimi, PlatformOpenCodeGo}, ownership.Platforms)
	require.Equal(t, PlatformDeepseek, ownership.TargetPlatform)
}

func TestResolveCompositeModelOwnershipRefusesCrossGatewayUnion(t *testing.T) {
	const groupID = 7
	repo := &compositeOwnershipAccountRepo{
		accounts: []Account{
			{ID: 1, Platform: PlatformAnthropic, Credentials: map[string]any{
				"model_mapping": map[string]any{"deepseek-flash": "deepseek-v4.1-flash"}}},
			{ID: 2, Platform: PlatformOpenCodeGo, Credentials: map[string]any{
				"model_mapping": map[string]any{"deepseek-flash": "deepseek-v4.1-flash"}}},
		},
	}
	svc := &GatewayService{accountRepo: repo}

	ownership, err := svc.resolveCompositeModelOwnership(context.Background(), groupID, "deepseek-flash")
	require.NoError(t, err)
	require.True(t, ownership.Ambiguous)
	require.False(t, ownership.Matched)
	require.Empty(t, ownership.Platforms)
}

func TestCompositeRouteUnion(t *testing.T) {
	exact := func(id int64, platform string, priority int) CompositeModelRoute {
		return CompositeModelRoute{
			ID: id, GroupID: 1, PublicModel: "deepseek-flash", MatchType: CompositeRouteMatchExact,
			Endpoint: CompositeRouteEndpointAny, TargetPlatform: platform, Priority: priority, Enabled: true,
		}
	}

	t.Run("same specificity tier pools platforms", func(t *testing.T) {
		routes := []CompositeModelRoute{
			exact(1, PlatformDeepseek, 1),
			exact(2, PlatformOpenCodeGo, 2),
			exact(3, PlatformKimi, 3),
		}
		route, platforms, ok := compositeRouteUnion(routes, "deepseek-flash", CompositeRouteEndpointAny)
		require.True(t, ok)
		require.EqualValues(t, 1, route.ID, "lowest priority wins as anchor")
		require.Equal(t, []string{PlatformDeepseek, PlatformOpenCodeGo, PlatformKimi}, platforms)
	})

	t.Run("cross gateway family keeps the anchor only", func(t *testing.T) {
		routes := []CompositeModelRoute{
			exact(1, PlatformDeepseek, 1),
			exact(2, PlatformAnthropic, 2),
		}
		route, platforms, ok := compositeRouteUnion(routes, "deepseek-flash", CompositeRouteEndpointAny)
		require.True(t, ok)
		require.EqualValues(t, 1, route.ID)
		require.Nil(t, platforms)
	})

	t.Run("single route is unchanged", func(t *testing.T) {
		route, platforms, ok := compositeRouteUnion([]CompositeModelRoute{exact(1, PlatformDeepseek, 1)},
			"deepseek-flash", CompositeRouteEndpointAny)
		require.True(t, ok)
		require.EqualValues(t, 1, route.ID)
		require.Nil(t, platforms)
	})

	t.Run("a zero-ID route still matches", func(t *testing.T) {
		// Routes constructed in memory — and rows read back before an ID is
		// assigned — legitimately carry ID 0. Match must come from the returned
		// flag, never from testing the route for a zero value.
		routes := []CompositeModelRoute{{
			GroupID: 1, PublicModel: "deepseek-flash", MatchType: CompositeRouteMatchExact,
			Endpoint: CompositeRouteEndpointAny, TargetPlatform: PlatformDeepseek, Enabled: true,
		}}
		route, platforms, ok := compositeRouteUnion(routes, "deepseek-flash", CompositeRouteEndpointAny)
		require.True(t, ok)
		require.Equal(t, PlatformDeepseek, route.TargetPlatform)
		require.Nil(t, platforms)
	})

	t.Run("different specificity does not pool", func(t *testing.T) {
		routes := []CompositeModelRoute{
			{ID: 1, GroupID: 1, PublicModel: "deepseek-flash", MatchType: CompositeRouteMatchExact,
				Endpoint: CompositeRouteEndpointAny, TargetPlatform: PlatformDeepseek, Enabled: true},
			{ID: 2, GroupID: 1, PublicModel: "deepseek", MatchType: CompositeRouteMatchPrefix,
				Endpoint: CompositeRouteEndpointAny, TargetPlatform: PlatformOpenCodeGo, Enabled: true},
		}
		route, platforms, ok := compositeRouteUnion(routes, "deepseek-flash", CompositeRouteEndpointAny)
		require.True(t, ok)
		require.EqualValues(t, 1, route.ID, "the exact route beats the prefix route")
		require.Nil(t, platforms)
	})
}

func TestCompositeRouteDecisionCarriesUnionPlatforms(t *testing.T) {
	resolver := NewCompositeRouteResolver(compositeRouteRepoStub{routes: []CompositeModelRoute{
		{ID: 1, GroupID: 3, PublicModel: "deepseek-flash", MatchType: CompositeRouteMatchExact,
			Endpoint: CompositeRouteEndpointAny, TargetPlatform: PlatformDeepseek, Enabled: true},
		{ID: 2, GroupID: 3, PublicModel: "deepseek-flash", MatchType: CompositeRouteMatchExact,
			Endpoint: CompositeRouteEndpointAny, TargetPlatform: PlatformOpenCodeGo, Enabled: true},
	}})

	decision, err := resolver.Resolve(context.Background(), 3, "deepseek-flash", CompositeRouteEndpointMessages)
	require.NoError(t, err)
	require.True(t, decision.Matched)
	require.Equal(t, PlatformDeepseek, decision.TargetPlatform)
	require.Equal(t, []string{PlatformDeepseek, PlatformOpenCodeGo}, decision.TargetPlatforms)

	// The decision must reach the scheduler through the request context.
	ctx := WithCompositeRouteDecision(context.Background(), decision)
	platforms, ok := ResolvedTargetPlatformsFromContext(ctx)
	require.True(t, ok)
	require.Equal(t, []string{PlatformDeepseek, PlatformOpenCodeGo}, platforms)

	anchor, ok := ResolvedTargetPlatformFromContext(ctx)
	require.True(t, ok)
	require.Equal(t, PlatformDeepseek, anchor, "the anchor stays the single-platform answer")
}

func TestResolvedTargetPlatformsFromContextRequiresASet(t *testing.T) {
	ctx := WithResolvedTargetPlatforms(context.Background(), []string{PlatformDeepseek})
	_, ok := ResolvedTargetPlatformsFromContext(ctx)
	require.False(t, ok, "a single platform is not a union")
}

// The bucket token is produced twice from independent inputs — once when a
// request resolves a union, once when a background rebuild derives it from the
// stored routes. Both must land on the same Redis key.
func TestSchedulerPlatformSetTokenIsCanonical(t *testing.T) {
	require.Equal(t, "deepseek", SchedulerPlatformSetToken([]string{PlatformDeepseek}),
		"a single platform keeps the plain token so existing bucket keys survive")
	require.Equal(t,
		SchedulerPlatformSetToken([]string{PlatformOpenCodeGo, PlatformDeepseek}),
		SchedulerPlatformSetToken([]string{PlatformDeepseek, PlatformOpenCodeGo}))
	require.Equal(t, "deepseek+opencode_go",
		SchedulerPlatformSetToken([]string{PlatformOpenCodeGo, PlatformDeepseek}))
	require.Equal(t, "deepseek+opencode_go",
		SchedulerPlatformSetToken([]string{PlatformDeepseek, PlatformOpenCodeGo, PlatformDeepseek}),
		"duplicates collapse")
	require.Equal(t, "deepseek", SchedulerPlatformSetToken([]string{"", PlatformDeepseek, " "}))
}

func TestUnionPlatformSetTokens(t *testing.T) {
	exact := func(id int64, model, platform string, enabled bool) CompositeModelRoute {
		return CompositeModelRoute{
			ID: id, GroupID: 9, PublicModel: model, MatchType: CompositeRouteMatchExact,
			Endpoint: CompositeRouteEndpointAny, TargetPlatform: platform,
			Priority: int(id), Enabled: enabled,
		}
	}

	t.Run("a same-family tier yields one token", func(t *testing.T) {
		tokens := unionPlatformSetTokens([]CompositeModelRoute{
			exact(1, "deepseek-flash", PlatformDeepseek, true),
			exact(2, "deepseek-flash", PlatformOpenCodeGo, true),
		})
		require.Equal(t, []string{"deepseek+opencode_go"}, tokens)
	})

	t.Run("a cross-family tier yields nothing", func(t *testing.T) {
		tokens := unionPlatformSetTokens([]CompositeModelRoute{
			exact(1, "deepseek-flash", PlatformDeepseek, true),
			exact(2, "deepseek-flash", PlatformAnthropic, true),
		})
		require.Empty(t, tokens, "cross-family tiers are never pooled, so they get no bucket")
	})

	t.Run("a single-platform tier yields nothing", func(t *testing.T) {
		tokens := unionPlatformSetTokens([]CompositeModelRoute{
			exact(1, "deepseek-flash", PlatformDeepseek, true),
		})
		require.Empty(t, tokens)
	})

	t.Run("a disabled route does not create a union", func(t *testing.T) {
		tokens := unionPlatformSetTokens([]CompositeModelRoute{
			exact(1, "deepseek-flash", PlatformDeepseek, true),
			exact(2, "deepseek-flash", PlatformOpenCodeGo, false),
		})
		require.Empty(t, tokens)
	})

	t.Run("separate tiers yield separate tokens", func(t *testing.T) {
		tokens := unionPlatformSetTokens([]CompositeModelRoute{
			exact(1, "deepseek-flash", PlatformDeepseek, true),
			exact(2, "deepseek-flash", PlatformOpenCodeGo, true),
			exact(3, "kimi-flash", PlatformDeepseek, true),
			exact(4, "kimi-flash", PlatformKimi, true),
		})
		require.ElementsMatch(t, []string{"deepseek+opencode_go", "deepseek+kimi"}, tokens)
	})

	t.Run("the same union reached by two models yields one token", func(t *testing.T) {
		tokens := unionPlatformSetTokens([]CompositeModelRoute{
			exact(1, "a", PlatformDeepseek, true),
			exact(2, "a", PlatformOpenCodeGo, true),
			exact(3, "b", PlatformOpenCodeGo, true),
			exact(4, "b", PlatformDeepseek, true),
		})
		require.Equal(t, []string{"deepseek+opencode_go"}, tokens)
	})
}

func TestUnionBucketsForGroupsFiltersByPlatform(t *testing.T) {
	repo := compositeRouteRepoStub{routes: []CompositeModelRoute{
		{ID: 1, GroupID: 11, PublicModel: "m", MatchType: CompositeRouteMatchExact,
			Endpoint: CompositeRouteEndpointAny, TargetPlatform: PlatformDeepseek, Enabled: true},
		{ID: 2, GroupID: 11, PublicModel: "m", MatchType: CompositeRouteMatchExact,
			Endpoint: CompositeRouteEndpointAny, TargetPlatform: PlatformOpenCodeGo, Enabled: true},
		{ID: 3, GroupID: 11, PublicModel: "n", MatchType: CompositeRouteMatchExact,
			Endpoint: CompositeRouteEndpointAny, TargetPlatform: PlatformDeepseek, Enabled: true},
		{ID: 4, GroupID: 11, PublicModel: "n", MatchType: CompositeRouteMatchExact,
			Endpoint: CompositeRouteEndpointAny, TargetPlatform: PlatformKimi, Enabled: true},
		{ID: 5, GroupID: 11, PublicModel: "x", MatchType: CompositeRouteMatchExact,
			Endpoint: CompositeRouteEndpointAny, TargetPlatform: PlatformDeepseek, Enabled: true},
		{ID: 6, GroupID: 11, PublicModel: "x", MatchType: CompositeRouteMatchExact,
			Endpoint: CompositeRouteEndpointAny, TargetPlatform: PlatformAnthropic, Enabled: true},
	}}
	svc := &SchedulerSnapshotService{compositeRouteRepo: repo}

	all := svc.unionBucketsForGroups(context.Background(), []int64{11}, "")
	require.Len(t, all, 4, "two unions, each in single and forced mode")
	require.Contains(t, all, SchedulerBucket{GroupID: 11, Platform: "deepseek+opencode_go", Mode: SchedulerModeSingle})
	require.Contains(t, all, SchedulerBucket{GroupID: 11, Platform: "deepseek+kimi", Mode: SchedulerModeForced})
	require.NotContains(t, all, SchedulerBucket{GroupID: 11, Platform: "anthropic+deepseek", Mode: SchedulerModeSingle},
		"the cross-family tier must not produce a bucket")

	onlyKimis := svc.unionBucketsForGroups(context.Background(), []int64{11}, PlatformKimi)
	require.Len(t, onlyKimis, 2, "kimi appears in one union only")
	require.Equal(t, "deepseek+kimi", onlyKimis[0].Platform)

	require.Empty(t, svc.unionBucketsForGroups(context.Background(), []int64{11}, PlatformAnthropic),
		"a platform that only appears in cross-family tiers contributes no union bucket")

	// A group with no routes, and a nil repository, both degrade to nothing.
	require.Empty(t, svc.unionBucketsForGroups(context.Background(), []int64{99}, ""))
	require.Empty(t, (&SchedulerSnapshotService{}).unionBucketsForGroups(context.Background(), []int64{11}, ""))
}
