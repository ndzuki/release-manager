package e2e

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"syscall"
	"time"
)

// ErrLockUnavailable reports that another process owns the lock.
var ErrLockUnavailable = errors.New("e2e: lock unavailable")

// ErrEnvironmentLocked is the contract-level alias for lock contention.
var ErrEnvironmentLocked = ErrLockUnavailable

// ErrLockReleased reports an operation on a released lock.
var ErrLockReleased = errors.New("e2e: lock released")

// LockOwner is the durable marker written while a process owns a lock.
type LockOwner struct {
	Owner      string    `json:"owner"`
	PID        int       `json:"pid"`
	AcquiredAt time.Time `json:"acquired_at"`
}

// LockConflictError describes the owner observed when acquisition failed.
type LockConflictError struct {
	Owner LockOwner
}

func (e *LockConflictError) Error() string {
	if e == nil || e.Owner.Owner == "" {
		return ErrLockUnavailable.Error()
	}
	return fmt.Sprintf("%s: owner %q (pid %d)", ErrLockUnavailable, e.Owner.Owner, e.Owner.PID)
}

func (e *LockConflictError) Unwrap() error { return ErrLockUnavailable }

// ProcessLock is an exclusive, non-blocking process lock with an owner marker.
// The zero value is not usable; acquire locks through AcquireProcessLock.
type ProcessLock struct {
	mu       sync.Mutex
	file     *os.File
	path     string
	owner    LockOwner
	released bool
}

// AcquireProcessLock obtains path using LOCK_EX|LOCK_NB and records owner.
func AcquireProcessLock(path, owner string) (*ProcessLock, error) {
	if path == "" {
		return nil, fmt.Errorf("acquire process lock: empty path")
	}
	if owner == "" {
		return nil, fmt.Errorf("acquire process lock: empty owner")
	}

	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open process lock: %w", err)
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, &LockConflictError{Owner: readLockOwner(path)}
		}
		return nil, fmt.Errorf("acquire process lock: %w", err)
	}

	marker := LockOwner{Owner: owner, PID: os.Getpid(), AcquiredAt: time.Now().UTC()}
	if err := writeLockOwner(file, marker); err != nil {
		// The explicit release is best effort: closing the descriptor below
		// drops the lock either way, so a failed unlock is reported alongside
		// the original failure rather than replacing it.
		if unlockErr := syscall.Flock(int(file.Fd()), syscall.LOCK_UN); unlockErr != nil {
			err = errors.Join(err, fmt.Errorf("release process lock: %w", unlockErr))
		}
		_ = file.Close()
		return nil, fmt.Errorf("write process lock owner: %w", err)
	}
	return &ProcessLock{file: file, path: path, owner: marker}, nil
}

// AcquireLock is a short alias for AcquireProcessLock.
func AcquireLock(path, owner string) (*ProcessLock, error) {
	return AcquireProcessLock(path, owner)
}

// Owner returns the immutable owner marker for this lock.
func (l *ProcessLock) Owner() LockOwner {
	if l == nil {
		return LockOwner{}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.owner
}

// Path returns the lock path.
func (l *ProcessLock) Path() string {
	if l == nil {
		return ""
	}
	return l.path
}

// Release clears the marker and releases the process lock. It is idempotent.
func (l *ProcessLock) Release() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.released {
		return nil
	}
	if l.file == nil {
		l.released = true
		return nil
	}

	var releaseErr error
	if err := clearLockOwner(l.file); err != nil {
		releaseErr = errors.Join(releaseErr, fmt.Errorf("clear process lock owner: %w", err))
	}
	if err := syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN); err != nil {
		releaseErr = errors.Join(releaseErr, fmt.Errorf("release process lock: %w", err))
	}
	if err := l.file.Close(); err != nil {
		releaseErr = errors.Join(releaseErr, fmt.Errorf("close process lock: %w", err))
	}
	l.released = true
	l.file = nil
	return releaseErr
}

// Close implements io.Closer and is equivalent to Release.
func (l *ProcessLock) Close() error { return l.Release() }

func writeLockOwner(file *os.File, owner LockOwner) error {
	data, err := json.Marshal(owner)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if _, err := file.Seek(0, 0); err != nil {
		return err
	}
	if err := file.Truncate(0); err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		return err
	}
	return file.Sync()
}

func clearLockOwner(file *os.File) error {
	if _, err := file.Seek(0, 0); err != nil {
		return err
	}
	if err := file.Truncate(0); err != nil {
		return err
	}
	return file.Sync()
}

func readLockOwner(path string) LockOwner {
	data, err := os.ReadFile(path)
	if err != nil {
		return LockOwner{}
	}
	var owner LockOwner
	if err := json.Unmarshal(data, &owner); err != nil {
		return LockOwner{}
	}
	return owner
}
