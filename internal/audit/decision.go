package audit

import (
	"context"
	"errors"
	"time"

	"connectrpc.com/connect"

	authv1 "github.com/ndzuki/release-manager/api/gen/auth/v1"
	authv1connect "github.com/ndzuki/release-manager/api/gen/auth/v1/authv1connect"
)

// decisionDeadline bounds one authorization decision call. The audit surface is
// interactive, so a slow release-auth must fail the request rather than stall it
// (ADR-021: fail closed, never fall back to a local role guess).
const decisionDeadline = 200 * time.Millisecond

// AccessDecision is the scope release-auth grants for one (object, action) pair.
type AccessDecision struct {
	Allowed bool
	Reason  string
	// OrganizationID is the effective scope: the requested organization, or the
	// caller's own organization when the request left it empty.
	OrganizationID string
	// AllowCrossOrganization is true only for platform_admin.
	AllowCrossOrganization bool
	// MaxWindowDays is the policy-owned query window ceiling.
	MaxWindowDays int32
	PolicyVersion uint64
}

// DecisionClient asks release-auth (the single authorization authority) whether
// the caller holding authorization may act with (object, action) in
// organizationID.
type DecisionClient interface {
	Authorize(ctx context.Context, authorization, organizationID, object, action string) (AccessDecision, error)
}

// ConnectDecisionClient calls auth.v1.AuthorizationService/AuthorizeAccess with
// the caller's own bearer token, so release-auth proves the identity instead of
// trusting this service (ADR-021).
type ConnectDecisionClient struct {
	client  authv1connect.AuthorizationServiceClient
	timeout time.Duration
}

// NewConnectDecisionClient wires the decision client to a release-auth endpoint.
func NewConnectDecisionClient(client authv1connect.AuthorizationServiceClient) *ConnectDecisionClient {
	return &ConnectDecisionClient{client: client, timeout: decisionDeadline}
}

// Authorize implements DecisionClient.
func (c *ConnectDecisionClient) Authorize(
	ctx context.Context,
	authorization, organizationID, object, action string,
) (AccessDecision, error) {
	if c == nil || c.client == nil {
		return AccessDecision{}, errors.New("authorization decision client is not configured")
	}
	callCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	request := connect.NewRequest(&authv1.AuthorizeAccessRequest{
		OrganizationId: organizationID, Object: object, Action: action,
	})
	if authorization != "" {
		request.Header().Set("Authorization", authorization)
	}
	response, err := c.client.AuthorizeAccess(callCtx, request)
	if err != nil {
		return AccessDecision{}, err
	}
	message := response.Msg
	return AccessDecision{
		Allowed:                message.GetAllowed(),
		Reason:                 message.GetReason(),
		OrganizationID:         message.GetOrganizationId(),
		AllowCrossOrganization: message.GetAllowCrossOrganization(),
		MaxWindowDays:          message.GetMaxWindowDays(),
		PolicyVersion:          message.GetPolicyVersion(),
	}, nil
}
