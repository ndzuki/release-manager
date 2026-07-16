package slicing

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func makeFiles(n int) []string {
	f := make([]string, n)
	for i := range n {
		f[i] = "file.go"
	}
	return f
}

func makeServices(n int) []string {
	s := make([]string, n)
	for i := range n {
		s[i] = "svc"
	}
	return s
}

func makeStateMachines(n int) []string {
	m := make([]string, n)
	for i := range n {
		m[i] = "fsm"
	}
	return m
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		req     *Requirement
		wantErr bool
		wantCode ValidationErrorCode
		wantReasons int // expected number of reasons
	}{
		{
			name: "valid atomic requirement",
			req: &Requirement{
				Files:         []string{"a.go", "b.go"},
				Services:      []string{"orchestrator"},
				StateMachines: []string{"release-lifecycle"},
			},
			wantErr: false,
		},
		{
			name: "empty requirement",
			req:  &Requirement{},
			wantErr: false,
		},
		{
			name: "boundary: exactly 8 files",
			req: &Requirement{
				Files:         makeFiles(MaxFiles),
				Services:      []string{"svc"},
				StateMachines: []string{"fsm"},
			},
			wantErr: false,
		},
		{
			name: "boundary: 9 files triggers error",
			req: &Requirement{
				Files:         makeFiles(MaxFiles + 1),
				Services:      []string{"svc"},
				StateMachines: []string{"fsm"},
			},
			wantErr:  true,
			wantCode: ErrRequirementTooLarge,
			wantReasons: 1,
		},
		{
			name: "boundary: exactly 2 services",
			req: &Requirement{
				Files:         []string{"a.go"},
				Services:      makeServices(MaxServices),
				StateMachines: []string{"fsm"},
			},
			wantErr: false,
		},
		{
			name: "boundary: 3 services triggers error",
			req: &Requirement{
				Files:         []string{"a.go"},
				Services:      makeServices(MaxServices + 1),
				StateMachines: []string{"fsm"},
			},
			wantErr:  true,
			wantCode: ErrRequirementTooLarge,
			wantReasons: 1,
		},
		{
			name: "boundary: exactly 1 state machine",
			req: &Requirement{
				Files:         []string{"a.go"},
				Services:      []string{"svc"},
				StateMachines: makeStateMachines(MaxStateMachines),
			},
			wantErr: false,
		},
		{
			name: "boundary: 2 state machines triggers error",
			req: &Requirement{
				Files:         []string{"a.go"},
				Services:      []string{"svc"},
				StateMachines: makeStateMachines(MaxStateMachines + 1),
			},
			wantErr:  true,
			wantCode: ErrRequirementTooLarge,
			wantReasons: 1,
		},
		{
			name: "multiple violations accumulated",
			req: &Requirement{
				Files:         makeFiles(10),
				Services:      makeServices(5),
				StateMachines: makeStateMachines(3),
			},
			wantErr:  true,
			wantCode: ErrRequirementTooLarge,
			wantReasons: 3,
		},
		{
			name: "files violation only",
			req: &Requirement{
				Files:         makeFiles(12),
				Services:      []string{"svc"},
				StateMachines: []string{"fsm"},
			},
			wantErr:  true,
			wantCode: ErrRequirementTooLarge,
			wantReasons: 1,
		},
		{
			name: "services violation only",
			req: &Requirement{
				Files:         []string{"a.go"},
				Services:      makeServices(4),
				StateMachines: []string{"fsm"},
			},
			wantErr:  true,
			wantCode: ErrRequirementTooLarge,
			wantReasons: 1,
		},
		{
			name: "state machine violation only",
			req: &Requirement{
				Files:         []string{"a.go"},
				Services:      []string{"svc"},
				StateMachines: makeStateMachines(2),
			},
			wantErr:  true,
			wantCode: ErrRequirementTooLarge,
			wantReasons: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ve := Validate(tt.req)
			if !tt.wantErr {
				assert.Nil(t, ve)
				return
			}
			require.NotNil(t, ve)
			assert.Equal(t, tt.wantCode, ve.Code)
			assert.Len(t, ve.Reasons, tt.wantReasons)
			assert.Contains(t, ve.Message, "requirement must be split")
			for _, r := range ve.Reasons {
				assert.NotEmpty(t, r)
			}
		})
	}
}

func TestValidateDeferred(t *testing.T) {
	tests := []struct {
		name      string
		deferred  []string
		wantErr   bool
		wantCode  ValidationErrorCode
	}{
		{
			name:     "empty deferred list",
			deferred: []string{},
			wantErr:  false,
		},
		{
			name:     "nil deferred list",
			deferred: nil,
			wantErr:  false,
		},
		{
			name:     "safe categories only",
			deferred: []string{"ui", "perf", "logging"},
			wantErr:  false,
		},
		{
			name:     "security deferred",
			deferred: []string{"security"},
			wantErr:  true,
			wantCode: ErrNonDeferrableDeferred,
		},
		{
			name:     "test deferred",
			deferred: []string{"test"},
			wantErr:  true,
			wantCode: ErrNonDeferrableDeferred,
		},
		{
			name:     "migration deferred",
			deferred: []string{"migration"},
			wantErr:  true,
			wantCode: ErrNonDeferrableDeferred,
		},
		{
			name:     "docs deferred",
			deferred: []string{"docs"},
			wantErr:  true,
			wantCode: ErrNonDeferrableDeferred,
		},
		{
			name:     "case insensitive: SECURITY",
			deferred: []string{"SECURITY"},
			wantErr:  true,
			wantCode: ErrNonDeferrableDeferred,
		},
		{
			name:     "case insensitive: Test",
			deferred: []string{"Test"},
			wantErr:  true,
			wantCode: ErrNonDeferrableDeferred,
		},
		{
			name:     "whitespace trimmed",
			deferred: []string{"  security  "},
			wantErr:  true,
			wantCode: ErrNonDeferrableDeferred,
		},
		{
			name:     "multiple non-deferrable",
			deferred: []string{"security", "test", "migration", "docs"},
			wantErr:  true,
			wantCode: ErrNonDeferrableDeferred,
		},
		{
			name:     "mixed safe and non-deferrable",
			deferred: []string{"ui", "security", "perf"},
			wantErr:  true,
			wantCode: ErrNonDeferrableDeferred,
		},
		{
			name:     "only security in mixed",
			deferred: []string{"logging", "security", "observability"},
			wantErr:  true,
			wantCode: ErrNonDeferrableDeferred,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ve := ValidateDeferred(tt.deferred)
			if !tt.wantErr {
				assert.Nil(t, ve)
				return
			}
			require.NotNil(t, ve)
			assert.Equal(t, tt.wantCode, ve.Code)
			assert.NotEmpty(t, ve.Message)
			assert.NotEmpty(t, ve.Reasons)
		})
	}
}

func TestValidationError_Error(t *testing.T) {
	ve := &ValidationError{
		Code:    ErrRequirementTooLarge,
		Message: "requirement must be split",
		Reasons: []string{"too many files"},
	}
	assert.Equal(t, "requirement must be split", ve.Error())
}
