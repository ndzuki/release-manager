package trust

import (
	commonv1 "github.com/ndzuki/release-manager/api/gen/common/v1"
	"github.com/ndzuki/release-manager/internal/store"
)

// DefaultPolicy returns the default trust policy for the given environment.
// Production environments use fail-closed; staging/development use fail-open
// with policy_warning on verification failure.
func DefaultPolicy(env string) store.TrustPolicy {
	if env == "production" {
		return store.TrustPolicy{
			PolicyVersion: "v1",
			FailClosed:    true,
			TrustedIssuers: []string{
				"release-manager-ci",
			},
		}
	}
	return store.TrustPolicy{
		PolicyVersion: "v1",
		FailClosed:    false,
		TrustedIssuers: []string{
			"release-manager-ci",
		},
	}
}

// PolicyFromProto converts a proto TrustPolicy to a domain TrustPolicy.
func PolicyFromProto(p *commonv1.TrustPolicy) store.TrustPolicy {
	if p == nil {
		return store.TrustPolicy{PolicyVersion: "v1"}
	}
	tp := store.TrustPolicy{
		PolicyVersion:  p.PolicyVersion,
		FailClosed:     p.FailClosed,
		TrustedIssuers: make([]string, len(p.TrustedIssuers)),
	}
	copy(tp.TrustedIssuers, p.TrustedIssuers)
	if tp.PolicyVersion == "" {
		tp.PolicyVersion = "v1"
	}
	return tp
}
