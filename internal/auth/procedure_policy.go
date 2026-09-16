package auth

import (
	auditv1connect "github.com/ndzuki/release-manager/api/gen/audit/v1/auditv1connect"
	authv1connect "github.com/ndzuki/release-manager/api/gen/auth/v1/authv1connect"
	notifierv1connect "github.com/ndzuki/release-manager/api/gen/notifier/v1/notifierv1connect"
	operatorv1connect "github.com/ndzuki/release-manager/api/gen/operator/v1/operatorv1connect"
	orchestratorv1connect "github.com/ndzuki/release-manager/api/gen/orchestrator/v1/orchestratorv1connect"
	trustv1connect "github.com/ndzuki/release-manager/api/gen/trust/v1/trustv1connect"
	webhookv1connect "github.com/ndzuki/release-manager/api/gen/webhook/v1/webhookv1connect"
)

// This file is the single authoritative registry of how every Connect
// procedure declared in api/proto is authenticated and authorized. There is no
// string-prefix fallback: a procedure that is not registered here is rejected
// with invalid_actor_context, and TestProcedurePolicyRegistryIsExhaustive fails
// when a procedure is added to the contract without a registry row (TASK-095
// AC-1). TestProcedurePolicyPairsAreGranted fails when a Casbin pair cannot be
// satisfied by any non-wildcard role in the default matrix, which is the defect
// class that made SwitchOrganization permanently unreachable.

// authzMode classifies where a procedure's authorization decision is made.
type authzMode uint8

const (
	// modeCasbin: the auth interceptor resolves the domain and enforces
	// (object, action) through Casbin before the handler runs.
	modeCasbin authzMode = iota + 1
	// modeHandler: the interceptor authenticates and validates the session, but
	// the Connect handler makes the authorization decision.
	modeHandler
	// modePublic: no credential is required. The runtime authority is the
	// per-binary publicMethods set; this row records the design intent.
	modePublic
	// modePrincipalScope: the owning listener (release-api) authenticates a JWT
	// principal and scopes every request to principal.OrgID.
	modePrincipalScope
	// modeMTLS: identity comes from a verified client certificate.
	modeMTLS
	// modeUnintercepted: the owning listener mounts no authentication
	// interceptor. Registered explicitly so an unauthenticated surface is a
	// recorded decision rather than an omission.
	modeUnintercepted
)

// procedurePolicy is one registry row.
type procedurePolicy struct {
	mode   authzMode
	object string
	action string
	// adminOnly marks a Casbin pair that only platform_admin satisfies because
	// the default role matrix grants it to no non-wildcard role. It must not be
	// used to paper over a pair that a non-wildcard role should hold.
	adminOnly bool
	// targetOrg marks a procedure whose request org_id names the organization
	// being operated on rather than the caller's session domain, so the
	// cross-organization request check is deliberately skipped.
	targetOrg bool
	// reason records the evidence or design intent behind the row.
	reason string
}

