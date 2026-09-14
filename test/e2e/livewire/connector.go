// Package livewire binds the formal Connect/Operator API to the narrow seams
// the E2E stages consume.
//
// It exists so the stage package never has to know about protobuf messages,
// enums, endpoints, or bearer tokens, and the transport layer never has to know
// about stage semantics. Nothing here shells out to kubectl or helm, and no
// adapter reaches into a database: every read and write goes through a
// generated Connect client.
//
// One Connector authenticates once (via e2e.RunnerSession) and then satisfies
// several stage interfaces, so a run cannot end up with two competing
// identities or two different tokens.
package livewire

import (
	"context"
	"strings"
	"time"

	e2e "github.com/ndzuki/release-manager/test/e2e"
	"github.com/ndzuki/release-manager/test/e2e/stages"
)

// defaultPollInterval bounds how often AwaitOperation re-reads an operation.
// It is a poll interval, not a fixed sleep: every wait is still bounded by the
// caller's context deadline.
const defaultPollInterval = 250 * time.Millisecond

// Connector implements the formal-API write/observe seams for the E2E stages.
type Connector struct {
	cfg     *e2e.Config
	clients *e2e.ClientBundle
	session *e2e.RunnerSession
	poll    time.Duration
	// runID scopes every write stage's idempotency key to one run. An E2E run
	// performs a genuinely new write even when its parameters repeat, so a key
	// that names only the parameters turns the second run into an ADR-009 replay
	// of the first: the server returns the first run's terminal operation without
	// applying anything, and the stage then asserts an effect that never happened
	// (real smoke 2026-09-11: the emergency change was reported requested and
	// SUCCEEDED while the workload stayed at its baseline for the whole run).
	runID string
}

// WithRunID scopes this connector's write idempotency keys to one run, so a
// retry inside the run still dedupes while the next run is a new write.
func (c *Connector) WithRunID(runID string) *Connector {
	if c != nil {
		c.runID = strings.TrimSpace(runID)
	}
	return c
}

// writeKey renders the idempotency key for one write stage, scoped to this run
// and qualified by the state the write starts from.
//
// The run scope is what makes two runs distinct logical writes. The state
// qualifier is what makes two writes inside one run distinct when they start
// from different states: an upgrade names its starting revision and a replica
// change its requested count.
func (c *Connector) writeKey(kind, target string, state ...string) string {
	parts := make([]string, 0, len(state)+1)
	if c != nil && c.runID != "" {
		parts = append(parts, c.runID)
	}
	parts = append(parts, state...)
	return e2e.WriteIdempotencyKey(kind, target, parts...)
}

// Compile-time proof that one Connector satisfies every stage seam it is wired
// into. A signature drift breaks the build here rather than at the CLI.
var (
	_ stages.ReleaseObserver = (*Connector)(nil)
	_ stages.OperationWriter = (*Connector)(nil)
	_ stages.EmergencyWriter = (*Connector)(nil)
)

// New constructs a Connector from an already-validated config.
func New(cfg *e2e.Config) (*Connector, error) {
	clients, err := e2e.NewClientBundle(cfg)
	if err != nil {
		return nil, err
	}
	return NewWithClients(cfg, clients)
}

// NewWithClients constructs a Connector over an existing client bundle. Tests
// use it to inject a bundle bound to an in-process Connect server.
func NewWithClients(cfg *e2e.Config, clients *e2e.ClientBundle) (*Connector, error) {
	session, err := e2e.NewRunnerSession(cfg, clients)
	if err != nil {
		return nil, err
	}
	return &Connector{cfg: cfg, clients: clients, session: session, poll: defaultPollInterval}, nil
}

// WithPollInterval overrides the AwaitOperation poll interval.
func (c *Connector) WithPollInterval(interval time.Duration) *Connector {
	if c == nil {
		return nil
	}
	if interval > 0 {
		c.poll = interval
	}
	return c
}

// Login authenticates the runner account once. Adapters call it lazily, so an
// explicit call is only needed when a caller wants the failure up front.
func (c *Connector) Login(ctx context.Context) error {
	if c == nil || c.session == nil {
		return e2e.ErrRunnerLogin
	}
	return c.session.EnsureLogin(ctx)
}

// UserID returns the authenticated runner user id (empty before Login).
func (c *Connector) UserID() string {
	if c == nil {
		return ""
	}
	return c.session.UserID()
}

// Clients returns the shared client bundle. Read-only adapters (the control
// plane observer and the inventory/bundle reader) reuse this bundle instead of
// constructing a second one, so a run cannot end up talking to two different
// endpoint sets.
func (c *Connector) Clients() *e2e.ClientBundle {
	if c == nil {
		return nil
	}
	return c.clients
}

// Session returns the shared runner session. Callers that must authenticate as
// the same principal (for example the control-plane waiter) reuse this session
// rather than constructing a second one, so a run keeps exactly one identity
// and one access token.
func (c *Connector) Session() *e2e.RunnerSession {
	if c == nil {
		return nil
	}
	return c.session
}

// clientsOrFail returns the bundle or a transport error, keeping every adapter
// free of repeated nil plumbing.
func (c *Connector) clientsOrFail() (*e2e.ClientBundle, error) {
	if c == nil || c.clients == nil || c.session == nil {
		return nil, errUnavailable
	}
	return c.clients, nil
}
