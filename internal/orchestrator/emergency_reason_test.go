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
// bounded to 1-1000 UTF-8 BYTES after trimming, the window the contract pins
// (Notes/contracts/emergency-execution.md). The unit is bytes, not
// characters/runes, so 400 CJK characters (1200 bytes) must be rejected even
// though they are only 400 runes, while 300 CJK characters (900 bytes) fit.
// Each case asserts both the rejection code/message AND that a rejection has no
// side effects; the accepted cases assert the trimmed value is what the durable
// column carries, and that the display projection matches it unless the 500-rune
// projection budget truncates it.
func TestExecuteEmergencyChangeReasonValidation(t *testing.T) {
	const (
		lengthMessage    = "invalid_argument: reason must contain 1 to 1000 UTF-8 bytes"
		forbiddenMessage = "invalid_argument: reason contains forbidden characters"
	)
	cases := []struct {
		name        string
		reason      string
		rejected    bool
		wantText    string
		wantMessage string
		// wantDisplayTruncated marks an accepted reason longer than the 500-rune
		// display projection budget; the durable column keeps the full text.
		wantDisplayTruncated bool
	}{
		{name: "empty", reason: "", rejected: true, wantMessage: lengthMessage},
		{name: "whitespace only", reason: " \n\t ", rejected: true, wantMessage: lengthMessage},
		// The contract's minimum is one BYTE, not the five characters the old
		// rune window required, so a single character is a valid justification.
		{name: "one byte", reason: "a", wantText: "a"},
		{name: "surrounding whitespace is trimmed", reason: "  incident-42  ", wantText: "incident-42"},
		{name: "one byte after trimming", reason: "\t a\n", wantText: "a"},
		{name: "1000 bytes (ASCII)", reason: strings.Repeat("a", 1000), wantText: strings.Repeat("a", 1000), wantDisplayTruncated: true},
		{name: "1001 bytes (ASCII)", reason: strings.Repeat("a", 1001), rejected: true, wantMessage: lengthMessage},
		// Byte-vs-rune discriminators. 400 CJK is inside the old 500-rune cap but
		// over the 1000-byte bound; 300 CJK is inside both.
		{name: "300 CJK (300 runes, 900 bytes)", reason: strings.Repeat("中", 300), wantText: strings.Repeat("中", 300)},
		{name: "400 CJK (400 runes, 1200 bytes)", reason: strings.Repeat("中", 400), rejected: true, wantMessage: lengthMessage},
		{name: "333 CJK (999 bytes)", reason: strings.Repeat("中", 333), wantText: strings.Repeat("中", 333)},
		{name: "334 CJK (1002 bytes)", reason: strings.Repeat("中", 334), rejected: true, wantMessage: lengthMessage},
		{name: "NUL", reason: "abc\x00de", rejected: true, wantMessage: forbiddenMessage},
		{name: "U+FFFE", reason: "abc\ufffede", rejected: true, wantMessage: forbiddenMessage},
		{name: "U+FFFF", reason: "abc\uffffde", rejected: true, wantMessage: forbiddenMessage},
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
				assert.Equal(t, tc.wantMessage, err.Error())
				// The fixed prose must never echo the caller's text.
				if trimmed := strings.TrimSpace(tc.reason); trimmed != "" {
					assert.NotContains(t, err.Error(), trimmed)
				}
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
			display := read.Msg.GetOperation().GetReasonDisplay()
			if tc.wantDisplayTruncated {
				// Accepted reasons may exceed the 500-rune display budget now that
				// the bound is 1000 bytes; the projection truncates, the row does not.
				assert.Equal(t, 500, len([]rune(display)))
				assert.True(t, strings.HasSuffix(display, "..."))
			} else {
				assert.Equal(t, tc.wantText, display)
			}
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