// procedurePolicies maps every declared Connect procedure to its policy row.
var procedurePolicies = map[string]procedurePolicy{
	// AuditService
	auditv1connect.AuditServiceEmitProcedure:              {mode: modePrincipalScope, reason: "cmd/api authenticates the JWT principal and scopes every audit request to principal.OrgID; release-api has no Casbin policy source"},
	auditv1connect.AuditServiceQueryAuditEventsProcedure:  {mode: modePrincipalScope, reason: "cmd/api authenticates the JWT principal and scopes every audit request to principal.OrgID; release-api has no Casbin policy source"},
	auditv1connect.AuditServiceExportAuditEventsProcedure: {mode: modePrincipalScope, reason: "cmd/api authenticates the JWT principal and scopes every audit request to principal.OrgID; release-api has no Casbin policy source"},
	// AuthService
	authv1connect.AuthServiceGetInitStatusProcedure:      {mode: modePublic, reason: "listed in cmd/auth publicMethods"},
	authv1connect.AuthServiceInitializeProcedure:         {mode: modePublic, reason: "listed in cmd/auth publicMethods"},
	authv1connect.AuthServiceLoginProcedure:              {mode: modePublic, reason: "listed in cmd/auth publicMethods"},
	authv1connect.AuthServiceLogoutProcedure:             {mode: modeHandler, reason: "session revocation: the handler self-verifies the caller's session and the interceptor does not enforce Casbin"},
	authv1connect.AuthServiceRefreshTokenProcedure:       {mode: modePublic, reason: "listed in cmd/auth publicMethods"},
	authv1connect.AuthServiceValidateTokenProcedure:      {mode: modePublic, reason: "listed in cmd/auth publicMethods"},
	authv1connect.AuthServiceSwitchOrganizationProcedure: {mode: modeCasbin, object: "organization", action: "write", targetOrg: true, reason: "switch target: request org_id is the organization being switched into; the handler additionally verifies membership"},
	authv1connect.AuthServiceChangePasswordProcedure:     {mode: modeHandler, reason: "self-service password change: the handler verifies the old password and revokes the caller's sessions; the old auth/write mapping made it platform_admin-only"},
	authv1connect.AuthServiceCreateLocalUserProcedure:    {mode: modeCasbin, object: "auth", action: "write", adminOnly: true, reason: "local user administration is a platform_admin-only namespace: the auth object has no non-wildcard policy row"},
	authv1connect.AuthServiceGetLocalUserProcedure:       {mode: modeCasbin, object: "auth", action: "read", adminOnly: true, reason: "local user administration is a platform_admin-only namespace: the auth object has no non-wildcard policy row"},
	authv1connect.AuthServiceListLocalUsersProcedure:     {mode: modeCasbin, object: "auth", action: "read", adminOnly: true, reason: "local user administration is a platform_admin-only namespace: the auth object has no non-wildcard policy row"},
	// OrganizationService
	authv1connect.OrganizationServiceCreateOrganizationProcedure:  {mode: modeCasbin, object: "organization", action: "write"},
	authv1connect.OrganizationServiceGetOrganizationProcedure:     {mode: modeCasbin, object: "organization", action: "read"},
	authv1connect.OrganizationServiceListOrganizationsProcedure:   {mode: modeCasbin, object: "organization", action: "read"},
	authv1connect.OrganizationServiceUpdateOrganizationProcedure:  {mode: modeCasbin, object: "organization", action: "write"},
	authv1connect.OrganizationServiceDisableOrganizationProcedure: {mode: modeCasbin, object: "organization", action: "write"},
	authv1connect.OrganizationServiceAddMemberProcedure:           {mode: modeCasbin, object: "organization", action: "write"},
	authv1connect.OrganizationServiceRemoveMemberProcedure:        {mode: modeCasbin, object: "organization", action: "write"},
	authv1connect.OrganizationServiceListMembersProcedure:         {mode: modeCasbin, object: "organization", action: "read"},
	authv1connect.OrganizationServiceUpdateMemberRoleProcedure:    {mode: modeCasbin, object: "organization", action: "write"},
	// BindingService
	authv1connect.BindingServiceCreateBindingProcedure: {mode: modeCasbin, object: "binding", action: "write"},
	authv1connect.BindingServiceGetBindingProcedure:    {mode: modeCasbin, object: "binding", action: "read"},
	authv1connect.BindingServiceListBindingsProcedure:  {mode: modeCasbin, object: "binding", action: "read"},
	authv1connect.BindingServiceRevokeBindingProcedure: {mode: modeCasbin, object: "binding", action: "write"},
	// AuthorizationService
	authv1connect.AuthorizationServiceGetAuthorizationSnapshotProcedure: {mode: modeHandler, object: "organization", action: "read", reason: "handler re-derives scope from the actor context and requires membership plus an active binding"},
	authv1connect.AuthorizationServiceSetCapabilityGrantProcedure:       {mode: modeHandler, object: "organization", action: "write", reason: "handler requires platform_admin or release_admin membership of the actor"},
	// ExternalIdentityService
	authv1connect.ExternalIdentityServiceAuthenticateLDAPProcedure:   {mode: modePublic, reason: "pre-authentication IdP entrypoint (REQ-028); implemented but not mounted"},
	authv1connect.ExternalIdentityServiceGetOIDCAuthURLProcedure:     {mode: modePublic, reason: "pre-authentication IdP entrypoint (REQ-028); implemented but not mounted"},
	authv1connect.ExternalIdentityServiceGetDingTalkAuthURLProcedure: {mode: modePublic, reason: "pre-authentication IdP entrypoint (REQ-028); implemented but not mounted"},
	// NotifierService
	notifierv1connect.NotifierServiceSendProcedure:      {mode: modeUnintercepted, reason: "release-notifier mounts the handler without an authentication interceptor"},
	notifierv1connect.NotifierServiceGetStatusProcedure: {mode: modeUnintercepted, reason: "release-notifier mounts the handler without an authentication interceptor"},
	// OperatorService
	operatorv1connect.OperatorServiceEnrollProcedure:                   {mode: modeMTLS, reason: "agent gateway: identity from the verified client certificate; Enroll also carries an enrollment token before a certificate exists"},
	operatorv1connect.OperatorServiceRenewCertificateProcedure:         {mode: modeMTLS, reason: "agent gateway: identity from the verified client certificate; Enroll also carries an enrollment token before a certificate exists"},
	operatorv1connect.OperatorServiceCommandStreamProcedure:            {mode: modeMTLS, reason: "agent gateway: identity from the verified client certificate; Enroll also carries an enrollment token before a certificate exists"},
	operatorv1connect.OperatorServiceGetActiveOperatorSessionProcedure: {mode: modeMTLS, reason: "agent gateway: identity from the verified client certificate; Enroll also carries an enrollment token before a certificate exists"},
	// CleanupService
	orchestratorv1connect.CleanupServiceRunCleanupProcedure:      {mode: modeCasbin, object: "cleanup", action: "write", adminOnly: true, reason: "destructive maintenance: no non-wildcard role holds the cleanup object"},
	orchestratorv1connect.CleanupServiceUnarchiveBundleProcedure: {mode: modeCasbin, object: "cleanup", action: "write", adminOnly: true, reason: "destructive maintenance: no non-wildcard role holds the cleanup object"},
	// BundleService
	orchestratorv1connect.BundleServiceSubmitBundleProcedure:        {mode: modeCasbin, object: "bundle", action: "write", adminOnly: true, reason: "bundle ingress is platform_admin-only on the JWT leg; the service-token leg is scoped separately in cmd/orchestrator"},
	orchestratorv1connect.BundleServiceRecordArtifactEventProcedure: {mode: modeCasbin, object: "bundle", action: "write", adminOnly: true, reason: "bundle ingress is platform_admin-only on the JWT leg; the service-token leg is scoped separately in cmd/orchestrator"},
	orchestratorv1connect.BundleServiceListBundlesProcedure:         {mode: modeCasbin, object: "bundle", action: "read"},
	orchestratorv1connect.BundleServiceGetBundleProcedure:           {mode: modeCasbin, object: "bundle", action: "read"},
	// OrchestratorService
	orchestratorv1connect.OrchestratorServiceCreateOperationProcedure:              {mode: modeCasbin, object: "release", action: "write"},
	orchestratorv1connect.OrchestratorServicePublishReleaseProcedure:               {mode: modeCasbin, object: "release", action: "write"},
	orchestratorv1connect.OrchestratorServiceRollbackReleaseProcedure:              {mode: modeCasbin, object: "release", action: "write"},
	orchestratorv1connect.OrchestratorServiceGetOperationProcedure:                 {mode: modeCasbin, object: "release", action: "read"},
	orchestratorv1connect.OrchestratorServiceWatchOperationProcedure:               {mode: modeCasbin, object: "release", action: "read"},
	orchestratorv1connect.OrchestratorServiceCancelOperationProcedure:              {mode: modeCasbin, object: "release", action: "write"},
	orchestratorv1connect.OrchestratorServiceSubmitValuesRevisionProcedure:         {mode: modeHandler, object: "release", action: "write", reason: "handler enforces the release.values.* capability actions (REQ-058)"},
	orchestratorv1connect.OrchestratorServiceApproveValuesRevisionProcedure:        {mode: modeHandler, object: "release", action: "write", reason: "handler enforces the release.values.* capability actions (REQ-058)"},
	orchestratorv1connect.OrchestratorServiceRejectValuesRevisionProcedure:         {mode: modeHandler, object: "release", action: "write", reason: "handler enforces the release.values.* capability actions (REQ-058)"},
	orchestratorv1connect.OrchestratorServiceCreateValuesRevisionProcedure:         {mode: modeCasbin, object: "release", action: "write"},
	orchestratorv1connect.OrchestratorServiceGetValuesRevisionProcedure:            {mode: modeCasbin, object: "release", action: "read"},
	orchestratorv1connect.OrchestratorServiceListValuesRevisionsProcedure:          {mode: modeCasbin, object: "release", action: "read"},
	orchestratorv1connect.OrchestratorServiceDiscardValuesRevisionProcedure:        {mode: modeCasbin, object: "release", action: "write"},
	orchestratorv1connect.OrchestratorServiceCreatePrepareSessionProcedure:         {mode: modeCasbin, object: "release", action: "write"},
	orchestratorv1connect.OrchestratorServiceGetPrepareSessionProcedure:            {mode: modeCasbin, object: "release", action: "read"},
	orchestratorv1connect.OrchestratorServiceListSecretsProcedure:                  {mode: modeCasbin, object: "release", action: "read"},
	orchestratorv1connect.OrchestratorServiceCreateReleaseDefinitionProcedure:      {mode: modeCasbin, object: "release", action: "write"},
	orchestratorv1connect.OrchestratorServiceGetReleaseDefinitionProcedure:         {mode: modeCasbin, object: "release", action: "read"},
	orchestratorv1connect.OrchestratorServiceListReleaseDefinitionsProcedure:       {mode: modeCasbin, object: "release", action: "read"},
	orchestratorv1connect.OrchestratorServiceUpdateReleaseDefinitionProcedure:      {mode: modeCasbin, object: "release", action: "write"},
	orchestratorv1connect.OrchestratorServiceDisableReleaseDefinitionProcedure:     {mode: modeCasbin, object: "release", action: "write"},
	orchestratorv1connect.OrchestratorServiceCreateCustomerProcedure:               {mode: modeCasbin, object: "release", action: "write"},
	orchestratorv1connect.OrchestratorServiceGetCustomerProcedure:                  {mode: modeCasbin, object: "release", action: "read"},
	orchestratorv1connect.OrchestratorServiceListCustomersProcedure:                {mode: modeCasbin, object: "release", action: "read"},
	orchestratorv1connect.OrchestratorServiceUpdateCustomerProcedure:               {mode: modeCasbin, object: "release", action: "write"},
	orchestratorv1connect.OrchestratorServiceDisableCustomerProcedure:              {mode: modeCasbin, object: "release", action: "write"},
	orchestratorv1connect.OrchestratorServiceListCustomerEventsProcedure:           {mode: modeCasbin, object: "release", action: "read"},
	orchestratorv1connect.OrchestratorServiceCreateClusterProcedure:                {mode: modeCasbin, object: "release", action: "write"},
	orchestratorv1connect.OrchestratorServiceUpdateClusterProcedure:                {mode: modeCasbin, object: "release", action: "write"},
	orchestratorv1connect.OrchestratorServiceGetClusterProcedure:                   {mode: modeCasbin, object: "release", action: "read"},
	orchestratorv1connect.OrchestratorServiceListClustersProcedure:                 {mode: modeCasbin, object: "release", action: "read"},
	orchestratorv1connect.OrchestratorServiceDisableClusterProcedure:               {mode: modeCasbin, object: "release", action: "write"},
	orchestratorv1connect.OrchestratorServiceListOperatorsProcedure:                {mode: modeCasbin, object: "operator", action: "read"},
	orchestratorv1connect.OrchestratorServiceGetOperatorProcedure:                  {mode: modeCasbin, object: "operator", action: "read"},
	orchestratorv1connect.OrchestratorServiceRevokeOperatorProcedure:               {mode: modeCasbin, object: "operator", action: "revoke"},
	orchestratorv1connect.OrchestratorServiceCreateEnrollmentTokenProcedure:        {mode: modeCasbin, object: "operator", action: "enroll"},
	orchestratorv1connect.OrchestratorServiceGetEnrollmentTokenStatusProcedure:     {mode: modeCasbin, object: "operator", action: "enroll"},
	orchestratorv1connect.OrchestratorServiceRevokePendingEnrollmentTokenProcedure: {mode: modeCasbin, object: "operator", action: "enroll"},
	orchestratorv1connect.OrchestratorServiceExecuteEmergencyChangeProcedure:       {mode: modeCasbin, object: "release", action: "write"},
	orchestratorv1connect.OrchestratorServiceListEmergencyTargetsProcedure:         {mode: modeCasbin, object: "release", action: "read"},
	orchestratorv1connect.OrchestratorServiceCheckEmergencyConflictProcedure:       {mode: modeCasbin, object: "release", action: "read", reason: "conflict probe is a release read; the handler also requires an active customer binding"},
	orchestratorv1connect.OrchestratorServiceListCandidateArtifactsProcedure:       {mode: modeCasbin, object: "release", action: "read"},
	orchestratorv1connect.OrchestratorServiceListConvergenceTasksProcedure:         {mode: modeCasbin, object: "release", action: "read"},
	orchestratorv1connect.OrchestratorServiceListStuckLocksProcedure:               {mode: modeCasbin, object: "release", action: "read"},
	orchestratorv1connect.OrchestratorServiceReleaseEmergencyLockProcedure:         {mode: modeCasbin, object: "release", action: "write"},
	orchestratorv1connect.OrchestratorServiceConfigureClusterRouteProcedure:        {mode: modeCasbin, object: "release", action: "write"},
	orchestratorv1connect.OrchestratorServiceGetClusterRoutesProcedure:             {mode: modeCasbin, object: "release", action: "read"},
	orchestratorv1connect.OrchestratorServiceDeleteClusterRouteProcedure:           {mode: modeCasbin, object: "release", action: "write"},
	orchestratorv1connect.OrchestratorServiceListReleasesProcedure:                 {mode: modeCasbin, object: "release", action: "read"},
	orchestratorv1connect.OrchestratorServiceListReleaseInventoryProcedure:         {mode: modeCasbin, object: "release", action: "read"},
	orchestratorv1connect.OrchestratorServiceListOperationsProcedure:               {mode: modeCasbin, object: "release", action: "read"},
	orchestratorv1connect.OrchestratorServiceTriggerInventorySyncProcedure:         {mode: modeCasbin, object: "release", action: "write", reason: "manual sync enqueues an operator command, so it is a release write"},
	orchestratorv1connect.OrchestratorServiceSyncInventoryProcedure:                {mode: modeCasbin, object: "release", action: "write", reason: "also served on the agent gateway leg, where identity comes from the client certificate"},
	// TrustService
	trustv1connect.TrustServiceCreateTrustRootProcedure: {mode: modeCasbin, object: "trust_root", action: "write", adminOnly: true, reason: "trust root rotation is platform_admin-only: no non-wildcard role holds trust_root write"},
	trustv1connect.TrustServiceRotateTrustRootProcedure: {mode: modeCasbin, object: "trust_root", action: "write", adminOnly: true, reason: "trust root rotation is platform_admin-only: no non-wildcard role holds trust_root write"},
	trustv1connect.TrustServiceEndGraceProcedure:        {mode: modeCasbin, object: "trust_root", action: "write", adminOnly: true, reason: "trust root rotation is platform_admin-only: no non-wildcard role holds trust_root write"},
	trustv1connect.TrustServiceRetireTrustRootProcedure: {mode: modeCasbin, object: "trust_root", action: "write", adminOnly: true, reason: "trust root rotation is platform_admin-only: no non-wildcard role holds trust_root write"},
	trustv1connect.TrustServiceRevokeTrustRootProcedure: {mode: modeCasbin, object: "trust_root", action: "write", adminOnly: true, reason: "trust root rotation is platform_admin-only: no non-wildcard role holds trust_root write"},
	trustv1connect.TrustServiceGetTrustPolicyProcedure:  {mode: modeCasbin, object: "trust_root", action: "read"},
	// WebhookService
	webhookv1connect.WebhookServiceSubmitReleaseBundleProcedure: {mode: modeUnintercepted, reason: "release-webhook enforces no credential; the REQ-011 section 562 CI API key is not implemented (registered gap)"},
}

// lookupProcedure returns the registered row for a Connect procedure.
func lookupProcedure(procedure string) (procedurePolicy, bool) {
	policy, ok := procedurePolicies[procedure]
	return policy, ok
}

// mapProcedure returns the Casbin object and action registered for a procedure.
// Unregistered procedures yield empty strings; the interceptor fails closed and
// the registry test makes the omission a gate failure instead of a runtime
// surprise.
func mapProcedure(procedure string) (object, action string) {
	policy, ok := procedurePolicies[procedure]
	if !ok {
		return "", ""
	}
	return policy.object, policy.action
}

// usesHandlerAuthorization reports whether the handler owns the authorization
// decision for a procedure (the interceptor skips the Casbin check).
func usesHandlerAuthorization(procedure string) bool {
	policy, ok := procedurePolicies[procedure]
	return ok && policy.mode == modeHandler
}
