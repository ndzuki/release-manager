// Package redisstore decorates the authoritative auth session store with Redis
// cache and refresh-token blacklist semantics. See ADR-019.
package redisstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	redis "github.com/redis/go-redis/v9"

	"github.com/ndzuki/release-manager/internal/store"
)

const (
	sessionKeyPrefix   = "auth:sess:"
	blacklistKeyPrefix = "auth:blacklist:"
	userKeyPrefix      = "auth:user:"
)

// Adapter keeps PostgreSQL or SQLite authoritative while using Redis for
// refresh-session lookup and revoked-token rejection.
type Adapter struct {
	client *redis.Client
	base   store.AuthSessionStore
}

// New creates an auth session store backed by an authoritative store and Redis.
func New(client *redis.Client, base store.AuthSessionStore) *Adapter {
	return &Adapter{client: client, base: base}
}

// Create persists the session first, then publishes an active cache entry or
// a blacklist tombstone without removing any existing tombstone for the hash.
// If Redis publication fails, the durable family is revoked before the error
// is returned so an undelivered token cannot leave an active session behind.
func (a *Adapter) Create(ctx context.Context, session *store.AuthSession) error {
	if err := a.base.Create(ctx, session); err != nil {
		return err
	}
	var publishErr error
	if session.Revoked {
		publishErr = a.blacklistSessions(ctx, []*store.AuthSession{session})
	} else {
		publishErr = a.cacheSession(ctx, session)
	}
	if publishErr == nil {
		return nil
	}
	if revokeErr := a.base.RevokeFamily(ctx, session.TokenFamily); revokeErr != nil {
		return errors.Join(publishErr, fmt.Errorf("revoke unpublished auth session family: %w", revokeErr))
	}
	return publishErr
}

// Get reads the authoritative store directly.
func (a *Adapter) Get(ctx context.Context, id string) (*store.AuthSession, error) {
	return a.base.Get(ctx, id)
}

// GetByRefreshHash rejects blacklisted hashes before consulting the cache. A
// cache miss is backfilled from the authoritative store.
func (a *Adapter) GetByRefreshHash(ctx context.Context, hash string) (*store.AuthSession, error) {
	payload, err := a.client.Get(ctx, blacklistKey(hash)).Bytes()
	switch {
	case err == nil:
		var blacklisted blacklistedSession
		if decodeErr := json.Unmarshal(payload, &blacklisted); decodeErr != nil {
			return nil, unavailable("decode refresh blacklist", decodeErr)
		}
		return &store.AuthSession{
			UserID:           blacklisted.UserID,
			TokenFamily:      blacklisted.TokenFamily,
			RefreshTokenHash: hash,
			Revoked:          true,
		}, nil
	case !errors.Is(err, redis.Nil):
		return nil, unavailable("read refresh blacklist", err)
	}

	if session, found, err := a.readCachedSession(ctx, hash); err != nil {
		return nil, err
	} else if found {
		return session, nil
	}

	session, err := a.base.GetByRefreshHash(ctx, hash)
	if err != nil {
		return nil, err
	}
	if session.Revoked {
		if err := a.blacklistSessions(ctx, []*store.AuthSession{session}); err != nil {
			return nil, err
		}
		return session, nil
	}
	if err := a.cacheSession(ctx, session); err != nil {
		return nil, err
	}
	return session, nil
}

// GetByTokenFamily reads the authoritative store directly.
func (a *Adapter) GetByTokenFamily(ctx context.Context, family string) ([]*store.AuthSession, error) {
	return a.base.GetByTokenFamily(ctx, family)
}

// RevokeFamily commits the authoritative revocation before publishing Redis
// blacklist entries and removing cached active representations. Redis failures
// remain visible to callers, but durable revocation is never skipped.
func (a *Adapter) RevokeFamily(ctx context.Context, family string) error {
	sessions, err := a.base.GetByTokenFamily(ctx, family)
	if err != nil {
		return err
	}
	if err := a.base.RevokeFamily(ctx, family); err != nil {
		return err
	}
	return a.blacklistSessions(ctx, sessions)
}

// HasActiveByUserID reads the authoritative store directly.
func (a *Adapter) HasActiveByUserID(ctx context.Context, userID string) (bool, error) {
	return a.base.HasActiveByUserID(ctx, userID)
}

func (a *Adapter) readCachedSession(ctx context.Context, hash string) (*store.AuthSession, bool, error) {
	payload, err := a.client.Get(ctx, sessionKey(hash)).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, unavailable("read session cache", err)
	}
	cached, err := decodeSession(payload)
	if err != nil {
		return nil, false, unavailable("decode session cache", err)
	}
	session, err := a.base.GetByRefreshHash(ctx, hash)
	if err != nil {
		return nil, false, err
	}
	if session.Revoked {
		if err := a.blacklistSessions(ctx, []*store.AuthSession{session}); err != nil {
			return nil, false, err
		}
		return session, true, nil
	}
	if !sameCachedSession(cached, session) {
		if err := a.cacheSession(ctx, session); err != nil {
			return nil, false, err
		}
	}
	return session, true, nil
}

