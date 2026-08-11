package redisstore

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	redis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ndzuki/release-manager/internal/store"
	sqlitestore "github.com/ndzuki/release-manager/internal/store/sqlite"
)

func TestAdapterCachesSessionsAndBackfillsMisses(t *testing.T) {
	ctx := t.Context()
	mini := miniredis.RunT(t)
	client := newTestRedisClient(t, mini.Addr())
	base := sqlitestore.OpenTest(t)
	adapter := New(client, base.AuthSessions())
	session := createSession(ctx, t, base, "cache", time.Hour)

	got, err := adapter.GetByRefreshHash(ctx, session.RefreshTokenHash)
	require.NoError(t, err)
	assert.Equal(t, session.ID, got.ID)
	assert.True(t, mini.Exists(sessionKey(session.RefreshTokenHash)))

	mini.Del(sessionKey(session.RefreshTokenHash))
	backfilled, err := adapter.GetByRefreshHash(ctx, session.RefreshTokenHash)
	require.NoError(t, err)
	assert.Equal(t, session.ID, backfilled.ID)
	assert.True(t, mini.Exists(sessionKey(session.RefreshTokenHash)))
}

func TestAdapterCachedSessionRespectsAuthoritativeRevocation(t *testing.T) {
	ctx := t.Context()
	mini := miniredis.RunT(t)
	client := newTestRedisClient(t, mini.Addr())
	base := sqlitestore.OpenTest(t)
	adapter := New(client, base.AuthSessions())
	session := createSession(ctx, t, base, "authoritative-revoke", time.Hour)

	_, err := adapter.GetByRefreshHash(ctx, session.RefreshTokenHash)
	require.NoError(t, err)
	require.NoError(t, base.AuthSessions().RevokeFamily(ctx, session.TokenFamily))

	got, err := adapter.GetByRefreshHash(ctx, session.RefreshTokenHash)
	require.NoError(t, err)
	assert.True(t, got.Revoked)
	assert.True(t, mini.Exists(blacklistKey(session.RefreshTokenHash)))
	assert.False(t, mini.Exists(sessionKey(session.RefreshTokenHash)))
}

func TestAdapterCreatePublishesRevokedSessionAsBlacklist(t *testing.T) {
	ctx := t.Context()
	mini := miniredis.RunT(t)
	client := newTestRedisClient(t, mini.Addr())
	base := sqlitestore.OpenTest(t)
	adapter := New(client, base.AuthSessions())
	revoked := &store.AuthSession{
		ID:               "session-created-revoked",
		UserID:           "user-created-revoked",
		TokenFamily:      "family-created-revoked",
		RefreshTokenHash: "hash-created-revoked",
		ExpiresAt:        time.Now().UTC().Add(time.Hour),
		Revoked:          true,
	}
	require.NoError(t, base.Users().Create(ctx, &store.User{ID: revoked.UserID, Username: "created-revoked", PasswordHash: "unused"}))
	require.NoError(t, adapter.Create(ctx, revoked))

	got, err := adapter.GetByRefreshHash(ctx, revoked.RefreshTokenHash)
	require.NoError(t, err)
	assert.True(t, got.Revoked)
	assert.Equal(t, revoked.TokenFamily, got.TokenFamily)
	assert.Equal(t, revoked.UserID, got.UserID)
}

func TestAdapterBlacklistPrecedesSessionCache(t *testing.T) {
	ctx := t.Context()
	mini := miniredis.RunT(t)
	client := newTestRedisClient(t, mini.Addr())
	base := sqlitestore.OpenTest(t)
	adapter := New(client, base.AuthSessions())
	session := createSession(ctx, t, base, "family", time.Hour)
	payload, err := encodeSession(session)
	require.NoError(t, err)
	require.NoError(t, client.Set(ctx, sessionKey(session.RefreshTokenHash), payload, time.Hour).Err())
	blacklistPayload, err := json.Marshal(blacklistedSession{UserID: session.UserID, TokenFamily: session.TokenFamily})
	require.NoError(t, err)
	require.NoError(t, client.Set(ctx, blacklistKey(session.RefreshTokenHash), blacklistPayload, time.Hour).Err())

	got, err := adapter.GetByRefreshHash(ctx, session.RefreshTokenHash)
	require.NoError(t, err)
	assert.True(t, got.Revoked)
	assert.Equal(t, session.TokenFamily, got.TokenFamily)
	assert.Equal(t, session.UserID, got.UserID)
}

