package service

import (
	"context"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/domain"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
)

// WithResolvedTargetPlatform stores the concrete provider chosen for a request
// made through a composite group.
func WithResolvedTargetPlatform(ctx context.Context, platform string) context.Context {
	platform = strings.TrimSpace(platform)
	if ctx == nil || platform == "" {
		return ctx
	}
	return context.WithValue(ctx, ctxkey.ResolvedTargetPlatform, platform)
}

// ResolvedTargetPlatformFromContext returns the concrete provider chosen for
// the current request, if one was resolved.
func ResolvedTargetPlatformFromContext(ctx context.Context) (string, bool) {
	if ctx == nil {
		return "", false
	}
	platform, ok := ctx.Value(ctxkey.ResolvedTargetPlatform).(string)
	platform = strings.TrimSpace(platform)
	if !ok || platform == "" {
		return "", false
	}
	return platform, true
}

func WithCompositeRouteDecision(ctx context.Context, decision CompositeRouteDecision) context.Context {
	if ctx == nil || !decision.Matched {
		return ctx
	}
	ctx = WithResolvedTargetPlatform(ctx, decision.TargetPlatform)
	if len(decision.TargetPlatforms) > 1 {
		ctx = WithResolvedTargetPlatforms(ctx, decision.TargetPlatforms)
	}
	if model := strings.TrimSpace(decision.UpstreamModel); model != "" {
		ctx = context.WithValue(ctx, ctxkey.ResolvedUpstreamModel, model)
	}
	if model := strings.TrimSpace(decision.PublicModel); model != "" {
		ctx = context.WithValue(ctx, ctxkey.RequestedPublicModel, model)
	}
	if source := strings.TrimSpace(decision.Source); source != "" {
		ctx = context.WithValue(ctx, ctxkey.CompositeRouteSource, source)
	}
	return ctx
}

// WithResolvedTargetPlatforms records the full same-gateway-family platform set
// a composite request may draw accounts from. The slice is normalised (trimmed,
// de-duplicated, order preserved) so callers can treat index 0 as the anchor.
func WithResolvedTargetPlatforms(ctx context.Context, platforms []string) context.Context {
	if ctx == nil {
		return ctx
	}
	normalized := make([]string, 0, len(platforms))
	seen := make(map[string]struct{}, len(platforms))
	for _, platform := range platforms {
		platform = strings.TrimSpace(platform)
		if platform == "" {
			continue
		}
		if _, dup := seen[platform]; dup {
			continue
		}
		seen[platform] = struct{}{}
		normalized = append(normalized, platform)
	}
	if len(normalized) < 2 {
		return ctx
	}
	return context.WithValue(ctx, ctxkey.ResolvedTargetPlatforms, normalized)
}

// ResolvedTargetPlatformsFromContext returns the platform set when the request
// resolved to a model-union, i.e. more than one platform may serve it.
func ResolvedTargetPlatformsFromContext(ctx context.Context) ([]string, bool) {
	if ctx == nil {
		return nil, false
	}
	platforms, ok := ctx.Value(ctxkey.ResolvedTargetPlatforms).([]string)
	if !ok || len(platforms) < 2 {
		return nil, false
	}
	return platforms, true
}

func ResolvedUpstreamModelFromContext(ctx context.Context) (string, bool) {
	if ctx == nil {
		return "", false
	}
	model, ok := ctx.Value(ctxkey.ResolvedUpstreamModel).(string)
	model = strings.TrimSpace(model)
	if !ok || model == "" {
		return "", false
	}
	return model, true
}

func RequestedPublicModelFromContext(ctx context.Context) (string, bool) {
	if ctx == nil {
		return "", false
	}
	model, ok := ctx.Value(ctxkey.RequestedPublicModel).(string)
	model = strings.TrimSpace(model)
	if !ok || model == "" {
		return "", false
	}
	return model, true
}

func CompositeRouteSourceFromContext(ctx context.Context) (string, bool) {
	if ctx == nil {
		return "", false
	}
	source, ok := ctx.Value(ctxkey.CompositeRouteSource).(string)
	source = strings.TrimSpace(source)
	if !ok || source == "" {
		return "", false
	}
	return source, true
}

