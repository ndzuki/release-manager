// Package manager 实现向客户 operator 批量转发发布通知的 gRPC forwarder。
// release notifications to customer release-operators.
package manager

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/go-logr/logr"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"

	releasev1 "github.com/ndzuki/release-manager/api/gen/release/v1"
	"github.com/ndzuki/release-manager/internal/config"
)

// ForwardResult 保存向单个客户转发通知的结果。
type ForwardResult struct {
	CustomerID   string
	CustomerName string
	Success      bool
	ErrorMessage string
	Duration     time.Duration
}

// Forwarder 将发布通知批量转发给客户 operator。
type Forwarder struct {
	store   Store
	tlsCfg  *config.TLSConfig
	log     logr.Logger
	timeout time.Duration
}

// NewForwarder 创建新的 Forwarder。
func NewForwarder(store Store, tlsCfg *config.TLSConfig, log logr.Logger, timeout time.Duration) *Forwarder {
	return &Forwarder{
		store:   store,
		tlsCfg:  tlsCfg,
		log:     log.WithName("forwarder"),
		timeout: timeout,
	}
}

// ForwardToAll 并发地向所有已启用客户发送发布通知。
func (f *Forwarder) ForwardToAll(ctx context.Context, notification ReleaseNotification) ([]ForwardResult, error) {
	customers, err := f.store.ListCustomers(true) // enabled only
	if err != nil {
		return nil, fmt.Errorf("list enabled customers: %w", err)
	}

	if len(customers) == 0 {
		f.log.Info("no enabled customers to notify")
		return nil, nil
	}

	f.log.Info("forwarding release notification",
		"chart", notification.ChartName,
		"version", notification.ChartVersion,
		"customer_count", len(customers),
	)

	results := make([]ForwardResult, len(customers))
	var mu sync.Mutex
	g, ctx := errgroup.WithContext(ctx)

	for i, cust := range customers {
		idx := i
		customer := cust
		g.Go(func() error {
			result := f.forwardToOne(ctx, customer, notification)
			mu.Lock()
			results[idx] = result
			mu.Unlock()
			// 单个转发失败不影响其他客户的转发
			return nil
		})
	}

	if err := g.Wait(); err != nil {
		return results, fmt.Errorf("forward group: %w", err)
	}

	return results, nil
}

// forwardToOne sends a notification to a single customer's release-operator.
func (f *Forwarder) forwardToOne(ctx context.Context, customer Customer, notification ReleaseNotification) ForwardResult {
	start := time.Now()
	result := ForwardResult{
		CustomerID:   customer.ID,
		CustomerName: customer.Name,
	}

	ctx, cancel := context.WithTimeout(ctx, f.timeout)
	defer cancel()

	// 使用 mTLS 建立 gRPC 连接
	conn, err := f.dial(ctx, customer)
	if err != nil {
		result.ErrorMessage = fmt.Sprintf("dial operator: %v", err)
		f.log.Error(err, "failed to dial operator",
			"customer_id", customer.ID,
			"endpoint", customer.OperatorEndpoint,
		)
		result.Duration = time.Since(start)
		return result
	}
	defer conn.Close()

	client := releasev1.NewReleaseNotificationServiceClient(conn)

	req := &releasev1.NotifyReleaseRequest{
		ChartName:    notification.ChartName,
		ChartVersion: notification.ChartVersion,
		ChartUrl:     notification.ChartURL,
		Images:       notification.Images,
		RequestId:    GenerateRequestID(),
	}

	resp, err := client.NotifyRelease(ctx, req)
	if err != nil {
		result.ErrorMessage = fmt.Sprintf("notify release: %v", err)
		f.log.Error(err, "failed to notify release",
			"customer_id", customer.ID,
			"chart", notification.ChartName,
		)
		result.Duration = time.Since(start)
		return result
	}

	result.Success = resp.Accepted
	if !resp.Accepted {
		result.ErrorMessage = resp.Message
	}

	result.Duration = time.Since(start)
	f.log.Info("release notification forwarded",
		"customer_id", customer.ID,
		"accepted", resp.Accepted,
		"duration", result.Duration,
	)

	return result
}

// dial establishes a gRPC connection to a customer operator.
func (f *Forwarder) dial(ctx context.Context, customer Customer) (*grpc.ClientConn, error) {
	var opts []grpc.DialOption

	if f.tlsCfg != nil && f.tlsCfg.CertFile != "" {
		tlsCfg, err := f.tlsCfg.BuildClientTLSConfig()
		if err != nil {
			return nil, fmt.Errorf("build TLS config: %w", err)
		}
		opts = append(opts, grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg)))
	} else {
		f.log.V(1).Info("using insecure gRPC connection (no TLS config)")
		opts = append(opts, grpc.WithTransportCredentials(insecure.NewCredentials()))
	}

	opts = append(opts, grpc.WithBlock(), grpc.WithReturnConnectionError())

	conn, err := grpc.DialContext(ctx, customer.OperatorEndpoint, opts...)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", customer.OperatorEndpoint, err)
	}

	return conn, nil
}
