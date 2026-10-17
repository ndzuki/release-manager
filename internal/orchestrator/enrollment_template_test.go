package orchestrator

import (
	"context"
	"os"
	"regexp"
	"testing"

	"connectrpc.com/connect"
	orchestratorv1 "github.com/ndzuki/release-manager/api/gen/orchestrator/v1"
	"github.com/ndzuki/release-manager/internal/authctx"
	"github.com/ndzuki/release-manager/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// operatorFlagPattern extracts the flags cmd/operator registers with the flag
// package. The install command and the binary are two surfaces of the same
// contract and nothing else pins them: the template advertised --operator-addr,
// --customer-id, --cluster-id and --enrollment-token, which the binary never
// defined, so copying it exited with "flag provided but not defined" (TASK-346).
var operatorFlagPattern = regexp.MustCompile(`flag\.(?:String|Bool|Duration|Int|Float64)\("([^"]+)"`)

// templateFlagPattern extracts every long flag named in the rendered command.
var templateFlagPattern = regexp.MustCompile(`(?:^|\s)--([a-z][a-z0-9-]*)`)

func TestInstallCommandTemplateNamesOnlyRealOperatorFlags(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile("../../cmd/operator/main.go")
	require.NoError(t, err, "the operator binary must stay readable from this test")

	defined := map[string]bool{}
	for _, match := range operatorFlagPattern.FindAllStringSubmatch(string(raw), -1) {
		defined[match[1]] = true
	}
	require.GreaterOrEqual(t, len(defined), 7,
		"expected the flag set to be extracted from cmd/operator/main.go; a registration refactor must update this test")

	command := renderInstallTemplate("customer-346", "cluster-346")
	flags := templateFlagPattern.FindAllStringSubmatch(command, -1)
	require.NotEmpty(t, flags, "the install command must call release-operator with flags")
	for _, match := range flags {
		assert.True(t, defined[match[1]],
			"the install command names --%s, which cmd/operator does not define", match[1])
	}
}

func TestInstallCommandTemplateCarriesIdentityAndTokenWhereTheBinaryReadsThem(t *testing.T) {
	t.Parallel()

	command := renderInstallTemplate("customer-346", "cluster-346")
	t.Logf("rendered install command:\n%s", command)

	// Identity belongs in the config file the binary reads through --config;
	// neither id is a flag.
	assert.Contains(t, command, "agent:")
	assert.Contains(t, command, "customer_id: customer-346")
	assert.Contains(t, command, "cluster_id: cluster-346")
	// The one-time token stays an environment reference the console replaces.
	assert.Contains(t, command, "ENROLLMENT_TOKEN")
	assert.Contains(t, command, "${ENROLLMENT_TOKEN}")
	// The gateway is a deployment fact: the template must not guess one, and
	// must never leak the management-plane listener addresses.
	assert.Contains(t, command, "<CONTROL_PLANE_OPERATOR_GATEWAY>")
	assert.NotContains(t, command, "http://orchestrator:8083")
	assert.NotContains(t, command, "http://operator:8084")
	// Regression pins: these flags never existed on the binary.
	assert.NotContains(t, command, "--operator-addr")
	assert.NotContains(t, command, "--customer-id")
	assert.NotContains(t, command, "--cluster-id")
	assert.NotContains(t, command, "--enrollment-token")
}

func TestCreateEnrollmentTokenReturnsAnExecutableInstallCommand(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	ctx := context.Background()
	require.NoError(t, st.Organizations().Create(ctx, &store.Organization{ID: "org-346", Name: "Install Command Org"}))
	require.NoError(t, st.Users().Create(ctx, &store.User{ID: "user-346", Username: "user-346", PasswordHash: "unused"}))
	require.NoError(t, st.OrgMembers().Create(ctx, &store.OrganizationMember{
		OrgID: "org-346", UserID: "user-346", Role: store.RoleReleaseAdmin,
	}))
	require.NoError(t, createCustomerViaManagement(ctx, st, &store.Customer{
		ID: "customer-346", Name: "Install Command Customer", Slug: "install-command-customer",
	}))
	require.NoError(t, st.Clusters().Create(ctx, &store.Cluster{
		ID: "cluster-346", Name: "Install Command Cluster", CustomerID: "customer-346",
	}))
	actorCtx := authctx.WithActor(ctx, authctx.Actor{
		UserID: "user-346", OrganizationID: "org-346", Roles: []string{string(store.RoleReleaseAdmin)},
	})

	resp, err := svc.CreateEnrollmentToken(actorCtx, connect.NewRequest(&orchestratorv1.CreateEnrollmentTokenRequest{
		CustomerId: "customer-346", ClusterId: "cluster-346", OperatorName: "operator-346", TtlMinutes: 5,
	}))
	require.NoError(t, err)

	command := resp.Msg.GetInstallCommandTemplate()
	assert.Equal(t, "v2", resp.Msg.GetInstallCommandTemplateVersion())
	assert.Contains(t, command, "customer_id: customer-346")
	assert.Contains(t, command, "cluster_id: cluster-346")
	assert.Contains(t, command, "--config")
	assert.Contains(t, command, "${ENROLLMENT_TOKEN}")
	assert.Contains(t, command, "<CONTROL_PLANE_OPERATOR_GATEWAY>")
	assert.NotContains(t, command, resp.Msg.GetToken(), "the template must not carry the plaintext token")
	// The separately returned endpoint keeps the REQ-053 contract; only the
	// command stops pretending a management-plane address is customer-reachable.
	assert.NotEmpty(t, resp.Msg.GetOperatorEndpoint())
}
