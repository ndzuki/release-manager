package operator

import (
	"errors"

	"connectrpc.com/connect"
)

const (
	reasonInvalidToken             = "invalid_token"
	reasonEnrollTokenExpired       = "enroll_token_expired"
	reasonTokenReused              = "token_reused"
	reasonScopeMismatch            = "scope_mismatch"
	reasonCustomerDisabled         = "customer_disabled"
	reasonClusterDisabled          = "cluster_disabled"
	reasonCSRInvalid               = "csr_invalid"
	reasonCSRSANMismatch           = "csr_san_mismatch"
	reasonDuplicateOperatorName    = "duplicate_operator_name"
	reasonOperatorNameCrossCluster = "operator_name_cross_cluster"
	reasonCertificateInvalid       = "certificate_invalid"
	reasonOperatorSuperseded       = "operator_superseded"
	reasonOperatorRevoked          = "operator_revoked"
	reasonRenewTooEarly            = "renew_too_early"
	reasonCertReplaced             = "cert_replaced"
	reasonInternal                 = "internal"
)

func operatorError(code connect.Code, reason, message string) error {
	err := connect.NewError(code, errors.New(message))
	err.Meta().Set("X-Reason-Code", reason)
	return err
}
