// Package operator implements the status reporter gRPC client.
package operator

import (
	"context"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/ndzuki/release-manager/internal/config"
	"github.com/ndzuki/release-manager/internal/pkg/retry"
	releasev1 "github.com/ndzuki/release-manager/api/gen/release/v1"
)

// Reporter reports release status back to the notification service.
type Reporter struct {
	endpoint string
	customerID string
	tlsCfg  *config.TLSConfig
	log     logr.Logger
}

// NewReporter creates a new Reporter.
func NewReporter(endpoint, customerID string, tlsCfg *config.TLSConfig, log logr.Logger) *Reporter {
	return &Reporter{
		endpoint:   endpoint,
		customerID: customerID,
		tlsCfg:     tlsCfg,
		log:        log.WithName("reporter"),
	}
}

// ReportStatus reports a release result to the notification service.
func (r *Reporter) ReportStatus(ctx context.Context, customerID string, result ReleaseResult) error {
	r.log.Info("reporting release status",
		"request_id", result.RequestID,
		"status", result.Status,
	)

	// Create gRPC connection
	conn, err := r.dial(ctx)
	if err != nil {
		return fmt.Errorf("dial notification service: %w", err)
	}
	defer conn.Close()

	client := releasev1.NewStatusReportServiceClient(conn)

	req := &releasev1.ReportStatusRequest{
		CustomerId:      customerID,
		RequestId:       result.RequestID,
		ChartName:       result.ChartName,
		ChartVersion:    result.ChartVersion,
		Status:          MapStatusToProto(result.Status),
		ErrorMessage:    result.ErrorMessage,
		DurationSeconds: result.DurationSecs,
		StartedAt:       result.StartedAt.Unix(),
		CompletedAt:     result.CompletedAt.Unix(),
	}

	// Retry with exponential backoff
	retryCfg := retry.Config{
		MaxAttempts: 5,
		InitialWait: 1 * time.Second,
		MaxWait:     30 * time.Second,
		Multiplier:  2.0,
	}

	return retry.DoWithLog(ctx, retryCfg, r.log, "report-status", func() error {
		resp, err := client.ReportStatus(ctx, req)
		if err != nil {
			return err
		}
		if !resp.Acknowledged {
			return fmt.Errorf("status report not acknowledged: %s", resp.Message)
		}
		return nil
	})
}

// dial establishes a gRPC connection to the notification service.
func (r *Reporter) dial(parentCtx context.Context) (*grpc.ClientConn, error) {
	// 添加 deadline 避免 grpc.WithBlock() 无限阻塞
	ctx, cancel := context.WithTimeout(parentCtx, 30*time.Second)
	defer cancel()

	var opts []grpc.DialOption

	if r.tlsCfg != nil && r.tlsCfg.CertFile != "" {
		tlsCfg, err := r.tlsCfg.BuildClientTLSConfig()
		if err != nil {
			return nil, fmt.Errorf("build TLS config: %w", err)
		}
		opts = append(opts, grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg)))
	} else {
		opts = append(opts, grpc.WithTransportCredentials(insecure.NewCredentials()))
	}

	opts = append(opts, grpc.WithBlock(), grpc.WithReturnConnectionError())

	conn, err := grpc.DialContext(ctx, r.endpoint, opts...)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", r.endpoint, err)
	}

	return conn, nil
}

// ReportStatus implements the StatusReporter interface.
func (r *Reporter) Report(ctx context.Context, customerID string, result ReleaseResult) error {
	return r.ReportStatus(ctx, customerID, result)
}

// GenerateRequestID creates a unique request ID.
func GenerateRequestID() string {
	return uuid.New().String()
}
