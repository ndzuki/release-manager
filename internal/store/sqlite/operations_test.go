package sqlite_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ndzuki/release-manager/internal/store"
)

// TestCreateIdempotentConcurrentSameKey verifies TASK-010 域幂等并发语义：
// 并发同 scope/key/hash 双写必须一个 created、一个 replayed，且返回同一 OperationID。
func TestCreateIdempotentConcurrentSameKey(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()
	def := createTestDefinition(t, st)

	now := time.Now().UTC()
	command := store.OperationCreateCommand{
		Operation: &store.Operation{
			ID:                  uuid.NewString(),
			OperationType:       store.OperationInstall,
			Status:              store.StatusPending,
			ReleaseDefinitionID: def.ID,
			CreatedAt:           now,
			UpdatedAt:           now,
		},
		Idempotency: &store.IdempotencyRecord{
			Scope: "org:" + def.ID, Key: "concurrent-key-1", RequestHash: "hash-1",
			ExpiresAt: now.Add(time.Hour),
		},
	}

	type outcome struct {
		result *store.OperationCreateResult
		err    error
	}
	results := make(chan outcome, 2)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			res, err := st.Operations().CreateIdempotent(ctx, command)
			results <- outcome{result: res, err: err}
		}()
	}
	close(start)
	wg.Wait()
	close(results)

	var first *store.OperationCreateResult
	createdCount, replayedCount := 0, 0
	for out := range results {
		require.NoError(t, out.err)
		require.NotNil(t, out.result)
		if out.result.Replayed {
			replayedCount++
		} else {
			createdCount++
		}
		if first == nil {
			first = out.result
		} else {
			assert.Equal(t, first.Operation.ID, out.result.Operation.ID)
		}
		assert.Equal(t, command.Operation.ID, out.result.Operation.ID)
	}
	assert.Equal(t, 1, createdCount, "exactly one caller must create the operation")
	assert.Equal(t, 1, replayedCount, "the concurrent caller must replay the existing operation")

	// 重放结果必须能回到持久化的操作（非命令中的暂存对象）。
	persisted, err := st.Operations().Get(ctx, command.Operation.ID)
	require.NoError(t, err)
	assert.Equal(t, store.StatusPending, persisted.Status)
	assert.Equal(t, 1, persisted.StateVersion)
}

// TestCreateIdempotentEmptyKeySkipsIdempotency verifies 空 Key（或未携带幂等记录）
// 时跳过 lookup/insert，不 panic、不写 idempotency_records，直接业务创建。
func TestCreateIdempotentEmptyKeySkipsIdempotency(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()
	def := createTestDefinition(t, st)

	now := time.Now().UTC()
	// Idempotency 为 nil。
	res, err := st.Operations().CreateIdempotent(ctx, store.OperationCreateCommand{
		Operation: &store.Operation{
			ID: uuid.NewString(), OperationType: store.OperationInstall, Status: store.StatusPending,
			ReleaseDefinitionID: def.ID, IdempotencyKey: "empty-key-op-1", CreatedAt: now, UpdatedAt: now,
		},
	})
	require.NoError(t, err)
	require.NotNil(t, res.Operation)
	assert.False(t, res.Replayed)

	// Key 为空字符串。
	res2, err := st.Operations().CreateIdempotent(ctx, store.OperationCreateCommand{
		Operation: &store.Operation{
			ID: uuid.NewString(), OperationType: store.OperationInstall, Status: store.StatusPending,
			ReleaseDefinitionID: def.ID, IdempotencyKey: "empty-key-op-2", CreatedAt: now, UpdatedAt: now,
		},
		Idempotency: &store.IdempotencyRecord{
			Scope: "org:" + def.ID, Key: "", RequestHash: "hash-2", ExpiresAt: now.Add(time.Hour),
		},
	})
	require.NoError(t, err)
	require.NotNil(t, res2.Operation)
	assert.False(t, res2.Replayed)

	// 空 Key 路径不得写入任何幂等记录。
	var count int
	require.NoError(t, st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM idempotency_records`).Scan(&count))
	assert.Zero(t, count)
}
