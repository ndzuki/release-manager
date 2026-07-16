package localstore

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	bolt "go.etcd.io/bbolt"
)

// boltStore implements Store backed by BoltDB.
type boltStore struct {
	db *bolt.DB
}

// bucket names
var (
	commandsBucket   = []byte("commands")
	byOutboxBucket   = []byte("by_outbox")
	sequenceKey      = []byte("__last_sequence__")
)

// OpenBolt opens (or creates) a BoltDB-backed command store at the given path.
func OpenBolt(path string) (Store, error) {
	db, err := bolt.Open(path, 0600, nil)
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
		return nil
	})
	if err != nil {
		db.Close()
		return nil, err
	}

	return &boltStore{db: db}, nil
}

// Save persists a command entry with fsync.
func (s *boltStore) Save(ctx context.Context, e *CommandEntry) error {
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
func (s *boltStore) Get(ctx context.Context, commandID string) (*CommandEntry, error) {
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
func (s *boltStore) GetByOutboxID(ctx context.Context, outboxID string) (*CommandEntry, error) {
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
func (s *boltStore) UpdateStatus(ctx context.Context, commandID string, status string, resultJSON string) error {
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
func (s *boltStore) ListActive(ctx context.Context) ([]*CommandEntry, error) {
	var entries []*CommandEntry
	err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(commandsBucket)
		c := b.Cursor()
		for k, v := c.First(); k != nil; k, v = c.Next() {
			// Skip the sequence metadata key.
			if string(k) == string(sequenceKey) {
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
func (s *boltStore) LastSequence(ctx context.Context) (int64, error) {
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
func (s *boltStore) Close() error {
	return s.db.Close()
}

// ErrNotFound is returned when a command is not found in the local store.
var ErrNotFound = fmt.Errorf("localstore: not found")
