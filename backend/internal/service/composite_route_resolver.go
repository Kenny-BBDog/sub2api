package service

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/domain"
)

const (
	// CompositeRouteReasonAmbiguous marks a model whose explicit account
	// mappings span several provider platforms.
	CompositeRouteReasonAmbiguous = "model is exposed by multiple provider platforms"
	// CompositeRouteReasonOAuthAmbiguous is the ambiguous case where a claimant
	// holds an OAuth / setup-token credential that only the OpenAI gateway can
	// carry, and the model-name fallback would also pick an OpenAI-gateway
	// platform. The unresolved request would land in the Anthropic gateway
	// handler and select those credentials, so this group misconfiguration must
	// be rejected instead of guessing a platform from the model name.
	CompositeRouteReasonOAuthAmbiguous = "model is exposed by multiple provider platforms including OAuth accounts"
)

// OAuthAmbiguous reports whether the request must be rejected because the
// cross-platform ownership involves OpenAI-gateway OAuth credentials.
func (d CompositeRouteDecision) OAuthAmbiguous() bool {
	return !d.Matched && d.Reason == CompositeRouteReasonOAuthAmbiguous
}

// AmbiguousMessage is the client-facing rejection for an OAuthAmbiguous decision.
func (d CompositeRouteDecision) AmbiguousMessage() string {
	return fmt.Sprintf("Model %q is mapped on multiple provider platforms in this group; configure an explicit composite route", d.PublicModel)
}

type CompositeRouteResolver struct {
	repo                   CompositeModelRouteRepository
	modelOwnershipResolver CompositeModelOwnershipResolver
}

func NewCompositeRouteResolver(repo CompositeModelRouteRepository) *CompositeRouteResolver {
	return &CompositeRouteResolver{repo: repo}
}

func (r *CompositeRouteResolver) SetModelOwnershipResolver(resolver CompositeModelOwnershipResolver) {
	if r != nil {
		r.modelOwnershipResolver = resolver
	}
}

// ListExactPublicModels returns enabled, concrete route IDs suitable for a model catalog.
func (r *CompositeRouteResolver) ListExactPublicModels(ctx context.Context, groupID int64, endpoint string, includeSystemOne bool) ([]string, error) {
	if r == nil || r.repo == nil || groupID <= 0 {
		return nil, nil
	}
	routes, err := r.repo.ListByGroup(ctx, groupID, false)
	if err != nil {
		return nil, err
	}
	models := make([]string, 0, len(routes))
	for _, route := range routes {
		if route.Enabled && route.MatchType == CompositeRouteMatchExact &&
			(endpoint == "" || normalizeCompositeRouteEndpoint(route.Endpoint) == CompositeRouteEndpointAny || normalizeCompositeRouteEndpoint(route.Endpoint) == endpoint) {
			if model := strings.TrimSpace(route.PublicModel); model != "" {
				models = append(models, model)
			}
		}
	}
	if !includeSystemOne {
		models = filterSystemOneRouteModels(routes, models, endpoint)
	}
	return models, nil
}

func (r *CompositeRouteResolver) FilterCodexModels(ctx context.Context, groupID int64, models []string) ([]string, error) {
	if r == nil || r.repo == nil || groupID <= 0 {
		return models, nil
	}
	routes, err := r.repo.ListByGroup(ctx, groupID, false)
	if err != nil {
		return nil, err
	}
	return filterSystemOneRouteModels(routes, models, CompositeRouteEndpointResponses), nil
}

func filterSystemOneRouteModels(routes []CompositeModelRoute, models []string, endpoint string) []string {
	filtered := make([]string, 0, len(models))
	for _, model := range models {
		route, matched := matchCompositeRoute(routes, model, normalizeCompositeRouteEndpoint(endpoint))
		if !matched || route.TargetPlatform != PlatformTypeSafe {
			filtered = append(filtered, model)
		}
	}
	return filtered
}

