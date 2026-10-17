package service

import (
	"context"
	"log/slog"
	"strings"
)

// Model-union buckets cannot be enumerated from the platform registry the way
// canonical buckets are: `groupID:platformA+platformB:mode` only exists because
// a group's composite routes map one public model to two platforms. So when an
// account changes, the platform-scoped invalidation paths — which walk
// schedulerSnapshotPlatforms() — would miss every union bucket and leave a stale
// snapshot behind. These helpers read the sets back from the routes so the same
// invalidation reaches them.

// SetCompositeRouteRepository wires the repository used to discover a group's
// model-union platform sets. Optional: without it the canonical, single-platform
// invalidation is unchanged and union buckets are only built lazily on demand.
func (s *SchedulerSnapshotService) SetCompositeRouteRepository(repo CompositeModelRouteRepository) {
	if s != nil {
		s.compositeRouteRepo = repo
	}
}

// unionPlatformSetTokens returns the canonical bucket tokens for every model
// union a group's routes currently define.
//
// Routes are grouped by the same key the database enforces unique
// (endpoint, match_type, public_model), which is exactly the specificity tier
// compositeRouteUnion pools across. A tier that resolves to two or more concrete
// platforms behind one gateway yields one token.
func unionPlatformSetTokens(routes []CompositeModelRoute) []string {
	type tierKey struct {
		endpoint   string
		matchType  string
		publicMode string
	}
	tiers := make(map[tierKey][]string)
	order := make([]tierKey, 0, len(routes))

	for _, route := range routes {
		if !route.Enabled {
			continue
		}
		publicModel := strings.TrimSpace(route.PublicModel)
		if publicModel == "" {
			continue
		}
		matchType := normalizeCompositeRouteMatchType(route.MatchType)
		if matchType != CompositeRouteMatchExact && matchType != CompositeRouteMatchPrefix {
			continue
		}
		platform := strings.TrimSpace(route.TargetPlatform)
		if !isConcreteRequestPlatform(platform) {
			continue
		}
		key := tierKey{
			endpoint:   normalizeCompositeRouteEndpoint(route.Endpoint),
			matchType:  matchType,
			publicMode: publicModel,
		}
		if _, seen := tiers[key]; !seen {
			order = append(order, key)
		}
		tiers[key] = append(tiers[key], platform)
	}

	tokens := make([]string, 0, len(order))
	seenTokens := make(map[string]struct{}, len(order))
	for _, key := range order {
		platforms := tiers[key]
		if len(platforms) < 2 {
			continue
		}
		if !compositePlatformsShareGatewayFamily(platforms) {
			// Mirrors compositeRouteUnion: a cross-family tier is never pooled,
			// so it never gets a union bucket.
			continue
		}
		token := SchedulerPlatformSetToken(platforms)
		if _, dup := seenTokens[token]; dup {
			continue
		}
		seenTokens[token] = struct{}{}
		tokens = append(tokens, token)
	}
	return tokens
}

// unionBucketsForGroups returns the union buckets a set of groups may hold, in
// both modes, so a platform-scoped change can invalidate them alongside the
// canonical buckets for the group.
//
// A non-empty platform restricts the result to unions that contain it, so a
// change to one platform does not trigger a cross-platform rebuild of every
// unrelated union in the group.
func (s *SchedulerSnapshotService) unionBucketsForGroups(ctx context.Context, groupIDs []int64, platform string) []SchedulerBucket {
	if s == nil || s.compositeRouteRepo == nil || len(groupIDs) == 0 {
		return nil
	}
	platform = strings.TrimSpace(platform)
	buckets := make([]SchedulerBucket, 0, len(groupIDs))
	seen := make(map[SchedulerBucket]struct{}, len(groupIDs))
	for _, groupID := range groupIDs {
		if groupID <= 0 {
			continue
		}
		routes, err := s.compositeRouteRepo.ListByGroup(ctx, groupID, false)
		if err != nil {
			// A failed lookup must not silently skip invalidation for this group:
			// fall back to leaving union buckets to their lazy rebuild, but say so.
			slog.Warn("scheduler union bucket lookup failed; union snapshots for this group may stay stale",
				"group_id", groupID, "error", err)
			continue
		}
		for _, token := range unionPlatformSetTokens(routes) {
			if platform != "" && !schedulerPlatformSetContains(token, platform) {
				continue
			}
			for _, mode := range []string{SchedulerModeSingle, SchedulerModeForced} {
				bucket := SchedulerBucket{GroupID: groupID, Platform: token, Mode: mode}
				if _, dup := seen[bucket]; dup {
					continue
				}
				seen[bucket] = struct{}{}
				buckets = append(buckets, bucket)
			}
		}
	}
	return buckets
}

// schedulerPlatformSetContains reports whether a canonical bucket token lists
// the given platform.
func schedulerPlatformSetContains(token, platform string) bool {
	for _, member := range strings.Split(token, schedulerPlatformSetSeparator) {
		if member == platform {
			return true
		}
	}
	return false
}
