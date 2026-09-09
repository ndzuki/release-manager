package operator_test

import (
	"testing"
	"time"

	"connectrpc.com/connect"
	operatorv1 "github.com/ndzuki/release-manager/api/gen/operator/v1"
	"github.com/ndzuki/release-manager/internal/operator"
	"github.com/ndzuki/release-manager/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
)

func TestGetActiveOperatorSessionMapsOnlineAndSuspect(t *testing.T) {
	tests := []struct {
		name   string
		status store.SessionStatus
	}{
		{name: "online", status: store.SessionOnline},
		{name: "suspect", status: store.SessionSuspect},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := newTestSvc(t)
			ctx := t.Context()
			operatorID := "op-" + tt.name
			sessionID := "sess-" + tt.name
			customerID := "cust-" + tt.name
			clusterID := "clus-" + tt.name
			require.NoError(t, st.Customers().Create(ctx, &store.Customer{ID: customerID, Name: customerID, Slug: customerID}))
			require.NoError(t, st.Clusters().Create(ctx, &store.Cluster{ID: clusterID, Name: clusterID, CustomerID: customerID}))
			require.NoError(t, st.Operators().Create(ctx, &store.Operator{
				ID:         operatorID,
				CustomerID: customerID,
				ClusterID:  clusterID,
				CertSerial: "cert-" + tt.name,
				Status:     store.OperatorActive,
			}))
			startedAt := time.Date(2030, time.January, 2, 3, 4, 5, 123456789, time.UTC)
			lastHeartbeat := startedAt.Add(2 * time.Minute)
			expiresAt := startedAt.Add(time.Hour)
			require.NoError(t, st.Sessions().Create(ctx, &store.Session{
				ID:                  sessionID,
				OperatorID:          operatorID,
				CustomerID:          customerID,
				ClusterID:           clusterID,
				Status:              tt.status,
				InstanceID:          "instance-" + tt.name,
				Version:             "agent-1.2.3",
				ActiveConfigVersion: "config-7",
				Capabilities:        map[string]string{"inventory": "read", "secret": "sensitive"},
				StartedAt:           startedAt,
				LastHeartbeat:       lastHeartbeat,
				ExpiresAt:           expiresAt,
			}))

			svc, err := operator.NewService(st, nil, operator.WithCA(testCA(t)))
			require.NoError(t, err)
			resp, err := svc.GetActiveOperatorSession(ctx, connect.NewRequest(&operatorv1.GetActiveOperatorSessionRequest{
				OperatorId: operatorID,
			}))
			require.NoError(t, err)
			session := resp.Msg.GetSession()
			require.NotNil(t, session)
			assert.Equal(t, sessionID, session.GetSessionId())
			assert.Equal(t, operatorID, session.GetOperatorId())
			assert.Equal(t, string(tt.status), session.GetStatus())
			assert.Equal(t, "instance-"+tt.name, session.GetInstanceId())
			assert.Equal(t, "agent-1.2.3", session.GetVersion())
			assert.Equal(t, "config-7", session.GetActiveConfigVersion())
			assert.Equal(t, startedAt, session.GetStartedAt().AsTime())
			assert.Equal(t, lastHeartbeat, session.GetLastHeartbeat().AsTime())
			assert.Equal(t, expiresAt, session.GetExpiresAt().AsTime())

			encoded, err := protojson.Marshal(resp.Msg)
			require.NoError(t, err)
			assert.NotContains(t, string(encoded), "capabilities")
			assert.NotContains(t, string(encoded), "sensitive")
		})
	}
}

func TestGetActiveOperatorSessionRejectsBlankAndMissingOperator(t *testing.T) {
	st := newTestSvc(t)
	svc, err := operator.NewService(st, nil, operator.WithCA(testCA(t)))
	require.NoError(t, err)

	tests := []struct {
		name       string
		operatorID string
		wantCode   connect.Code
		wantReason string
	}{
		{name: "blank id", operatorID: "", wantCode: connect.CodeInvalidArgument, wantReason: "operator_id_required"},
		{name: "no active session", operatorID: "unknown-operator", wantCode: connect.CodeNotFound, wantReason: "session_not_found"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := svc.GetActiveOperatorSession(t.Context(), connect.NewRequest(&operatorv1.GetActiveOperatorSessionRequest{
				OperatorId: tt.operatorID,
			}))
			require.Error(t, err)
			assert.Equal(t, tt.wantCode, connect.CodeOf(err))
			var connectErr *connect.Error
			require.ErrorAs(t, err, &connectErr)
			assert.Equal(t, tt.wantReason, connectErr.Meta().Get("X-Reason-Code"))
		})
	}
}

func TestGetActiveOperatorSessionReadsPersistedSessionAfterServiceRecreation(t *testing.T) {
	st := newTestSvc(t)
	ctx := t.Context()
	operatorID := "op-recreated"
	sessionID := "sess-recreated"
	customerID := "cust-recreated"
	clusterID := "clus-recreated"
	require.NoError(t, st.Customers().Create(ctx, &store.Customer{ID: customerID, Name: customerID, Slug: customerID}))
	require.NoError(t, st.Clusters().Create(ctx, &store.Cluster{ID: clusterID, Name: clusterID, CustomerID: customerID}))
	require.NoError(t, st.Operators().Create(ctx, &store.Operator{
		ID:         operatorID,
		CustomerID: customerID,
		ClusterID:  clusterID,
		CertSerial: "cert-recreated",
		Status:     store.OperatorActive,
	}))
	startedAt := time.Date(2030, time.February, 3, 4, 5, 6, 0, time.UTC)
	require.NoError(t, st.Sessions().Create(ctx, &store.Session{
		ID:                  sessionID,
		OperatorID:          operatorID,
		CustomerID:          customerID,
		ClusterID:           clusterID,
		Status:              store.SessionOnline,
		InstanceID:          "instance-recreated",
		Version:             "agent-recreated",
		ActiveConfigVersion: "config-recreated",
		Capabilities:        map[string]string{"inventory": "read"},
		StartedAt:           startedAt,
		LastHeartbeat:       startedAt.Add(time.Minute),
		ExpiresAt:           startedAt.Add(time.Hour),
	}))

	first, err := operator.NewService(st, nil, operator.WithCA(testCA(t)))
	require.NoError(t, err)
	firstResp, err := first.GetActiveOperatorSession(ctx, connect.NewRequest(&operatorv1.GetActiveOperatorSessionRequest{
		OperatorId: operatorID,
	}))
	require.NoError(t, err)
	assert.Equal(t, sessionID, firstResp.Msg.GetSession().GetSessionId())

	recreated, err := operator.NewService(st, nil, operator.WithCA(testCA(t)))
	require.NoError(t, err)
	resp, err := recreated.GetActiveOperatorSession(ctx, connect.NewRequest(&operatorv1.GetActiveOperatorSessionRequest{
		OperatorId: operatorID,
	}))
	require.NoError(t, err)
	assert.Equal(t, sessionID, resp.Msg.GetSession().GetSessionId())
	assert.Equal(t, startedAt, resp.Msg.GetSession().GetStartedAt().AsTime())
	assert.Equal(t, "instance-recreated", resp.Msg.GetSession().GetInstanceId())
}
