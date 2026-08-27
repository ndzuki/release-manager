package operator

import (
	"errors"
	"fmt"
	"time"

	"connectrpc.com/connect"

	"github.com/ndzuki/release-manager/internal/store"
)

// errCertificateExpiryUnavailable distinguishes a missing certificate expiry
// (an inconsistent operator record) from a plain out-of-window renewal, so the
// caller can map it to certificate_invalid instead of renew_too_early.
var errCertificateExpiryUnavailable = errors.New("certificate expiry is unavailable")

func renewAllowed(now time.Time, operator *store.Operator, certTTL time.Duration, renewBeforeRatio float64) error {
	if operator == nil || operator.CertificateExpiresAt == nil {
		return errCertificateExpiryUnavailable
	}
	window := time.Duration(float64(certTTL) * renewBeforeRatio)
	if operator.CertificateExpiresAt.Sub(now.UTC()) > window {
		return fmt.Errorf("certificate is outside the renewal window")
	}
	return nil
}

func validateRenewIdentity(identity certificateIdentity, operator *store.Operator, requestedOperatorID string) error {
	if operator == nil || requestedOperatorID == "" || identity.Serial == "" {
		return fmt.Errorf("certificate identity is required")
	}
	if operator.ID != requestedOperatorID || operator.Name != identity.OperatorName ||
		operator.CustomerID != identity.CustomerID || operator.ClusterID != identity.ClusterID {
		return fmt.Errorf("certificate identity does not match operator")
	}
	return nil
}

func validateCertificateIdentity(operator *store.Operator, identity certificateIdentity) error {
	if operator == nil || identity.Serial == "" {
		return operatorError(connect.CodeUnauthenticated, reasonCertificateInvalid, "client certificate identity is invalid")
	}
	switch operator.Status {
	case store.OperatorSuperseded:
		return operatorError(connect.CodePermissionDenied, reasonOperatorSuperseded, "operator is superseded")
	case store.OperatorRevoked:
		return operatorError(connect.CodePermissionDenied, reasonOperatorRevoked, "operator is revoked")
	}
	if identity.Serial != operator.CertSerial {
		return operatorError(connect.CodePermissionDenied, reasonCertReplaced, "client certificate was replaced")
	}
	return nil
}
