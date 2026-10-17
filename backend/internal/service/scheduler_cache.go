package service

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	SchedulerModeSingle = "single"
	SchedulerModeMixed  = "mixed"
	SchedulerModeForced = "forced"
)

var (
	ErrSchedulerBucketRetired              = errors.New("scheduler bucket retired")
	ErrSchedulerBucketWriteFenced          = errors.New("scheduler bucket write fenced")
	ErrSchedulerGroupLifecycleLeaseInvalid = errors.New("scheduler group lifecycle lease invalid")
	ErrSchedulerGroupLifecycleLeaseLost    = errors.New("scheduler group lifecycle lease lost")
)

// SchedulerBucketWriteToken fences a snapshot writer to one bucket epoch.
// Tokens must be captured before any database load or queued rebuild work.
type SchedulerBucketWriteToken struct {
	Bucket SchedulerBucket
	Epoch  int64
}

func (t SchedulerBucketWriteToken) ValidFor(bucket SchedulerBucket) bool {
	return t.Epoch > 0 && t.Bucket == bucket
}

// SchedulerGroupLifecycleLease identifies one owner of a group's short-lived
// retirement/reopen critical section.
type SchedulerGroupLifecycleLease struct {
	GroupID    int64
	OwnerToken string
}

func (l SchedulerGroupLifecycleLease) ValidFor(groupID int64) bool {
	return groupID > 0 && l.GroupID == groupID && l.OwnerToken != ""
}

// schedulerPlatformSetSeparator joins a model-union platform set into the
// single wire token a SchedulerBucket carries. "+" rather than ":" because
// String/ParseSchedulerBucket split buckets on ":".
const schedulerPlatformSetSeparator = "+"

// SchedulerPlatformSetToken renders a platform set as one canonical bucket
// token. A single platform keeps its plain form, so existing bucket keys — and
// the Redis entries behind them — are unchanged.
//
// The order is fixed by sorting rather than taken from the caller: the same set
// is produced both when a request resolves a union and when a background rebuild
// derives one from the group's composite routes. Those two producers must agree
// on the key exactly, or an invalidation would rebuild a bucket nobody reads and
// leave the live one stale.
func SchedulerPlatformSetToken(platforms []string) string {
	trimmed := make([]string, 0, len(platforms))
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
		trimmed = append(trimmed, platform)
	}
	if len(trimmed) < 2 {
		return strings.Join(trimmed, schedulerPlatformSetSeparator)
	}
	sort.Strings(trimmed)
	return strings.Join(trimmed, schedulerPlatformSetSeparator)
}

// Platforms returns the platform set a bucket covers. A plain single-platform
// bucket yields exactly one entry.
func (b SchedulerBucket) Platforms() []string {
	if !strings.Contains(b.Platform, schedulerPlatformSetSeparator) {
		if b.Platform == "" {
			return nil
		}
		return []string{b.Platform}
	}
	return strings.Split(b.Platform, schedulerPlatformSetSeparator)
}

// IsPlatformSet reports whether the bucket covers more than one platform, i.e.
// it came from a model-union composite route.
func (b SchedulerBucket) IsPlatformSet() bool {
	return strings.Contains(b.Platform, schedulerPlatformSetSeparator)
}

type SchedulerBucket struct {
	GroupID  int64
	Platform string
	Mode     string
}

func (b SchedulerBucket) String() string {
	return fmt.Sprintf("%d:%s:%s", b.GroupID, b.Platform, b.Mode)
}

func ParseSchedulerBucket(raw string) (SchedulerBucket, bool) {
	parts := strings.Split(raw, ":")
	if len(parts) != 3 {
		return SchedulerBucket{}, false
	}
	groupID, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return SchedulerBucket{}, false
	}
	if parts[1] == "" || parts[2] == "" {
		return SchedulerBucket{}, false
	}
	return SchedulerBucket{
		GroupID:  groupID,
		Platform: parts[1],
		Mode:     parts[2],
	}, true
}

// SchedulerCache 负责调度快照与账号快照的缓存读写。
type SchedulerCache interface {
	// GetSnapshot 读取快照并返回命中与否（ready + active + 数据完整）。
	GetSnapshot(ctx context.Context, bucket SchedulerBucket) ([]*Account, bool, error)
	// CaptureBucketWriteToken captures the current open epoch without changing
	// retirement state. A tombstoned bucket returns ErrSchedulerBucketRetired.
	CaptureBucketWriteToken(ctx context.Context, bucket SchedulerBucket) (SchedulerBucketWriteToken, error)
	// SetSnapshot 写入快照并切换激活版本。token 必须在 DB load/任务排队前取得。
	SetSnapshot(ctx context.Context, bucket SchedulerBucket, token SchedulerBucketWriteToken, accounts []Account) error
	// RetireBucket persistently tombstones a bucket and fences every older writer.
	// Readers that captured the active version before retirement may finish; new
	// readers observe ready/active as absent.
	RetireBucket(ctx context.Context, bucket SchedulerBucket) error
	// ReopenBucket is the only operation allowed to clear a tombstone. It returns
	// the retirement generation established by RetireBucket; repeated calls for
	// the same generation are idempotent. Callers must serialize a fresh authority
	// check through ReopenBucket with RetireBucket under the same group lifecycle
	// lease; ordinary rebuild paths never call ReopenBucket.
	ReopenBucket(ctx context.Context, bucket SchedulerBucket) (SchedulerBucketWriteToken, error)
	// TryAcquireGroupLifecycleLease serializes authoritative retirement/reopen
	// decisions for one non-zero group across instances.
	TryAcquireGroupLifecycleLease(ctx context.Context, groupID int64, ttl time.Duration) (SchedulerGroupLifecycleLease, bool, error)
	// ReleaseGroupLifecycleLease releases the lease only if its owner token still
	// matches, so an expired holder cannot delete a successor's lease. Missing,
	// expired, mismatched, and already released leases return
	// ErrSchedulerGroupLifecycleLeaseLost.
	ReleaseGroupLifecycleLease(ctx context.Context, lease SchedulerGroupLifecycleLease) error
	// GetAccount 获取单账号快照。
	GetAccount(ctx context.Context, accountID int64) (*Account, error)
	// SetAccount 写入单账号快照（包含不可调度状态）。
	SetAccount(ctx context.Context, account *Account) error
	// DeleteAccount 删除单账号快照。
	DeleteAccount(ctx context.Context, accountID int64) error
	// UpdateLastUsed 批量更新账号的最后使用时间。
	UpdateLastUsed(ctx context.Context, updates map[int64]time.Time) error
	// TryLockBucket 尝试获取分桶重建锁。
	TryLockBucket(ctx context.Context, bucket SchedulerBucket, ttl time.Duration) (bool, error)
	// UnlockBucket 释放分桶重建锁。
	UnlockBucket(ctx context.Context, bucket SchedulerBucket) error
	// ListBuckets 返回已注册的分桶集合。
	ListBuckets(ctx context.Context) ([]SchedulerBucket, error)
	// GetOutboxWatermark 读取 outbox 水位。
	GetOutboxWatermark(ctx context.Context) (int64, error)
	// SetOutboxWatermark 保存 outbox 水位。
	SetOutboxWatermark(ctx context.Context, id int64) error
}