func (r *CompositeRouteResolver) Resolve(ctx context.Context, groupID int64, model, endpoint string) (CompositeRouteDecision, error) {
	model = strings.TrimSpace(model)
	endpoint = normalizeCompositeRouteEndpoint(endpoint)
	decision := CompositeRouteDecision{
		GroupID:     groupID,
		PublicModel: model,
		Endpoint:    endpoint,
	}
	if model == "" {
		decision.Reason = "model is required"
		return decision, nil
	}

	if r != nil && r.repo != nil && groupID > 0 {
		routes, err := r.repo.ListByGroup(ctx, groupID, false)
		if err != nil {
			return decision, fmt.Errorf("list composite routes: %w", err)
		}
		if route, unionPlatforms, matched := compositeRouteUnion(routes, model, endpoint); matched {
			upstreamModel := strings.TrimSpace(route.UpstreamModel)
			if upstreamModel == "" {
				upstreamModel = model
			}
			return CompositeRouteDecision{
				Matched:         true,
				Source:          CompositeRouteSourceExplicit,
				GroupID:         groupID,
				PublicModel:     model,
				TargetPlatform:  route.TargetPlatform,
				TargetPlatforms: unionPlatforms,
				UpstreamModel:   upstreamModel,
				Endpoint:        endpoint,
				Route:           &route,
			}, nil
		}
	}

	if r != nil && r.modelOwnershipResolver != nil && groupID > 0 {
		ownership, err := r.modelOwnershipResolver(ctx, groupID, model)
		if err != nil {
			// A recognizable model can still use the existing detector when the
			// account catalog is temporarily unavailable. Unknown aliases cannot.
			if _, detectable := DetectModelPlatform(model); !detectable {
				return decision, fmt.Errorf("resolve account model ownership: %w", err)
			}
		} else if ownership.Ambiguous {
			decision.Reason = CompositeRouteReasonAmbiguous
			// 未解析的请求由下游 handler 按模型名前缀兜底选平台；只有兜底落在
			// OpenAI 网关族时，OpenAI 网关 OAuth 声明方才会被误送进 Anthropic 管线。
			if guessed, ok := DetectModelPlatform(model); ok && ownership.OpenAIGatewayOAuthClaimed && domain.UsesOpenAIGateway(guessed) {
				decision.Reason = CompositeRouteReasonOAuthAmbiguous
			}
			return decision, nil
		} else if ownership.Matched {
			platform := strings.TrimSpace(ownership.TargetPlatform)
			if !isConcreteRequestPlatform(platform) {
				decision.Reason = "account model ownership has no concrete target platform"
				return decision, nil
			}
			return CompositeRouteDecision{
				Matched:         true,
				Source:          CompositeRouteSourceAccount,
				GroupID:         groupID,
				PublicModel:     model,
				TargetPlatform:  platform,
				TargetPlatforms: ownership.Platforms,
				UpstreamModel:   model,
				Endpoint:        endpoint,
			}, nil
		}
	}

	if platform, ok := DetectModelPlatform(model); ok {
		return CompositeRouteDecision{
			Matched:        true,
			Source:         CompositeRouteSourceDetector,
			GroupID:        groupID,
			PublicModel:    model,
			TargetPlatform: platform,
			UpstreamModel:  model,
			Endpoint:       endpoint,
		}, nil
	}
	decision.Reason = "no explicit route or built-in detector match"
	return decision, nil
}

// compositeRouteCandidate carries the sort keys used to rank routes matching
// one requested model. matchStrength, endpointWeight and prefixLen together
// define a specificity tier: routes tied on all three are equally specific, and
// that tier is what the model-union scheduler pools across platforms.
type compositeRouteCandidate struct {
	route          CompositeModelRoute
	matchStrength  int
	endpointWeight int
	prefixLen      int
}

func matchCompositeRoute(routes []CompositeModelRoute, model, endpoint string) (CompositeModelRoute, bool) {
	candidates := rankCompositeRouteCandidates(routes, model, endpoint)
	if len(candidates) == 0 {
		return CompositeModelRoute{}, false
	}
	return candidates[0].route, true
}

