// Package main starts the release-notifier service.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"connectrpc.com/connect"
	notifierv1connect "github.com/ndzuki/release-manager/api/gen/notifier/v1/notifierv1connect"
	"github.com/ndzuki/release-manager/internal/app"
	"github.com/ndzuki/release-manager/internal/auth"
	"github.com/ndzuki/release-manager/internal/config"
	contractsinterceptor "github.com/ndzuki/release-manager/internal/contracts/interceptor"
	"github.com/ndzuki/release-manager/internal/notifier"
	"github.com/ndzuki/release-manager/internal/store"
	postgresstore "github.com/ndzuki/release-manager/internal/store/postgres"
	sqlitestore "github.com/ndzuki/release-manager/internal/store/sqlite"
	"github.com/ndzuki/release-manager/migrations"
)

type notifierSvc struct {
	cfg          config.ServiceConfig
	store        store.Store
	consumer     *notifier.Consumer
	consumerDone chan struct{}
	pingDB       func(context.Context) error
	// resolver is the ADR-020 Vault SecretResolver; nil when disabled.
	resolver *notifier.VaultResolver
	// metrics counts outbound admission decisions (REQ-031/TASK-096).
	metrics *notifier.EgressMetrics
}

func (s *notifierSvc) Name() string { return "release-notifier" }

func (s *notifierSvc) Configure(cfg *config.ServiceConfig) { s.cfg = *cfg }

func (s *notifierSvc) Close() error {
	if s.resolver != nil {
		_ = s.resolver.Close()
	}
	if s.consumerDone != nil {
		<-s.consumerDone
	}
	if s.store == nil {
		return nil
	}
	return s.store.Close()
}

func (s *notifierSvc) Run(ctx context.Context) {
	defer close(s.consumerDone)
	s.consumer.Run(ctx)
}

func (s *notifierSvc) ReadinessChecks() map[string]func() error {
	if s.pingDB == nil {
		return nil
	}
	return map[string]func() error{
		"database": func() error {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			return s.pingDB(ctx)
		},
	}
}

func (s *notifierSvc) Register(mux *http.ServeMux, logger *slog.Logger) error {
	if err := s.openStore(); err != nil {
		return err
	}
	s.consumerDone = make(chan struct{})
	logger.Info("store opened", "driver", s.cfg.Database.Driver)

	svc := notifier.NewService(s.store, logger)
	path, h := newNotifierHandler(svc, logger, notifierServiceTokenHashes())
	mux.Handle(path, h)

	// Outbound admission (REQ-031/TASK-096): deny by default, and a malformed
	// allowlist entry aborts startup instead of silently shrinking the list.
	policy, err := notifier.NewEgressPolicy(s.cfg.Notifier.EgressAllowlist)
	if err != nil {
		return fmt.Errorf("notifier egress allowlist: %w", err)
	}
	logger.Info("notifier egress allowlist configured", "rules", policy.EgressRules())

	s.metrics = &notifier.EgressMetrics{}
	senderOpts := []notifier.WebhookSenderOption{
		notifier.WithEgressPolicy(policy),
		notifier.WithEgressMetrics(s.metrics),
		notifier.WithLogger(logger),
	}
	// ADR-020: the Vault resolver is optional; when enabled, a missing or
	// invalid reference fails startup instead of delivering unauthenticated.
	if s.cfg.Notifier.Vault.Enabled {
		vaultCfg := s.cfg.Notifier.Vault.WithDefaults()
		if err := vaultCfg.Validate(); err != nil {
			return err
		}
		resolver, resolverErr := notifier.NewVaultResolver(notifier.VaultResolverOptions{
			Address:    vaultCfg.Address,
			Namespace:  vaultCfg.Namespace,
			AuthMount:  vaultCfg.AuthMount,
			Role:       vaultCfg.Role,
			TokenPath:  vaultCfg.TokenPath,
			KVMount:    vaultCfg.KVMount,
			SecretPath: vaultCfg.SecretPath,
			SecretKey:  vaultCfg.SecretKey,
		})
		if resolverErr != nil {
			return resolverErr
		}
		if startErr := resolver.Start(context.Background()); startErr != nil {
			return startErr
		}
		s.resolver = resolver
		senderOpts = append(senderOpts, notifier.WithSecretResolution(resolver, vaultCfg.SecretKey))
		logger.Info("vault secret resolver enabled",
			"address", vaultCfg.Address,
			"kv_mount", vaultCfg.KVMount,
			"secret_path", vaultCfg.SecretPath,
		)
	}

	// Unconfigured channels are rejected during delivery.
	sender := notifier.NewWebhookSender(nil, senderOpts...)

	consumerCfg := notifier.DefaultConsumerConfig()
	s.consumer = notifier.NewConsumer(
		s.store.Notifications(),
		sender,
		logger,
		consumerCfg,
	)

	return nil
}

// openStore connects the configured backend (ADR-015: exactly one authority
// per process). The postgres path opens the per-authority release_notifier
// database and runs its golang-migrate migrations; any migration failure
// aborts startup (REQ-070). The notifier only uses Notifications(); the
// release_notifier database has no other tables, so any other accessor would
// fail fast with a SQL error.
func (s *notifierSvc) openStore() error {
	if err := s.cfg.Database.Validate(); err != nil {
		return err
	}
	var err error
	switch s.cfg.Database.Driver {
	case "postgres":
		s.store, err = postgresstore.Open(context.Background(), s.cfg.Database, migrations.ReleaseNotifierFS())
	case "sqlite":
		s.store, err = sqlitestore.Open(s.cfg.Database.DSN)
	}
	if err != nil {
		return err
	}
	switch backend := s.store.(type) {
	case *postgresstore.Store:
		s.pingDB = backend.SQLDB().PingContext
	case *sqlitestore.Store:
		s.pingDB = backend.DB().PingContext
	}
	return nil
}

// newNotifierHandler mounts NotifierService with the inbound credential guard
// (REQ-031/TASK-096). The guard is part of the service, not the ingress, so the
// same-origin console proxy and a direct call are both covered.
func newNotifierHandler(svc notifierv1connect.NotifierServiceHandler, logger *slog.Logger, tokenHashes []string) (string, http.Handler) {
	return notifierv1connect.NewNotifierServiceHandler(
		svc,
		connect.WithInterceptors(
			contractsinterceptor.NewRequestIDInterceptor(logger),
			contractsinterceptor.NewErrorSanitizeInterceptor(logger),
			auth.ServiceTokenInterceptor("release-notifier", tokenHashes, logger,
				notifierv1connect.NotifierServiceSendProcedure,
				notifierv1connect.NotifierServiceGetStatusProcedure,
			),
		),
	)
}

// notifierServiceTokenHashes reads the rotatable notifier credential pair. An
// unset pair yields an empty allowlist, which matches nothing: the surface is
// closed until a token is configured.
func notifierServiceTokenHashes() []string {
	return auth.TokenHashes(
		os.Getenv("DEV_NOTIFIER_SERVICE_TOKEN"),
		os.Getenv("DEV_NOTIFIER_SERVICE_TOKEN_PREVIOUS"),
	)
}

func main() {
	configPath := flag.String("config", "configs/notifier.dev.yaml", "path to config file")
	flag.Parse()
	app.Run(*configPath, &notifierSvc{})
}
