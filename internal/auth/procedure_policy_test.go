package auth

import (
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"

	_ "github.com/ndzuki/release-manager/api/gen/audit/v1"
	_ "github.com/ndzuki/release-manager/api/gen/auth/v1"
	_ "github.com/ndzuki/release-manager/api/gen/notifier/v1"
	_ "github.com/ndzuki/release-manager/api/gen/operator/v1"
	_ "github.com/ndzuki/release-manager/api/gen/orchestrator/v1"
	_ "github.com/ndzuki/release-manager/api/gen/trust/v1"
	_ "github.com/ndzuki/release-manager/api/gen/webhook/v1"
)

// declaredProcedures enumerates every Connect procedure in the compiled proto
// registry. Adding an RPC to api/proto changes this list, which is what makes
// the registry gate fire.
func declaredProcedures(t *testing.T) []string {
	t.Helper()
	var procedures []string
	protoregistry.GlobalFiles.RangeFiles(func(fd protoreflect.FileDescriptor) bool {
		services := fd.Services()
		for i := 0; i < services.Len(); i++ {
			service := services.Get(i)
			for j := 0; j < service.Methods().Len(); j++ {
				method := service.Methods().Get(j)
				procedures = append(procedures, "/"+string(service.FullName())+"/"+string(method.Name()))
			}
		}
		return true
	})
	sort.Strings(procedures)
	return procedures
}

// registryProblems is the registry-completeness gate. It is factored out so the
// test can prove the gate itself fires on an unregistered procedure.
func registryProblems(procedures []string) []string {
	var problems []string
	registered := make(map[string]bool, len(procedures))
	for procedure := range procedurePolicies {
		registered[procedure] = true
	}
	for _, procedure := range procedures {
		if !registered[procedure] {
			problems = append(problems, "procedure "+procedure+" is declared in api/proto but has no row in "+
				"procedurePolicies: register it in internal/auth/procedure_policy.go (object/action, or an explicit non-Casbin mode)")
		}
	}
	known := make(map[string]bool, len(procedures))
	for _, procedure := range procedures {
		known[procedure] = true
	}
	for procedure := range procedurePolicies {
		if !known[procedure] {
			problems = append(problems, "procedurePolicies has row "+procedure+" that no longer exists in api/proto: "+
				"remove it and record the contract change")
		}
	}
	sort.Strings(problems)
	return problems
}

// nonWildcardGrants returns the object|action pairs the default role matrix
// grants to at least one role other than platform_admin. platform_admin's
// ("*", "*") wildcard is excluded on purpose: it would make every pair pass and
// hide exactly the defect this gate exists to catch.
func nonWildcardGrants() map[string]bool {
	grants := map[string]bool{}
	for _, role := range []string{"release_admin", "deployer", "viewer"} {
		for _, rule := range roleRules(role, "gate-domain") {
			if len(rule) < 4 || rule[2] == "*" || rule[3] == "*" {
				continue
			}
			grants[rule[2]+"|"+rule[3]] = true
		}
	}
	return grants
}

// TestProcedurePolicyRegistryIsExhaustive fails whenever an RPC is added to the
// contract without an authorization row (TASK-095 AC-1), and when a row outlives
// its procedure.
func TestProcedurePolicyRegistryIsExhaustive(t *testing.T) {
	procedures := declaredProcedures(t)
	require.Len(t, procedures, 104, "the contract procedure count changed; classify the new/removed RPCs")
	for _, problem := range registryProblems(procedures) {
		t.Error(problem)
	}

	t.Run("the gate rejects an unregistered procedure", func(t *testing.T) {
		synthetic := append(append([]string{}, procedures...), "/example.v1.FutureService/NewMethod")
		problems := registryProblems(synthetic)
		require.Len(t, problems, 1)
		assert.Contains(t, problems[0], "/example.v1.FutureService/NewMethod")
	})

	t.Run("the gate rejects a stale row", func(t *testing.T) {
		trimmed := make([]string, 0, len(procedures))
		for _, procedure := range procedures {
			if procedure != "/auth.v1.AuthService/Login" {
				trimmed = append(trimmed, procedure)
			}
		}
		problems := registryProblems(trimmed)
		require.Len(t, problems, 1)
		assert.Contains(t, problems[0], "/auth.v1.AuthService/Login")
	})
}

// TestProcedurePolicyPairsAreGranted is the semantic gate: every Casbin pair the
// registry enforces must be satisfiable by some non-wildcard role, or be
// explicitly marked adminOnly. This is the check that the old prefix table could
// not pass: it proved an action string existed, not that anyone was allowed.
func TestProcedurePolicyPairsAreGranted(t *testing.T) {
	granted := nonWildcardGrants()
	require.NotEmpty(t, granted)

	for procedure, policy := range procedurePolicies {
		if policy.mode != modeCasbin {
			continue
		}
		require.NotEmptyf(t, policy.object, "%s: Casbin row needs an object", procedure)
		require.NotEmptyf(t, policy.action, "%s: Casbin row needs an action", procedure)
		pair := policy.object + "|" + policy.action
		if policy.adminOnly {
			assert.Falsef(t, granted[pair],
				"%s: marked adminOnly but a non-wildcard role already holds %s; drop the flag", procedure, pair)
			continue
		}
		assert.Truef(t, granted[pair],
			"%s: maps to %s/%s, which no non-wildcard role in the default matrix holds — "+
				"the procedure is permanently permission_denied (TASK-095)", procedure, policy.object, policy.action)
	}
}

// TestProcedurePolicyNonCasbinRowsJustifyThemselves requires every procedure
// that is not enforced by Casbin to say why, so "not managed by Casbin" is a
// recorded decision rather than an omission.
func TestProcedurePolicyNonCasbinRowsJustifyThemselves(t *testing.T) {
	for procedure, policy := range procedurePolicies {
		if policy.mode == modeCasbin {
			continue
		}
		assert.NotEmptyf(t, policy.reason, "%s: non-Casbin mode %d needs a reason", procedure, policy.mode)
	}
}

// TestMapProcedureIsExplicit pins the five procedures that the old prefix table
// left with an empty action. Each must now resolve to a pair the matrix grants,
// or to an explicit non-Casbin disposition.
func TestMapProcedureIsExplicit(t *testing.T) {
	tests := []struct {
		procedure string
		object    string
		action    string
	}{
		{"/auth.v1.AuthService/SwitchOrganization", "organization", "write"},
		{"/orchestrator.v1.OrchestratorService/CheckEmergencyConflict", "release", "read"},
		{"/orchestrator.v1.OrchestratorService/TriggerInventorySync", "release", "write"},
		{"/orchestrator.v1.BundleService/RecordArtifactEvent", "bundle", "write"},
	}
	for _, tt := range tests {
		t.Run(tt.procedure, func(t *testing.T) {
			object, action := mapProcedure(tt.procedure)
			assert.Equal(t, tt.object, object)
			assert.Equal(t, tt.action, action)
		})
	}

	t.Run("SetCapabilityGrant is handler-authorized", func(t *testing.T) {
		assert.True(t, usesHandlerAuthorization("/auth.v1.AuthorizationService/SetCapabilityGrant"))
	})
	t.Run("an unknown procedure fails closed", func(t *testing.T) {
		object, action := mapProcedure("/example.v1.FutureService/NewMethod")
		assert.Empty(t, object)
		assert.Empty(t, action)
		_, ok := lookupProcedure("/example.v1.FutureService/NewMethod")
		assert.False(t, ok)
	})
}