// compositeRouteUnion returns the winning route plus every other platform in
// the same specificity tier, when that tier maps one public model to several
// platforms behind a single gateway. platforms is nil for the historical
// single-platform case (including a tier that spans gateway families, which is
// not servable yet because the request handler is picked before an account is).
//
// matched must be used instead of testing the route for a zero value: routes
// built in memory — and rows read back before an ID is assigned — legitimately
// carry ID 0.
func compositeRouteUnion(routes []CompositeModelRoute, model, endpoint string) (CompositeModelRoute, []string, bool) {
	candidates := rankCompositeRouteCandidates(routes, model, endpoint)
	if len(candidates) == 0 {
		return CompositeModelRoute{}, nil, false
	}
	top := candidates[0]
	anchor := strings.TrimSpace(top.route.TargetPlatform)
	if !isConcreteRequestPlatform(anchor) {
		return top.route, nil, true
	}
	seen := map[string]struct{}{anchor: {}}
	platforms := []string{anchor}
	for _, c := range candidates[1:] {
		if c.matchStrength != top.matchStrength || c.endpointWeight != top.endpointWeight || c.prefixLen != top.prefixLen {
			break
		}
		platform := strings.TrimSpace(c.route.TargetPlatform)
		if !isConcreteRequestPlatform(platform) {
			continue
		}
		if _, dup := seen[platform]; dup {
			continue
		}
		seen[platform] = struct{}{}
		platforms = append(platforms, platform)
	}
	if len(platforms) < 2 {
		return top.route, nil, true
	}
	if !compositePlatformsShareGatewayFamily(platforms) {
		slog.Warn("composite route union spans gateway families; keeping the winning route only",
			"model", model, "anchor", anchor, "platforms", platforms)
		return top.route, nil, true
	}
	return top.route, platforms, true
}

func rankCompositeRouteCandidates(routes []CompositeModelRoute, model, endpoint string) []compositeRouteCandidate {
	if len(routes) == 0 {
		return nil
	}

	candidates := make([]compositeRouteCandidate, 0, len(routes))
	for _, route := range routes {
		route.Endpoint = normalizeCompositeRouteEndpoint(route.Endpoint)
		if route.Endpoint != endpoint && route.Endpoint != CompositeRouteEndpointAny {
			continue
		}
		route.MatchType = normalizeCompositeRouteMatchType(route.MatchType)
		publicModel := strings.TrimSpace(route.PublicModel)
		if publicModel == "" {
			continue
		}

		matchStrength := 0
		prefixLen := len(publicModel)
		switch route.MatchType {
		case CompositeRouteMatchExact:
			if publicModel != model {
				continue
			}
			matchStrength = 2
		case CompositeRouteMatchPrefix:
			if !strings.HasPrefix(model, publicModel) {
				continue
			}
			matchStrength = 1
		default:
			continue
		}
		endpointWeight := 0
		if route.Endpoint == endpoint {
			endpointWeight = 1
		}
		candidates = append(candidates, compositeRouteCandidate{
			route:          route,
			matchStrength:  matchStrength,
			endpointWeight: endpointWeight,
			prefixLen:      prefixLen,
		})
	}
	if len(candidates) == 0 {
		return nil
	}

	sort.SliceStable(candidates, func(i, j int) bool {
		a, b := candidates[i], candidates[j]
		if a.matchStrength != b.matchStrength {
			return a.matchStrength > b.matchStrength
		}
		if a.endpointWeight != b.endpointWeight {
			return a.endpointWeight > b.endpointWeight
		}
		if a.prefixLen != b.prefixLen {
			return a.prefixLen > b.prefixLen
		}
		if a.route.Priority != b.route.Priority {
			return a.route.Priority < b.route.Priority
		}
		return a.route.ID < b.route.ID
	})
	return candidates
}
