-- 模型并集（model union）：同一条公开模型允许通过多条路由指向不同平台。
--
-- 原唯一索引只覆盖 (group_id, endpoint, match_type, public_model)，等于强制
-- "一个公开模型在分组内只能有一个目标平台"。当 Ollama / OpenCode Go / DeepSeek
-- 都提供同一个模型时，用户希望把这些账号并成一个池子按整池调度，就必须能为同一个
-- public_model 建多条 target_platform 不同的路由。
--
-- 因此把 target_platform 并入唯一键：同一公开模型的同一平台仍然只能有一条，
-- 跨平台则允许并存。应用层（compositeRouteUnion）负责在这些同优先级的平台之间
-- 取并集并校验它们属于同一网关家族，跨家族的并集不会被调度。
--
-- DROP + CREATE 保证可重入。

DROP INDEX IF EXISTS idx_composite_model_routes_unique_active;

CREATE UNIQUE INDEX IF NOT EXISTS idx_composite_model_routes_unique_active
    ON composite_model_routes (group_id, endpoint, match_type, public_model, target_platform)
    WHERE deleted_at IS NULL;