func TestAdapterRevokeByUserIDBlacklistsKnownSessions(t *testing.T) {
	ctx := t.Context()
	mini := miniredis.RunT(t)
	client := newTestRedisClient(t, mini.Addr())
	base := sqlitestore.OpenTest(t)
	adapter := New(client, base.AuthSessions())
	first := createSession(ctx, t, base, "user-first", time.Hour)
	second := createSession(ctx, t, base, "user-second", 2*time.Hour)
	_, err := adapter.GetByRefreshHash(ctx, first.RefreshTokenHash)
	require.NoError(t, err)
	_, err = adapter.GetByRefreshHash(ctx, second.RefreshTokenHash)
	require.NoError(t, err)

	require.NoError(t, adapter.RevokeByUserID(ctx, first.UserID))
	for _, session := range []*store.AuthSession{first, second} {
		got, err := adapter.GetByRefreshHash(ctx, session.RefreshTokenHash)
		require.NoError(t, err)
		assert.True(t, got.Revoked)
		assert.True(t, mini.Exists(blacklistKey(session.RefreshTokenHash)))
		assert.False(t, mini.Exists(sessionKey(session.RefreshTokenHash)))
		persisted, err := base.AuthSessions().GetByRefreshHash(ctx, session.RefreshTokenHash)
		require.NoError(t, err)
		assert.True(t, persisted.Revoked)
	}
}

func TestAdapterCreatePreservesPriorBlacklistForSameRefreshHash(t *testing.T) {
	ctx := t.Context()
	mini := miniredis.RunT(t)
	client := newTestRedisClient(t, mini.Addr())
	base := sqlitestore.OpenTest(t)
	adapter := New(client, base.AuthSessions())
	old := createSession(ctx, t, base, "reuse-old", time.Hour)
	_, err := adapter.GetByRefreshHash(ctx, old.RefreshTokenHash)
	require.NoError(t, err)
	require.NoError(t, adapter.RevokeFamily(ctx, old.TokenFamily))

	fresh := &store.AuthSession{
		ID:               "session-reuse-new",
		UserID:           old.UserID,
		TokenFamily:      "family-reuse-new",
		RefreshTokenHash: old.RefreshTokenHash,
		ExpiresAt:        time.Now().UTC().Add(2 * time.Hour),
	}
	require.NoError(t, adapter.Create(ctx, fresh))

	got, err := adapter.GetByRefreshHash(ctx, fresh.RefreshTokenHash)
	require.NoError(t, err)
	assert.True(t, got.Revoked)
	assert.Equal(t, old.TokenFamily, got.TokenFamily)
}

func TestAdapterDirectMethodsRemainAuthoritative(t *testing.T) {
	ctx := t.Context()
	mini := miniredis.RunT(t)
	client := newTestRedisClient(t, mini.Addr())
	base := sqlitestore.OpenTest(t)
	adapter := New(client, base.AuthSessions())
	active := createSession(ctx, t, base, "direct-active", time.Hour)
	createSession(ctx, t, base, "direct-expired", -time.Minute)

	got, err := adapter.Get(ctx, active.ID)
	require.NoError(t, err)
	assert.Equal(t, active.RefreshTokenHash, got.RefreshTokenHash)
	family, err := adapter.GetByTokenFamily(ctx, active.TokenFamily)
	require.NoError(t, err)
	require.Len(t, family, 1)
	assert.Equal(t, active.ID, family[0].ID)
	hasActive, err := adapter.HasActiveByUserID(ctx, active.UserID)
	require.NoError(t, err)
	assert.True(t, hasActive)
	deleted, err := adapter.DeleteExpired(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(1), deleted)
}

func TestAdapterFailsClosedAndRecovers(t *testing.T) {
	ctx := t.Context()
	mini := miniredis.RunT(t)
	client := newTestRedisClient(t, mini.Addr())
	base := sqlitestore.OpenTest(t)
	adapter := New(client, base.AuthSessions())
	session := createSession(ctx, t, base, "outage", time.Hour)

	mini.Close()
	_, err := adapter.GetByRefreshHash(ctx, session.RefreshTokenHash)
	require.ErrorIs(t, err, store.ErrUnavailable)
	orphan := &store.AuthSession{
		ID:               "session-outage-write",
		UserID:           session.UserID,
		TokenFamily:      "family-outage-write",
		RefreshTokenHash: "hash-outage-write",
		ExpiresAt:        time.Now().UTC().Add(time.Hour),
	}
	err = adapter.Create(ctx, orphan)
	require.ErrorIs(t, err, store.ErrUnavailable)
	persisted, getErr := base.AuthSessions().GetByRefreshHash(ctx, orphan.RefreshTokenHash)
	require.NoError(t, getErr)
	assert.True(t, persisted.Revoked)

	require.NoError(t, mini.Restart())
	got, err := adapter.GetByRefreshHash(ctx, session.RefreshTokenHash)
	require.NoError(t, err)
	assert.Equal(t, session.ID, got.ID)
	recovered, err := adapter.GetByRefreshHash(ctx, orphan.RefreshTokenHash)
	require.NoError(t, err)
	assert.True(t, recovered.Revoked)
}

