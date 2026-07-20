package preflight

// StageStatus is the outcome of a single preflight stage.
type StageStatus string

const (
	StagePassed  StageStatus = "passed"
	StageFailed  StageStatus = "failed"
	StageSkipped StageStatus = "skipped"
	StageTimeout StageStatus = "timeout"
)

// StageResult captures the outcome of running one preflight stage.
type StageResult struct {
	Stage  StageName   `json:"stage"`
	Status StageStatus `json:"status"`
	Detail string      `json:"detail,omitempty"`
}

// AggregateResult summarizes the outcome of the full preflight pipeline.
type AggregateResult struct {
	OperationID  string        `json:"operation_id"`
	Overall      StageStatus   `json:"overall"` // passed or failed
	FailedStage  StageName     `json:"failed_stage,omitempty"`
	Stages       []StageResult `json:"stages"`
	ErrorCode    string        `json:"error_code,omitempty"` // stage_timeout, stage_unavailable, etc.
}
