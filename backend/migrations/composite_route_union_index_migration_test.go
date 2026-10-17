package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The model-union scheduler pools accounts across every platform that a single
// public model resolves to. That configuration is only expressible if one group
// may hold several routes for the same public model, so the unique index must
// key on target_platform as well.
func TestMigration246AllowsOneRoutePerPlatformAndKeepsThemUnique(t *testing.T) {
	content, err := FS.ReadFile("246_composite_route_union_unique_index.sql")
	require.NoError(t, err)

	sql := string(content)
	require.Contains(t, sql, "DROP INDEX IF EXISTS idx_composite_model_routes_unique_active")
	require.Contains(t, sql, "CREATE UNIQUE INDEX IF NOT EXISTS idx_composite_model_routes_unique_active")
	require.Contains(t, sql,
		"(group_id, endpoint, match_type, public_model, target_platform)",
		"target_platform must join the key or the union cannot be configured")
	require.Contains(t, sql, "WHERE deleted_at IS NULL")

	// Dropping without recreating, or recreating without the prior drop, would
	// each break either idempotency or the guarantee itself.
	require.Less(t, strings.Index(sql, "DROP INDEX IF EXISTS"), strings.Index(sql, "CREATE UNIQUE INDEX IF NOT EXISTS"),
		"the old index must be dropped before the widened one is created")
}