// DetectModelPlatform maps common public model IDs to the concrete provider
// platform used by sub2api. It intentionally returns false for ambiguous model
// names so composite groups fail closed instead of guessing.
func DetectModelPlatform(model string) (string, bool) {
	normalized := strings.ToLower(strings.TrimSpace(model))
	if normalized == "" {
		return "", false
	}

	normalized = strings.TrimPrefix(normalized, "models/")
	if slash := strings.IndexByte(normalized, '/'); slash > 0 {
		provider := strings.TrimSpace(normalized[:slash])
		rest := strings.TrimSpace(normalized[slash+1:])
		switch provider {
		case "anthropic", "claude":
			return PlatformAnthropic, true
		case "openai", "chatgpt":
			return PlatformOpenAI, true
		case "google", "google-ai-studio", "gemini":
			return PlatformGemini, true
		case "xai", "x-ai", "grok":
			return PlatformGrok, true
		case "kimi", "moonshot":
			return PlatformKimi, true
		case "zhipu", "glm", "bigmodel":
			return PlatformZhipu, true
		case "deepseek":
			return PlatformDeepseek, true
		case "minimax":
			return PlatformMiniMax, true
		case "typesafe", "jev":
			return PlatformTypeSafe, true
		}
		if rest != "" {
			normalized = strings.TrimPrefix(rest, "models/")
		}
	}

	switch {
	case strings.HasPrefix(normalized, "anthropic.claude-"),
		strings.HasPrefix(normalized, "claude-"):
		return PlatformAnthropic, true
	case strings.HasPrefix(normalized, "gpt-"),
		strings.HasPrefix(normalized, "chatgpt-"),
		strings.HasPrefix(normalized, "codex-"),
		strings.HasPrefix(normalized, "text-embedding-"),
		strings.HasPrefix(normalized, "text-moderation-"),
		strings.HasPrefix(normalized, "omni-moderation-"),
		strings.HasPrefix(normalized, "dall-e-"),
		strings.HasPrefix(normalized, "gpt-image-"),
		strings.HasPrefix(normalized, "tts-"),
		strings.HasPrefix(normalized, "whisper-"),
		hasOpenAISeriesPrefix(normalized):
		return PlatformOpenAI, true
	case strings.HasPrefix(normalized, "gemini-"),
		strings.HasPrefix(normalized, "learnlm-"):
		return PlatformGemini, true
	case normalized == "grok" || strings.HasPrefix(normalized, "grok-"):
		return PlatformGrok, true
	case normalized == "k3",
		normalized == "k3-256k",
		strings.HasPrefix(normalized, "kimi-"),
		strings.HasPrefix(normalized, "moonshot-"):
		return PlatformKimi, true
	case strings.HasPrefix(normalized, "glm-"):
		return PlatformZhipu, true
	case strings.HasPrefix(normalized, "deepseek-"):
		return PlatformDeepseek, true
	case strings.HasPrefix(normalized, "minimax-"),
		strings.HasPrefix(normalized, "abab5"),
		strings.HasPrefix(normalized, "abab6"),
		strings.HasPrefix(normalized, "abab7"):
		return PlatformMiniMax, true
	case normalized == "jev-latest" || strings.HasPrefix(normalized, "jev-"):
		return PlatformTypeSafe, true
	default:
		return "", false
	}
}

func hasOpenAISeriesPrefix(model string) bool {
	for _, prefix := range []string{"o1", "o3", "o4", "o5"} {
		if model == prefix || strings.HasPrefix(model, prefix+"-") {
			return true
		}
	}
	return false
}

func (s *GatewayService) resolveCompositeRouteDecision(ctx context.Context, group *Group, requestedModel, endpoint string) (CompositeRouteDecision, bool, error) {
	if group == nil || group.Platform != PlatformComposite {
		return CompositeRouteDecision{}, false, nil
	}
	if platform, ok := ResolvedTargetPlatformFromContext(ctx); ok {
		upstreamModel := requestedModel
		if resolvedModel, modelOK := ResolvedUpstreamModelFromContext(ctx); modelOK {
			upstreamModel = resolvedModel
		}
		source := CompositeRouteSourceDetector
		if resolvedSource, sourceOK := CompositeRouteSourceFromContext(ctx); sourceOK {
			source = resolvedSource
		}
		var platforms []string
		if resolvedPlatforms, platformsOK := ResolvedTargetPlatformsFromContext(ctx); platformsOK {
			platforms = resolvedPlatforms
		}
		return CompositeRouteDecision{
			Matched:         true,
			Source:          source,
			GroupID:         group.ID,
			PublicModel:     requestedModel,
			TargetPlatform:  platform,
			TargetPlatforms: platforms,
			UpstreamModel:   upstreamModel,
			Endpoint:        normalizeCompositeRouteEndpoint(endpoint),
		}, true, nil
	}
	decision, err := s.compositeResolver.Resolve(ctx, group.ID, requestedModel, endpoint)
	if err != nil {
		return decision, false, err
	}
	return decision, decision.Matched, nil
}

func isConcreteRequestPlatform(platform string) bool {
	return domain.IsConcretePlatform(platform)
}

// compositePlatformsShareGatewayFamily reports whether every platform in the
// set is served by the same gateway service.
//
// This is the precondition for pooling accounts across platforms for one
// model: the request handler is chosen from the resolved platform *before* an
// account is selected, so a union is only servable when one handler covers
// every member. Platforms behind different gateways need the dispatch itself
// to move after account selection, which is out of scope here.
//
// The comparison is on the gateway itself, not on "is it the OpenAI gateway":
// anthropic and antigravity are both non-OpenAI yet are separate gateways.
func compositePlatformsShareGatewayFamily(platforms []string) bool {
	if len(platforms) < 2 {
		return false
	}
	var anchorGateway domain.PlatformGateway
	for _, platform := range platforms {
		spec, ok := domain.LookupPlatform(strings.TrimSpace(platform))
		if !ok {
			return false
		}
		if anchorGateway == "" {
			anchorGateway = spec.Gateway
			continue
		}
		if spec.Gateway != anchorGateway {
			return false
		}
	}
	return true
}
