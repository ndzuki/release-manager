package stages

import (
	"context"
	"errors"
	"testing"
)

type controlPlaneFake struct {
	observation ControlPlaneObservation
	err         error
}

func (f controlPlaneFake) ObserveControlPlane(context.Context) (ControlPlaneObservation, error) {
	return f.observation, f.err
}

func healthyControlPlane() ControlPlaneObservation {
	services := make([]ServiceObservation, 0, 6)
	for _, name := range []string{"auth", "notifier", "operator", "orchestrator", "web", "webhook"} {
		services = append(services, ServiceObservation{
			Name:    name,
			Healthy: true,
			Ready:   true,
			Environment: EnvironmentObservation{
				Service:       name,
				Environment:   "development",
				EnvironmentID: "dev-local",
			},
		})
	}
	return ControlPlaneObservation{
		Services: services,
		OperatorSession: OperatorSessionObservation{
			SessionID:  "session-1",
			OperatorID: "operator-1",
			Status:     "ONLINE",
		},
	}
}

func TestControlPlaneStageRun(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		fake controlPlaneFake
		want error
	}{
		{
			name: "pass",
			fake: controlPlaneFake{observation: healthyControlPlane()},
		},
		{
			name: "readiness failure",
			fake: controlPlaneFake{observation: func() ControlPlaneObservation {
				observation := healthyControlPlane()
				observation.Services[0].Ready = false
				return observation
			}()},
			want: ErrEnvironmentUnhealthy,
		},
		{
			name: "environment drift",
			fake: controlPlaneFake{observation: func() ControlPlaneObservation {
				observation := healthyControlPlane()
				observation.Services[1].Environment.EnvironmentID = "other"
				return observation
			}()},
			want: ErrEnvironmentUnhealthy,
		},
		{
			name: "missing snapshot",
			fake: controlPlaneFake{},
			want: ErrSnapshotNotFound,
		},
		{
			name: "observer error is sanitized",
			fake: controlPlaneFake{err: errors.New("transport secret")},
			want: ErrEnvironmentUnhealthy,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			stage := NewControlPlaneStage(test.fake)
			err := stage.Run(context.Background(), nil)
			if test.want == nil {
				if err != nil {
					t.Fatalf("Run() error = %v", err)
				}
				if got := len(stage.Observation().Services); got != 6 {
					t.Fatalf("Observation() services = %d, want 6", got)
				}
				return
			}
			if !errors.Is(err, test.want) {
				t.Fatalf("Run() error = %v, want errors.Is(..., %v)", err, test.want)
			}
			if err.Error() == "transport secret" {
				t.Fatal("Run() leaked transport error")
			}
		})
	}
}

func TestControlPlaneStageExpectedServices(t *testing.T) {
	t.Parallel()

	stage := NewControlPlaneStage(
		controlPlaneFake{observation: healthyControlPlane()},
		"auth",
		"notifier",
		"operator",
		"orchestrator",
		"web",
		"webhook",
	)
	if err := stage.Run(context.Background(), nil); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
}
