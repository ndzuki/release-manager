package preflight

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestErrorCodeFromStatus(t *testing.T) {
	tests := []struct {
		name   string
		result StageResult
		want   string
	}{
		{name: "preserves stable detail code", result: StageResult{Status: StageFailed, Detail: "render_failed: invalid manifest"}, want: "render_failed"},
		{name: "uses timeout code", result: StageResult{Status: StageTimeout}, want: "stage_timeout"},
		{name: "uses generic failed code", result: StageResult{Status: StageFailed}, want: "preflight_failed"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, errorCodeFromStatus(tt.result))
		})
	}
}
