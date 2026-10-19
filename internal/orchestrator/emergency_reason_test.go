package orchestrator

import (
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	orchestratorv1 "github.com/ndzuki/release-manager/api/gen/orchestrator/v1"
)

// AC-058-12 / TASK-325 W3 (decision D16=A): Execute requires a justification
// bounded to 5-500 Unicode characters after trimming. Each case asserts both the
// rejection code AND that a rejection has no side effects; the accepted cases
// assert the trimmed value is what the durable column and the display projection
// carry.
func TestExecuteEmergencyChangeReasonValidation(t *testing.T) {
	cases := []struct {
		name     string
		reason   string
		rejected bool
		wantText string
	}{
		{name: "empty", reason: "", rejected: true},
		{name: "whitespace only", reason: " \n\t ", rejected: true},
		{name: "four characters", reason: "abcd", rejected: true},
		{name: "five characters", reason: "abcde", wantText: "abcde"},
		{name: "surrounding whitespace is trimmed", reason: "  incident-42  ", wantText: "incident-42"},
		{name: "five characters after trimming", reason: "\t abcde\n", wantText: "abcde"},
		{name: "500 characters", reason: strings.Repeat("a", 500), wantText: strings.Repeat("a", 500)},
		{name: "501 characters", reason: strings.Repeat("a", 501), rejected: true},
		// Multibyte boundaries pin the character (rune) semantics rather than
		// byte length: four emoji are 16 bytes but only four characters, so a
		// byte-length check would wrongly accept them, and 500 CJK characters
		// are 1500 bytes, so a byte-length check would wrongly reject them.
		{name: "four emoji (4 runes, 16 bytes)", reason: "🚀🚀🚀🚀", rejected: true},
		{name: "five emoji (5 runes, 20 bytes)", reason: "🚀🚀🚀🚀🚀", wantText: "🚀🚀🚀🚀🚀"},
		{name: "four CJK characters (4 runes, 12 bytes)", reason: "中文中文", rejected: true},
		{name: "mixed emoji and ASCII (5 runes, 8 bytes)", reason: "🚀ok!!", wantText: "🚀ok!!"},
		{name: "500 CJK characters (500 runes, 1500 bytes)", reason: strings.Repeat("中", 500), wantText: strings.Repeat("中", 500)},
		{name: "501 CJK characters (501 runes, 1503 bytes)", reason: strings.Repeat("中", 501), rejected: true},
		{name: "NUL", reason: "abc\x00de", rejected: true},
		{name: "U+FFFE", reason: "abc\ufffede", rejected: true},
		{name: "U+FFFF", reason: "abc\uffffde", rejected: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, st, dispatcher := emergencyTestService(t)
			req := emergencyImageRequest("reason-" + strings.ReplaceAll(tc.name, " ", "-"))
			req.Msg.Reason = tc.reason

			resp, err := svc.ExecuteEmergencyChange(emergencyAdminContext(), req)
			if tc.rejected {
				require.Error(t, err)
				assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
				assert.Equal(t, "invalid_reason", connectErrorReason(err))
				assert.Empty(t, dispatcher.commands, "a rejected reason must never reach the operator")
				operations, listErr := st.Operations().List(t.Context(), "def-001")
				require.NoError(t, listErr)
				assert.Empty(t, operations, "a rejected reason must not persist an operation")
				return
			}

			require.NoError(t, err)
			require.NotEmpty(t, resp.Msg.GetOperationId())
			operation, getErr := st.Operations().Get(t.Context(), resp.Msg.GetOperationId())
			require.NoError(t, getErr)
			assert.Equal(t, tc.wantText, operation.Reason, "operations.reason stores the trimmed reason")

			// The detail page reads GetOperation, so the projection is asserted
			// through the same path the console uses rather than the helper alone.
			read, readErr := svc.GetOperation(emergencyAdminContext(), connect.NewRequest(&orchestratorv1.GetOperationRequest{
				OperationId: resp.Msg.GetOperationId(),
			}))
			require.NoError(t, readErr)
			assert.Equal(t, tc.wantText, read.Msg.GetOperation().GetReasonDisplay())
		})
	}
}

// AC-058-48 / TASK-325 W3: reason_display is the safe projection — redact first,
// then truncate — so a secret typed into the free-text reason never reaches the
// wire, and a long reason can never widen the payload.
func TestOperationReasonDisplay(t *testing.T) {
	t.Run("redacts a secret", func(t *testing.T) {
		display := operationReasonDisplay("rotate db password=hunter2 for incident-42")
		assert.Contains(t, display, "****REDACTED****")
		assert.NotContains(t, display, "hunter2")
	})

	t.Run("truncates to 500 runes", func(t *testing.T) {
		display := operationReasonDisplay(strings.Repeat("界", 600))
		assert.Equal(t, 500, len([]rune(display)))
		assert.True(t, strings.HasSuffix(display, "..."))
	})

	t.Run("keeps a short reason intact", func(t *testing.T) {
		assert.Equal(t, "incident-42 mitigate impact", operationReasonDisplay("incident-42 mitigate impact"))
	})

	t.Run("empty stays empty", func(t *testing.T) {
		assert.Empty(t, operationReasonDisplay(""))
	})
}

// The stored reason stays authoritative while the projection is redacted: the
// console must receive the masked form, the row keeps the record.
func TestExecuteEmergencyChangeRedactsReasonOnlyInTheProjection(t *testing.T) {
	svc, st, _ := emergencyTestService(t)
	req := emergencyImageRequest("reason-redaction")
	req.Msg.Reason = "rotate db password=hunter2 for incident-42"

	resp, err := svc.ExecuteEmergencyChange(emergencyAdminContext(), req)
	require.NoError(t, err)

	operation, err := st.Operations().Get(t.Context(), resp.Msg.GetOperationId())
	require.NoError(t, err)
	assert.Contains(t, operation.Reason, "hunter2", "the durable record keeps what the operator typed")

	read, err := svc.GetOperation(emergencyAdminContext(), connect.NewRequest(&orchestratorv1.GetOperationRequest{
		OperationId: resp.Msg.GetOperationId(),
	}))
	require.NoError(t, err)
	display := read.Msg.GetOperation().GetReasonDisplay()
	assert.Contains(t, display, "****REDACTED****")
	assert.NotContains(t, display, "hunter2")
}

// The reason is part of the request fingerprint, so editing it under the same
// idempotency key is a conflict rather than a silent replay (AC-058-17).
func TestExecuteEmergencyChangeReasonIsPartOfTheIntent(t *testing.T) {
	svc, _, dispatcher := emergencyTestService(t)

	first := emergencyImageRequest("reason-intent")
	first.Msg.Reason = "incident-42 first justification"
	_, err := svc.ExecuteEmergencyChange(emergencyAdminContext(), first)
	require.NoError(t, err)

	second := emergencyImageRequest("reason-intent")
	second.Msg.Reason = "incident-42 second justification"
	_, err = svc.ExecuteEmergencyChange(emergencyAdminContext(), second)
	require.Error(t, err)
	assert.Equal(t, connect.CodeAlreadyExists, connect.CodeOf(err))
	assert.Equal(t, "idempotency_conflict", connectErrorReason(err))
	assert.Len(t, dispatcher.commands, 1, "a changed reason must not dispatch a second command")
}
