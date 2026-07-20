package auth

import "fmt"

// AuthorizationError is the common context carried by authorization failures.
type AuthorizationError struct {
	ReasonCode string
	Subject    string
	Domain     string
	Object     string
	Action     string
	CustomerID string
	Cause      error
}

func (e *AuthorizationError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("%s: %v", e.ReasonCode, e.Cause)
	}
	return e.ReasonCode
}

func (e AuthorizationError) AuthorizationReason() string { return e.ReasonCode }

func (e *AuthorizationError) Unwrap() error { return e.Cause }

// PermissionDeniedError indicates that the actor is not allowed to perform an action.
type PermissionDeniedError struct{ AuthorizationError }

// PolicyUnavailableError indicates that the policy snapshot cannot be trusted.
type PolicyUnavailableError struct{ AuthorizationError }

// DomainBindingMissingError indicates that the organization is not bound to a customer.
type DomainBindingMissingError struct{ AuthorizationError }

// InvalidActorContextError indicates that the actor context is incomplete or invalid.
type InvalidActorContextError struct{ AuthorizationError }

func newPermissionDenied(subject, domain, object, action string) error {
	return &PermissionDeniedError{AuthorizationError: AuthorizationError{
		ReasonCode: "permission_denied",
		Subject:    subject,
		Domain:     domain,
		Object:     object,
		Action:     action,
	}}
}

func newPolicyUnavailable(subject, domain, object, action string, cause error) error {
	return &PolicyUnavailableError{AuthorizationError: AuthorizationError{
		ReasonCode: "policy_unavailable",
		Subject:    subject,
		Domain:     domain,
		Object:     object,
		Action:     action,
		Cause:      cause,
	}}
}

func newDomainBindingMissing(domain, customerID string, cause error) error {
	return &DomainBindingMissingError{AuthorizationError: AuthorizationError{
		ReasonCode: "domain_binding_missing",
		Domain:     domain,
		CustomerID: customerID,
		Cause:      cause,
	}}
}

func newInvalidActorContext(subject, domain string, cause error) error {
	return &InvalidActorContextError{AuthorizationError: AuthorizationError{
		ReasonCode: "invalid_actor_context",
		Subject:    subject,
		Domain:     domain,
		Cause:      cause,
	}}
}
