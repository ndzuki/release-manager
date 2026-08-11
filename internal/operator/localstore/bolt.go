package localstore

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	bolt "go.etcd.io/bbolt"
)

// bucket names
var (
	commandsBucket = []byte("commands")
	byOutboxBucket = []byte("by_outbox")
	identityBucket = []byte("identity")
	sequenceKey    = []byte("__last_sequence__")
	identityKey    = []byte("__identity__")
)

// OpenBolt opens (or creates) a BoltDB-backed command + identity store at the
// given path. The returned store implements both Store (commands) and
// IdentityStore (bootstrap identity).
func OpenBolt(path string) (*BoltStore, error) {
	db, err := bolt.Open(path, 0o600, nil)
	if err != nil {
		return nil, fmt.Errorf("open bbolt store: %w", err)
	}

	// Create buckets.
	err = db.Update(func(tx *bolt.Tx) error {
		if _, err := tx.CreateBucketIfNotExists(commandsBucket); err != nil {
			return fmt.Errorf("create commands bucket: %w", err)
		}
		if _, err := tx.CreateBucketIfNotExists(byOutboxBucket); err != nil {
			return fmt.Errorf("create by_outbox bucket: %w", err)
		}
		if _, err := tx.CreateBucketIfNotExists(identityBucket); err != nil {
			return fmt.Errorf("create identity bucket: %w", err)
		}
		return nil
	})
	if err != nil {
		db.Close()
		return nil, err
	}

	return &BoltStore{db: db}, nil
}

// BoltStore implements Store and IdentityStore backed by a single BoltDB file
// (commands and identity share one database with separate buckets).
type BoltStore struct {
	db *bolt.DB
}

// SaveIdentity durably persists the bootstrap identity under a single key.
func (s *BoltStore) SaveIdentity(_ context.Context, identity *Identity) error {
	if identity == nil {
		return fmt.Errorf("identity is required")
	}
	encoded, err := json.Marshal(identity)
	if err != nil {
		return fmt.Errorf("marshal identity: %w", err)
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(identityBucket)
		if err := b.Put(identityKey, encoded); err != nil {
			return fmt.Errorf("put identity: %w", err)
		}
		return nil
	})
}

// LoadIdentity returns the persisted identity or ErrNotFound when the agent
// has never bootstrapped.
func (s *BoltStore) LoadIdentity(_ context.Context) (*Identity, error) {
	var encoded []byte
	if err := s.db.View(func(tx *bolt.Tx) error {
		encoded = tx.Bucket(identityBucket).Get(identityKey)
		return nil
	}); err != nil {
		return nil, fmt.Errorf("get identity: %w", err)
	}
	if len(encoded) == 0 {
		return nil, ErrNotFound
	}
	var identity Identity
	if err := json.Unmarshal(encoded, &identity); err != nil {
		return nil, fmt.Errorf("unmarshal identity: %w", err)
	}
	return &identity, nil
}

// Save persists a command entry with fsync.
func (s *BoltStore) Save(_ context.Context, e *CommandEntry) error {
	data, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("marshal command entry: %w", err)
	}

	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(commandsBucket)
		if err := b.Put([]byte(e.CommandID), data); err != nil {
			return fmt.Errorf("put command: %w", err)
		}

		// Maintain outbox_id → command_id index.
		if e.OutboxID != "" {
			ob := tx.Bucket(byOutboxBucket)
			if err := ob.Put([]byte(e.OutboxID), []byte(e.CommandID)); err != nil {
				return fmt.Errorf("put outbox index: %w", err)
			}
		}

		// Update last sequence.
		if e.Sequence > 0 {
			seqStr := strconv.FormatInt(e.Sequence, 10)
			if err := b.Put(sequenceKey, []byte(seqStr)); err != nil {
				return fmt.Errorf("put sequence: %w", err)
			}
		}

		return nil
	})
}

// Get retrieves a command by command_id.
func (s *BoltStore) Get(_ context.Context, commandID string) (*CommandEntry, error) {
	var e *CommandEntry
	err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(commandsBucket)
		data := b.Get([]byte(commandID))
		if data == nil {
			return nil
		}
		e = &CommandEntry{}
		return json.Unmarshal(data, e)
	})
	if err != nil {
		return nil, fmt.Errorf("get command: %w", err)
	}
	if e == nil {
		return nil, fmt.Errorf("command %s: %w", commandID, ErrNotFound)
	}
	return e, nil
}

// GetByOutboxID retrieves a command by outbox_id via the index.
func (s *BoltStore) GetByOutboxID(ctx context.Context, outboxID string) (*CommandEntry, error) {
	var commandID string
	err := s.db.View(func(tx *bolt.Tx) error {
		ob := tx.Bucket(byOutboxBucket)
		data := ob.Get([]byte(outboxID))
		if data != nil {
			commandID = string(data)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("get by outbox_id: %w", err)
	}
	if commandID == "" {
		return nil, fmt.Errorf("outbox %s: %w", outboxID, ErrNotFound)
	}
	return s.Get(ctx, commandID)
}

// UpdateStatus updates the status and result of a command.
func (s *BoltStore) UpdateStatus(_ context.Context, commandID, status, resultJSON string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(commandsBucket)
		data := b.Get([]byte(commandID))
		if data == nil {
			return fmt.Errorf("command %s: %w", commandID, ErrNotFound)
		}

		var e CommandEntry
		if err := json.Unmarshal(data, &e); err != nil {
			return fmt.Errorf("unmarshal command: %w", err)
		}

		e.Status = status
		if resultJSON != "" {
			e.ResultJSON = resultJSON
		}

		updated, err := json.Marshal(&e)
		if err != nil {
			return fmt.Errorf("marshal updated command: %w", err)
		}

		return b.Put([]byte(commandID), updated)
	})
}

// ListActive returns all non-terminal commands.
func (s *BoltStore) ListActive(_ context.Context) ([]*CommandEntry, error) {
	var entries []*CommandEntry
	err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(commandsBucket)
		c := b.Cursor()
		for k, v := c.First(); k != nil; k, v = c.Next() {
			// Skip the sequence metadata key.
			if bytes.Equal(k, sequenceKey) {
				continue
			}

			var e CommandEntry
			if err := json.Unmarshal(v, &e); err != nil {
				return fmt.Errorf("unmarshal command %s: %w", string(k), err)
			}

			if !IsTerminal(e.Status) {
				entries = append(entries, &e)
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("list active: %w", err)
	}
	return entries, nil
}

// LastSequence returns the highest stored sequence number.
func (s *BoltStore) LastSequence(_ context.Context) (int64, error) {
	var seq int64
	err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(commandsBucket)
		data := b.Get(sequenceKey)
		if data != nil {
			var err error
			seq, err = strconv.ParseInt(string(data), 10, 64)
			if err != nil {
				return fmt.Errorf("parse sequence: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return seq, nil
}

// Close releases the database.
func (s *BoltStore) Close() error {
	return s.db.Close()
}

// ErrNotFound is returned when a command is not found in the local store.
var ErrNotFound = fmt.Errorf("localstore: not found")
