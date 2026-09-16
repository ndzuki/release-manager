package sqlite_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ndzuki/release-manager/internal/store"
)

func TestAuditEventQueryCursorPagination(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()
	createdAt := time.Date(2026, 7, 16, 12, 0, 0, 123, time.UTC)

	for _, id := range []string{"event-001", "event-002", "event-003", "event-004", "event-005"} {
		require.NoError(t, st.AuditEvents().Create(ctx, &store.AuditEvent{
			ID:             id,
			ActorKind:      store.AuditActorUser,
			ActorID:        "user-001",
			OrganizationID: "org-001",
			Role:           string(store.RoleReleaseAdmin),
			ResourceType:   "release_operation",
			ResourceID:     id,
			Action:         "create",
			Status:         "accepted",
			Metadata:       map[string]string{"request_id": id},
			CreatedAt:      createdAt,
		}))
	}
	require.NoError(t, st.AuditEvents().Create(ctx, &store.AuditEvent{
		ID:             "other-org",
		OrganizationID: "org-002",
		Metadata:       map[string]string{},
		CreatedAt:      createdAt,
	}))

	filter := store.AuditEventFilter{OrganizationID: "org-001"}
	seen := make(map[string]struct{})
	cursor := ""
	for range 3 {
		page, err := st.AuditEvents().Query(ctx, filter, cursor, 2)
		require.NoError(t, err)
		for _, event := range page.Events {
			_, duplicate := seen[event.ID]
			assert.False(t, duplicate, "event %s repeated across pages", event.ID)
			seen[event.ID] = struct{}{}
			assert.Equal(t, "org-001", event.OrganizationID)
			assert.Equal(t, event.ID, event.Metadata["request_id"])
		}
		cursor = page.NextCursor
	}
	assert.Len(t, seen, 5)
	assert.Empty(t, cursor)
}

// TestAuditEventQueryStableSnapshotUnderConcurrentInsert verifies AC-010-03:
// paging over a stable keyset cursor while new events are inserted concurrently
// neither duplicates nor drops rows of the initial snapshot.
func TestAuditEventQueryStableSnapshotUnderConcurrentInsert(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()

	const (
		snapshotCount = 25
		pageSize      = 7
	)
	base := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	filter := store.AuditEventFilter{OrganizationID: "org-001"}

	create := func(id string, at time.Time) error {
		return st.AuditEvents().Create(ctx, &store.AuditEvent{
			ID:             id,
			ActorKind:      store.AuditActorUser,
			ActorID:        "user-001",
			OrganizationID: "org-001",
			Role:           string(store.RoleReleaseAdmin),
			ResourceType:   "release_operation",
			ResourceID:     id,
			Action:         "create",
			Status:         "accepted",
			Metadata:       map[string]string{"request_id": id},
			CreatedAt:      at,
		})
	}

	// 初始快照：created_at 严格递增，落在分页排序的稳定位置。
	snapshotIDs := make(map[string]struct{}, snapshotCount)
	for i := range snapshotCount {
		id := fmt.Sprintf("snap-%03d", i)
		snapshotIDs[id] = struct{}{}
		require.NoError(t, create(id, base.Add(time.Duration(i)*time.Minute)))
	}

	// 并发插入：created_at 散布在快照记录之间（+30s），翻页期间持续写入。
	insertErrors := make(chan error, 12)
	var insertWg sync.WaitGroup
	insertWg.Add(1)
	go func() {
		defer insertWg.Done()
		for i := range 12 {
			at := base.Add(time.Duration(i)*time.Minute + 30*time.Second)
			if err := create(fmt.Sprintf("insert-%03d", i), at); err != nil {
				insertErrors <- err
				return
			}
			time.Sleep(time.Millisecond)
		}
	}()

	seen := make(map[string]struct{})
	cursor := ""
	for {
		page, err := st.AuditEvents().Query(ctx, filter, cursor, pageSize)
		require.NoError(t, err)
		for _, event := range page.Events {
			_, duplicate := seen[event.ID]
			assert.False(t, duplicate, "event %s repeated across pages", event.ID)
			seen[event.ID] = struct{}{}
		}
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	insertWg.Wait()
	close(insertErrors)
	for err := range insertErrors {
		t.Errorf("concurrent insert failed: %v", err)
	}

	// 快照 N 条恰好各出现一次：无重复、无遗漏（新插入的记录只会追加，不影响快照集合）。
	snapshotSeen := 0
	for id := range seen {
		if _, ok := snapshotIDs[id]; ok {
			snapshotSeen++
		}
	}
	assert.Equal(t, snapshotCount, snapshotSeen, "snapshot rows must appear exactly once")
	assert.GreaterOrEqual(t, len(seen), snapshotCount)
}

func TestAuditEventQueryInvalidCursor(t *testing.T) {
	st := setupStore(t)
	_, err := st.AuditEvents().Query(context.Background(), store.AuditEventFilter{}, "not-base64", 10)
	assert.ErrorIs(t, err, store.ErrInvalidCursor)
}

func TestAuditEventCountFiltersOrganizationAndTimeRange(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()
	base := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	for index, organizationID := range []string{"org-001", "org-001", "org-002"} {
		require.NoError(t, st.AuditEvents().Create(ctx, &store.AuditEvent{
			ID:             organizationID + "-" + time.Duration(index).String(),
			OrganizationID: organizationID,
			Metadata:       map[string]string{},
			CreatedAt:      base.Add(time.Duration(index) * time.Hour),
		}))
	}
	since := base.Add(30 * time.Minute)
	until := base.Add(3 * time.Hour)
	count, err := st.AuditEvents().Count(ctx, store.AuditEventFilter{
		OrganizationID: "org-001",
		Since:          &since,
		Until:          &until,
	})
	require.NoError(t, err)
	assert.Equal(t, int64(1), count)
}

// TestAuditEventInsertIsIdempotentByID covers REQ-050/TASK-097 AC4: the event id
// is the deduplication key, so replaying the same event (a collector retry, or
// the emitter's spool replay after a crash) must not fail the batch nor store the
// row twice. Before this, a duplicate id failed the whole batch and the emitter
// retried it forever.
func TestAuditEventInsertIsIdempotentByID(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()
	event := &store.AuditEvent{
		ID: "event-idempotent", ActorKind: store.AuditActorSystem, ActorID: "system",
		OrganizationID: "org-1", ResourceType: "operator", ResourceID: "op-1",
		Action: "operator.revoked", Status: "succeeded", CreatedAt: time.Now().UTC(),
	}

	require.NoError(t, st.AuditEvents().Create(ctx, event))
	// A replay with different content keeps the original row (first write wins).
	replay := *event
	replay.Action = "operator.revoked.again"
	require.NoError(t, st.AuditEvents().Create(ctx, &replay), "a duplicate id must not fail the insert")

	require.NoError(t, st.AuditEvents().CreateBatch(ctx, []*store.AuditEvent{event, event}),
		"a batch containing duplicates must not fail")

	stored, err := st.AuditEvents().GetByID(ctx, "event-idempotent")
	require.NoError(t, err)
	assert.Equal(t, "operator.revoked", stored.Action, "the first write wins; a replay does not overwrite")

	page, err := st.AuditEvents().Query(ctx, store.AuditEventFilter{ResourceType: "operator", ResourceID: "op-1"}, "", 10)
	require.NoError(t, err)
	assert.Len(t, page.Events, 1, "the deduplication key is the event id")
}
