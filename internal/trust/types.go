package trust

import (
	"crypto"
	"crypto/ed25519"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"
	"time"
)

type RootState string

const (
	RootPending RootState = "pending"
	RootActive  RootState = "active"
	RootGrace   RootState = "grace"
	RootRetired RootState = "retired"
	RootRevoked RootState = "revoked"
)

var (
	ErrInvalidRoot              = errors.New("invalid_root")
	ErrOverlapConflict          = errors.New("overlap_conflict")
	ErrLastRootRemovalForbidden = errors.New("last_root_removal_forbidden")
	ErrRevokedRoot              = errors.New("revoked_root")
)

type Root struct {
	ID             string
	Environment    string
	KeyID          string
	PublicKeyPEM   string
	Issuer         string
	SubjectPattern string
	State          RootState
	ValidFrom      time.Time
	GraceUntil     *time.Time
	CreatedAt      time.Time
	UpdatedAt      time.Time
	RevokedAt      *time.Time
}

type Policy struct {
	Environment     string
	Version         int64
	RevocationEpoch int64
	Roots           []*Root
}

func (r *Root) Validate(now time.Time) error {
	if r == nil {
		return fmt.Errorf("%w: root is required", ErrInvalidRoot)
	}
	if strings.TrimSpace(r.Environment) == "" || strings.TrimSpace(r.KeyID) == "" || strings.TrimSpace(r.Issuer) == "" {
		return fmt.Errorf("%w: environment, key_id, and issuer are required", ErrInvalidRoot)
	}
	if r.State == "" {
		r.State = RootPending
	}
	if !r.State.Valid() {
		return fmt.Errorf("%w: unsupported state %q", ErrInvalidRoot, r.State)
	}
	if r.ValidFrom.IsZero() {
		r.ValidFrom = now
	}
	if r.GraceUntil != nil && !r.GraceUntil.After(r.ValidFrom) {
		return fmt.Errorf("%w: grace_until must be after valid_from", ErrInvalidRoot)
	}
	if _, err := ParsePublicKey(r.PublicKeyPEM); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidRoot, err)
	}
	return nil
}

func (s RootState) Valid() bool {
	switch s {
	case RootPending, RootActive, RootGrace, RootRetired, RootRevoked:
		return true
	}
	return false
}

func (r *Root) Accepts(at time.Time) bool {
	if r == nil || at.Before(r.ValidFrom) {
		return false
	}
	switch r.State {
	case RootActive:
		return true
	case RootGrace:
		return r.GraceUntil != nil && at.Before(*r.GraceUntil)
	default:
		return false
	}
}

func (r *Root) Matches(issuer, subject string) bool {
	if r == nil || issuer != r.Issuer {
		return false
	}
	return r.SubjectPattern == "" || strings.HasPrefix(subject, r.SubjectPattern)
}

func ParsePublicKey(publicKeyPEM string) (crypto.PublicKey, error) {
	block, rest := pem.Decode([]byte(publicKeyPEM))
	if block == nil || len(rest) != 0 {
		return nil, errors.New("public_key_pem must contain one PEM block")
	}
	if strings.Contains(block.Type, "PRIVATE") {
		return nil, errors.New("private keys are forbidden")
	}

	if key, err := x509.ParsePKIXPublicKey(block.Bytes); err == nil {
		switch typed := key.(type) {
		case ed25519.PublicKey:
			return typed, nil
		default:
			return nil, fmt.Errorf("unsupported public key type %T", key)
		}
	}
	if key, err := x509.ParsePKCS1PublicKey(block.Bytes); err == nil {
		return nil, fmt.Errorf("unsupported public key type %T", key)
	}
	return nil, errors.New("invalid public key PEM")
}