func TestAdapterRevocationPersistsWhenRedisIsUnavailable(t *testing.T) {
	tests := []struct {
		name   string
		revoke func(context.Context, *Adapter, *store.AuthSession) error
	}{
		{
			name: "family",
			revoke: func(ctx context.Context, adapter *Adapter, session *store.AuthSession) error {
				return adapter.RevokeFamily(ctx, session.TokenFamily)
			},
		},
		{
			name: "user",
			revoke: func(ctx context.Context, adapter *Adapter, session *store.AuthSession) error {
				return adapter.RevokeByUserID(ctx, session.UserID)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := t.Context()
			mini := miniredis.RunT(t)
			client := newTestRedisClient(t, mini.Addr())
			base := sqlitestore.OpenTest(t)
			adapter := New(client, base.AuthSessions())
			session := createSession(ctx, t, base, "revoke-"+tt.name, time.Hour)
			_, err := adapter.GetByRefreshHash(ctx, session.RefreshTokenHash)
			require.NoError(t, err)

			mini.Close()
			err = tt.revoke(ctx, adapter, session)
			require.ErrorIs(t, err, store.ErrUnavailable)
			persisted, getErr := base.AuthSessions().GetByRefreshHash(ctx, session.RefreshTokenHash)
			require.NoError(t, getErr)
			assert.True(t, persisted.Revoked)
		})
	}
}

func TestAdapterSessionAndBlacklistTTL(t *testing.T) {
	ctx := t.Context()
	mini := miniredis.RunT(t)
	client := newTestRedisClient(t, mini.Addr())
	base := sqlitestore.OpenTest(t)
	adapter := New(client, base.AuthSessions())
	session := createSession(ctx, t, base, "ttl", 30*time.Minute)

	_, err := adapter.GetByRefreshHash(ctx, session.RefreshTokenHash)
	require.NoError(t, err)
	ttl := mini.TTL(sessionKey(session.RefreshTokenHash))
	assert.Greater(t, ttl, 29*time.Minute)
	assert.LessOrEqual(t, ttl, 30*time.Minute)
	assert.Greater(t, mini.TTL(userKey(session.UserID)), 29*time.Minute)

	require.NoError(t, adapter.RevokeFamily(ctx, session.TokenFamily))
	blacklistTTL := mini.TTL(blacklistKey(session.RefreshTokenHash))
	assert.Greater(t, blacklistTTL, 29*time.Minute)
	assert.LessOrEqual(t, blacklistTTL, 30*time.Minute)
	assert.False(t, mini.Exists(sessionKey(session.RefreshTokenHash)))

	mini.FastForward(31 * time.Minute)
	assert.False(t, mini.Exists(blacklistKey(session.RefreshTokenHash)))
	assert.False(t, mini.Exists(userKey(session.UserID)))
}

func newTestRedisClient(t *testing.T, address string) *redis.Client {
	t.Helper()
	client := redis.NewClient(&redis.Options{
		Addr:                  address,
		MaxRetries:            -1,
		MinRetryBackoff:       -1,
		MaxRetryBackoff:       -1,
		DialerRetries:         1,
		DialerRetryTimeout:    time.Millisecond,
		DialTimeout:           250 * time.Millisecond,
		WriteTimeout:          250 * time.Millisecond,
		ContextTimeoutEnabled: true,
	})
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	return client
}

func createSession(ctx context.Context, t *testing.T, base *sqlitestore.Store, suffix string, ttl time.Duration) *store.AuthSession {
	t.Helper()
	userID := "user-adapter"
	if _, err := base.Users().Get(ctx, userID); errors.Is(err, store.ErrNotFound) {
		require.NoError(t, base.Users().Create(ctx, &store.User{ID: userID, Username: "adapter-user", PasswordHash: "unused"}))
	}
	session := &store.AuthSession{
		ID:               "session-" + suffix,
		UserID:           userID,
		TokenFamily:      "family-" + suffix,
		RefreshTokenHash: "hash-" + suffix,
		ExpiresAt:        time.Now().UTC().Add(ttl),
	}
	require.NoError(t, base.AuthSessions().Create(ctx, session))
	return session
}
