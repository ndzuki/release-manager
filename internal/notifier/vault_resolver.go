package notifier

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"

	vault "github.com/hashicorp/vault/api"
)

// ErrSecretNotFound is returned when the referenced secret or key is absent.
// It carries no Vault response body: an error must never become the channel a
// secret escapes through (ADR-007, ADR-020).
var ErrSecretNotFound = errors.New("notifier: webhook secret not found")

// VaultResolverOptions are the ADR-020 references. Every field is a reference or
// a path; no secret value ever appears in configuration.
type VaultResolverOptions struct {
	Address    string
	Namespace  string
	AuthMount  string
	Role       string
	TokenPath  string
	KVMount    string
	SecretPath string
	SecretKey  string
}

// validate fails closed: an enabled resolver with a missing reference must stop
// startup rather than deliver unauthenticated (ADR-020).
func (o VaultResolverOptions) validate() error {
	missing := []struct {
		name  string
		value string
	}{
		{"address", o.Address},
		{"auth_mount", o.AuthMount},
		{"role", o.Role},
		{"token_path", o.TokenPath},
		{"kv_mount", o.KVMount},
		{"secret_path", o.SecretPath},
		{"secret_key", o.SecretKey},
	}
	for _, field := range missing {
		if strings.TrimSpace(field.value) == "" {
			return fmt.Errorf("vault resolver: %s is required", field.name)
		}
	}
	return nil
}

// VaultResolver is the production SecretResolver (ADR-020): it authenticates to
// Vault with the Kubernetes auth method using the notifier's projected
// service-account JWT and reads a KV v2 value with the caller context.
type VaultResolver struct {
	client *vault.Client
	opts   VaultResolverOptions

	mu      sync.Mutex
	auth    *vault.Secret
	watcher *vault.LifetimeWatcher
	stopped chan struct{}
	once    sync.Once
}

// NewVaultResolver builds the resolver. It does not authenticate yet: Start
// performs the Kubernetes login so a failure is reported at service startup.
func NewVaultResolver(opts VaultResolverOptions) (*VaultResolver, error) {
	if err := opts.validate(); err != nil {
		return nil, err
	}
	cfg := vault.DefaultConfig()
	cfg.Address = opts.Address
	client, err := vault.NewClient(cfg)
	if err != nil {
		return nil, fmt.Errorf("vault resolver: create client: %w", err)
	}
	if opts.Namespace != "" {
		client.SetNamespace(opts.Namespace)
	}
	return &VaultResolver{client: client, opts: opts, stopped: make(chan struct{})}, nil
}

// NewVaultResolverForClient wires an existing client (tests, or an embedding
// process that already authenticated); the options still validate.
func NewVaultResolverForClient(client *vault.Client, opts VaultResolverOptions) (*VaultResolver, error) {
	if client == nil {
		return nil, fmt.Errorf("vault resolver: client is required")
	}
	if err := opts.validate(); err != nil {
		return nil, err
	}
	return &VaultResolver{client: client, opts: opts, stopped: make(chan struct{})}, nil
}

// Start authenticates with the Kubernetes auth method and starts token renewal.
func (r *VaultResolver) Start(ctx context.Context) error {
	if err := r.login(ctx); err != nil {
		return err
	}
	r.mu.Lock()
	auth := r.auth
	r.mu.Unlock()
	if auth != nil && auth.Auth != nil && auth.Auth.LeaseDuration > 0 {
		watcher, err := r.client.NewLifetimeWatcher(&vault.LifetimeWatcherInput{Secret: auth})
		if err != nil {
			return fmt.Errorf("vault resolver: start token renewal: %w", err)
		}
		r.mu.Lock()
		r.watcher = watcher
		r.mu.Unlock()
		go r.renew(ctx, watcher)
	}
	return nil
}

// login reads the projected service-account JWT and exchanges it for a Vault
// token. Failures carry no Vault response body.
func (r *VaultResolver) login(ctx context.Context) error {
	jwt, err := os.ReadFile(r.opts.TokenPath)
	if err != nil {
		return fmt.Errorf("vault resolver: read service account token: %w", err)
	}
	token := strings.TrimSpace(string(jwt))
	if token == "" {
		return fmt.Errorf("vault resolver: service account token is empty")
	}
	secret, err := r.client.Logical().WriteWithContext(ctx, "auth/"+r.opts.AuthMount+"/login", map[string]any{
		"role": r.opts.Role,
		"jwt":  token,
	})
	if err != nil {
		return fmt.Errorf("vault resolver: kubernetes login: %w", err)
	}
	if secret == nil || secret.Auth == nil || secret.Auth.ClientToken == "" {
		return fmt.Errorf("vault resolver: kubernetes login returned no client token")
	}
	r.client.SetToken(secret.Auth.ClientToken)
	r.mu.Lock()
	r.auth = secret
	r.mu.Unlock()
	return nil
}

// renew keeps the token fresh; a renewal failure re-authenticates with the
// service-account JWT, so a long-lived notifier does not lose its credential.
func (r *VaultResolver) renew(ctx context.Context, watcher *vault.LifetimeWatcher) {
	watcher.Start()
	defer watcher.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-r.stopped:
			return
		case renewal := <-watcher.RenewCh():
			if renewal != nil && renewal.Secret != nil && renewal.Secret.Auth != nil && renewal.Secret.Auth.ClientToken != "" {
				r.client.SetToken(renewal.Secret.Auth.ClientToken)
			}
		case err := <-watcher.DoneCh():
			if err == nil {
				return
			}
			if loginErr := r.login(context.WithoutCancel(ctx)); loginErr != nil {
				return
			}
			return
		}
	}
}

// Resolve returns the configured KV v2 value. The key argument overrides the
// configured secret key, which is how a caller asks for a different field of the
// same secret; an empty argument uses the configured one.
func (r *VaultResolver) Resolve(ctx context.Context, key string) (string, error) {
	if key = strings.TrimSpace(key); key == "" {
		key = r.opts.SecretKey
	}
	secret, err := r.client.KVv2(r.opts.KVMount).Get(ctx, r.opts.SecretPath)
	if err != nil {
		if errors.Is(err, vault.ErrSecretNotFound) {
			return "", ErrSecretNotFound
		}
		// The wrapped error is a Vault API error (status/URL), never the value.
		return "", fmt.Errorf("vault resolver: read secret %q: %w", r.opts.SecretPath, err)
	}
	if secret == nil || secret.Data == nil {
		return "", ErrSecretNotFound
	}
	value, ok := secret.Data[key].(string)
	if !ok || value == "" {
		return "", fmt.Errorf("%w: key %q absent in %q", ErrSecretNotFound, key, r.opts.SecretPath)
	}
	return value, nil
}

// Close stops renewal. It is safe to call more than once.
func (r *VaultResolver) Close() error {
	r.once.Do(func() { close(r.stopped) })
	r.mu.Lock()
	watcher := r.watcher
	r.mu.Unlock()
	if watcher != nil {
		watcher.Stop()
	}
	return nil
}