func sameCachedSession(cached, authoritative *store.AuthSession) bool {
	return cached.ID == authoritative.ID &&
		cached.UserID == authoritative.UserID &&
		cached.TokenFamily == authoritative.TokenFamily &&
		cached.RefreshTokenHash == authoritative.RefreshTokenHash &&
		cached.ExpiresAt.Equal(authoritative.ExpiresAt) &&
		cached.Revoked == authoritative.Revoked
}

// RevokeByUserID commits the authoritative user revocation before invalidating
// every cached session known to the user's Redis index. The authoritative
// session list is used so Redis index availability cannot block revocation.
func (a *Adapter) RevokeByUserID(ctx context.Context, userID string) error {
	if err := a.base.RevokeByUserID(ctx, userID); err != nil {
		return err
	}
	hashes, err := a.client.SMembers(ctx, userKey(userID)).Result()
	if err != nil {
		return unavailable("read user session index", err)
	}
	sessions := make([]*store.AuthSession, 0, len(hashes))
	for _, hash := range hashes {
		session, getErr := a.base.GetByRefreshHash(ctx, hash)
		if errors.Is(getErr, store.ErrNotFound) {
			continue
		}
		if getErr != nil {
			return getErr
		}
		sessions = append(sessions, session)
	}
	return a.blacklistSessions(ctx, sessions)
}

// DeleteExpired lets the authoritative store remove durable expired rows. Redis
// keys expire independently according to the same session deadline.
func (a *Adapter) DeleteExpired(ctx context.Context) (int64, error) {
	return a.base.DeleteExpired(ctx)
}

func (a *Adapter) cacheSession(ctx context.Context, session *store.AuthSession) error {
	ttl := time.Until(session.ExpiresAt)
	if ttl <= 0 {
		return nil
	}
	payload, err := encodeSession(session)
	if err != nil {
		return fmt.Errorf("encode auth session cache: %w", err)
	}
	_, err = a.client.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
		pipe.Set(ctx, sessionKey(session.RefreshTokenHash), payload, ttl)
		pipe.SAdd(ctx, userKey(session.UserID), session.RefreshTokenHash)
		pipe.ExpireNX(ctx, userKey(session.UserID), ttl)
		pipe.ExpireGT(ctx, userKey(session.UserID), ttl)
		return nil
	})
	if err != nil {
		return unavailable("write session cache", err)
	}
	return nil
}

type blacklistedSession struct {
	UserID      string `json:"user_id"`
	TokenFamily string `json:"token_family"`
}

func (a *Adapter) blacklistSessions(ctx context.Context, sessions []*store.AuthSession) error {
	if len(sessions) == 0 {
		return nil
	}
	_, err := a.client.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
		for _, session := range sessions {
			ttl := time.Until(session.ExpiresAt)
			if ttl > 0 {
				payload, err := json.Marshal(blacklistedSession{UserID: session.UserID, TokenFamily: session.TokenFamily})
				if err != nil {
					return err
				}
				pipe.Set(ctx, blacklistKey(session.RefreshTokenHash), payload, ttl)
			}
			pipe.Del(ctx, sessionKey(session.RefreshTokenHash))
			pipe.SRem(ctx, userKey(session.UserID), session.RefreshTokenHash)
		}
		return nil
	})
	if err != nil {
		return unavailable("write refresh blacklist", err)
	}
	return nil
}

type cachedSession struct {
	ID               string    `json:"id"`
	UserID           string    `json:"user_id"`
	TokenFamily      string    `json:"token_family"`
	RefreshTokenHash string    `json:"refresh_token_hash"`
	ExpiresAt        time.Time `json:"expires_at"`
	CreatedAt        time.Time `json:"created_at"`
	Revoked          bool      `json:"revoked"`
}

func encodeSession(session *store.AuthSession) ([]byte, error) {
	return json.Marshal(cachedSession{
		ID:               session.ID,
		UserID:           session.UserID,
		TokenFamily:      session.TokenFamily,
		RefreshTokenHash: session.RefreshTokenHash,
		ExpiresAt:        session.ExpiresAt,
		CreatedAt:        session.CreatedAt,
		Revoked:          session.Revoked,
	})
}

func decodeSession(payload []byte) (*store.AuthSession, error) {
	var cached cachedSession
	if err := json.Unmarshal(payload, &cached); err != nil {
		return nil, err
	}
	return &store.AuthSession{
		ID:               cached.ID,
		UserID:           cached.UserID,
		TokenFamily:      cached.TokenFamily,
		RefreshTokenHash: cached.RefreshTokenHash,
		ExpiresAt:        cached.ExpiresAt,
		CreatedAt:        cached.CreatedAt,
		Revoked:          cached.Revoked,
	}, nil
}

func sessionKey(hash string) string   { return sessionKeyPrefix + hash }
func blacklistKey(hash string) string { return blacklistKeyPrefix + hash }
func userKey(userID string) string    { return userKeyPrefix + userID }

func unavailable(operation string, err error) error {
	return fmt.Errorf("%s: %v: %w", operation, err, store.ErrUnavailable)
}

var _ store.AuthSessionStore = (*Adapter)(nil)
