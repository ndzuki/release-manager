// Package slicing defines atomicity rules for requirement-to-task decomposition.
//
// It encodes four admission rules ensuring every requirement is independently
// implementable, testable, releasable, and rollbackable:
//  1. Single major state machine or external contract
//  2. No more than 8 source files
//  3. Cross-service ≤ 2
//  4. Security, test, migration, and docs must not be deferred
package slicing

import (
	"fmt"
	"strings"
)

// Requirement represents a requirement to be validated for atomicity.
type Requirement struct {
	// Files is the list of source files expected to be modified.
	Files []string
	// Services is the list of affected services.
	Services []string
	// StateMachines is the list of major state machines or external contracts
	// touched by this requirement.
	StateMachines []string
}

// MaxFiles is the maximum number of source files an atomic requirement may modify.
const MaxFiles = 8

// MaxServices is the maximum number of services an atomic requirement may touch.
const MaxServices = 2

// MaxStateMachines is the maximum number of major state machines / external
// contracts an atomic requirement may have.
const MaxStateMachines = 1

// NonDeferrableCategories lists categories that must not be deferred to a
// separate cleanup task.
var NonDeferrableCategories = []string{"security", "test", "migration", "docs"}

// ValidationErrorCode enumerates atomicity rule violations.
type ValidationErrorCode string

const (
	// ErrRequirementTooLarge indicates the requirement exceeds size limits
	// (files, services, or state machines) and must be split.
	ErrRequirementTooLarge ValidationErrorCode = "requirement_too_large"
	// ErrNonDeferrableDeferred indicates a non-deferrable category was deferred
	// to a later cleanup task.
	ErrNonDeferrableDeferred ValidationErrorCode = "non_deferrable_deferred"
)

// ValidationError describes which atomicity rules were violated.
type ValidationError struct {
	Code    ValidationErrorCode
	Message string
	Reasons []string
}

// Error implements the error interface.
func (e *ValidationError) Error() string { return e.Message }

// Validate checks a requirement against atomicity rules in order:
//  1. File count ≤ MaxFiles
//  2. Cross-service count ≤ MaxServices
//  3. Single major state machine / external contract (≤ MaxStateMachines)
//
// Returns nil if the requirement is atomic; a *ValidationError otherwise.
func Validate(req *Requirement) *ValidationError {
	var reasons []string

	if len(req.Files) > MaxFiles {
		reasons = append(reasons, fmt.Sprintf(
			"modifies %d files, max %d allowed", len(req.Files), MaxFiles))
	}
	if len(req.Services) > MaxServices {
		reasons = append(reasons, fmt.Sprintf(
			"touches %d services, max %d allowed", len(req.Services), MaxServices))
	}
	if len(req.StateMachines) > MaxStateMachines {
		reasons = append(reasons, fmt.Sprintf(
			"has %d state machines/contracts, max %d allowed", len(req.StateMachines), MaxStateMachines))
	}

	if len(reasons) > 0 {
		return &ValidationError{
			Code:    ErrRequirementTooLarge,
			Message: "requirement must be split: " + strings.Join(reasons, "; "),
			Reasons: reasons,
		}
	}
	return nil
}

// ValidateDeferred checks that no non-deferrable category (security, test,
// migration, docs) is being deferred to a later cleanup task.
//
// Returns nil if all deferred categories are safe; a *ValidationError otherwise.
func ValidateDeferred(deferred []string) *ValidationError {
	deferredSet := make(map[string]bool, len(deferred))
	for _, d := range deferred {
		deferredSet[strings.ToLower(strings.TrimSpace(d))] = true
	}

	var reasons []string
	for _, ndc := range NonDeferrableCategories {
		if deferredSet[ndc] {
			reasons = append(reasons, fmt.Sprintf(
				"%q cannot be deferred to a cleanup task", ndc))
		}
	}

	if len(reasons) > 0 {
		return &ValidationError{
			Code:    ErrNonDeferrableDeferred,
			Message: "non-deferrable categories found: " + strings.Join(reasons, "; "),
			Reasons: reasons,
		}
	}
	return nil
}
