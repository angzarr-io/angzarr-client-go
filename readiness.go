// Package angzarr — readiness probes and gRPC Health supervisor.
//
// Go port of `client-rust/main/src/readiness.rs` and
// `client-python/main/angzarr_client/readiness.py` (audit #68, #74, #82,
// #83, #90).
//
// A runner exposes its readiness through `grpc.health.v1.Health`. While
// any probe is failing, the per-service-name status reports NOT_SERVING;
// once every probe is green it flips to SERVING. Aggregation is binary —
// `all up` is SERVING, anything else is NOT_SERVING.
//
// Probes are evaluated on a fixed cadence (default 30s, override via
// ANGZARR_READINESS_PROBE_INTERVAL) with a per-probe timeout (default
// 2s, override via ANGZARR_READINESS_PROBE_TIMEOUT). Bad env values
// silently fall back to defaults (matches Rust's `.parse().ok()`).
//
// Built-in probes:
//   - TransportProbe: one-shot — flipped true once the listener has bound.
//   - OutputDomainProbe: per-sync-target connect check (TCP / UDS).
//   - BusProbe: optional async-bus reachability (audit #74).
//
// Custom probes implement the `Probe` interface.
package angzarr

import (
	"context"
	"log/slog"
	"net"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"
)

// Defaults — see Python `DEFAULT_PROBE_INTERVAL` / Rust `DEFAULT_PROBE_INTERVAL`.
const (
	// DefaultProbeInterval is the default cadence for re-evaluating probes.
	DefaultProbeInterval = 30 * time.Second
	// DefaultProbeTimeout is the default per-probe timeout.
	DefaultProbeTimeout = 2 * time.Second
)

// Environment variable names — same contract as Python/Rust.
const (
	// EnvReadinessProbeInterval overrides DefaultProbeInterval (integer seconds).
	EnvReadinessProbeInterval = "ANGZARR_READINESS_PROBE_INTERVAL"
	// EnvReadinessProbeTimeout overrides DefaultProbeTimeout (integer seconds).
	EnvReadinessProbeTimeout = "ANGZARR_READINESS_PROBE_TIMEOUT"
	// EnvBusEndpoint is the optional async-bus endpoint for BusProbe (audit #74).
	EnvBusEndpoint = "ANGZARR_BUS_ENDPOINT"
)

// Probe is a single readiness check.
//
// `Name` returns a stable identifier for log lines; `Check` returns true
// when the underlying dependency is currently healthy. Implementations
// should respect ctx (the supervisor uses a per-probe timeout context).
type Probe interface {
	Name() string
	Check(ctx context.Context) bool
}

// ProbeConfigFromEnv reads the supervisor cadence + per-probe timeout from
// the env, falling back to defaults on missing / non-numeric values.
//
// Same env-var contract as Rust/Python; bad values silently fall back to
// preserve operator ergonomics on rolling upgrades.
func ProbeConfigFromEnv() (time.Duration, time.Duration) {
	read := func(env string, def time.Duration) time.Duration {
		raw := os.Getenv(env)
		if raw == "" {
			return def
		}
		secs, err := strconv.Atoi(raw)
		if err != nil || secs < 0 {
			return def
		}
		return time.Duration(secs) * time.Second
	}
	return read(EnvReadinessProbeInterval, DefaultProbeInterval),
		read(EnvReadinessProbeTimeout, DefaultProbeTimeout)
}

// TransportSignal is the side of a TransportProbe used by the runner to
// mark "bound and serving". One-way: once marked, never reverts.
type TransportSignal struct {
	bound atomic.Bool
}

// MarkBound flips the signal to bound. Subsequent calls are no-ops.
func (s *TransportSignal) MarkBound() { s.bound.Store(true) }

// IsBound reports whether MarkBound has been called.
func (s *TransportSignal) IsBound() bool { return s.bound.Load() }

// TransportProbe is a one-shot probe — true once the listener binds.
type TransportProbe struct {
	signal *TransportSignal
}

// NewTransportProbe returns a probe + its companion signal. The runner
// calls signal.MarkBound() once the listener is accepting traffic.
func NewTransportProbe() (*TransportProbe, *TransportSignal) {
	signal := &TransportSignal{}
	return &TransportProbe{signal: signal}, signal
}

// Name returns "transport".
func (p *TransportProbe) Name() string { return "transport" }

// Check returns true once the signal is bound.
func (p *TransportProbe) Check(_ context.Context) bool { return p.signal.IsBound() }

// OutputDomainProbe attempts a connect to the downstream coordinator on
// every tick. UDS or TCP is inferred from the endpoint string.
type OutputDomainProbe struct {
	domain   string
	endpoint string
}

// NewOutputDomainProbe builds a probe for a single sync output domain.
// The endpoint can be:
//   - "unix:/path"  — UDS (matches Rust's `strip_prefix("unix:")`)
//   - "/path"       — UDS (matches Rust's `starts_with('/')`)
//   - "host:port"   — TCP
func NewOutputDomainProbe(domain, endpoint string) *OutputDomainProbe {
	return &OutputDomainProbe{domain: domain, endpoint: endpoint}
}

// OutputDomainProbeForDomain resolves the coordinator endpoint via
// ResolveCHEndpointE and builds a probe. Mirrors Rust's
// `OutputDomainProbe::for_domain`.
//
// Bad env config returns an error — readiness probes are configured once
// at startup; a typo should surface before serve(), not silently break
// at the first tick.
func OutputDomainProbeForDomain(domain string) (*OutputDomainProbe, error) {
	endpoint, err := ResolveCHEndpointE(domain, "")
	if err != nil {
		return nil, err
	}
	return NewOutputDomainProbe(domain, endpoint), nil
}

// Name returns the configured domain.
func (p *OutputDomainProbe) Name() string { return p.domain }

// Check attempts a single connect with the supervisor's per-probe timeout.
func (p *OutputDomainProbe) Check(ctx context.Context) bool {
	network, address := splitEndpoint(p.endpoint)
	var d net.Dialer
	conn, err := d.DialContext(ctx, network, address)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// BusProbe is an async-bus reachability check (audit #74).
//
// Connection-only — confirms the broker is reachable, not that publishes
// succeed end-to-end. Built only when ANGZARR_BUS_ENDPOINT is set.
type BusProbe struct {
	endpoint string
}

// NewBusProbe builds a bus probe from an explicit endpoint.
func NewBusProbe(endpoint string) *BusProbe {
	return &BusProbe{endpoint: endpoint}
}

// BusProbeFromEnv reads ANGZARR_BUS_ENDPOINT and builds a BusProbe.
// Returns nil if the env var is unset or blank.
func BusProbeFromEnv() *BusProbe {
	raw := strings.TrimSpace(os.Getenv(EnvBusEndpoint))
	if raw == "" {
		return nil
	}
	return NewBusProbe(raw)
}

// Name returns "bus".
func (p *BusProbe) Name() string { return "bus" }

// Check attempts a single connect with the supervisor's per-probe timeout.
func (p *BusProbe) Check(ctx context.Context) bool {
	network, address := splitEndpoint(p.endpoint)
	var d net.Dialer
	conn, err := d.DialContext(ctx, network, address)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// splitEndpoint classifies a resolved endpoint string into a (network,
// address) tuple for `net.Dialer`. Mirrors Python's `_parse_endpoint` and
// Rust's `Endpoint` discrimination.
func splitEndpoint(raw string) (network, address string) {
	if strings.HasPrefix(raw, "unix:") {
		return "unix", strings.TrimPrefix(raw, "unix:")
	}
	if strings.HasPrefix(raw, "/") {
		return "unix", raw
	}
	return "tcp", raw
}

// NewHealthServer builds the stock grpc-go health server, pre-seeded to
// NOT_SERVING for the default name ("") and every supplied service name.
//
// grpc.health.v1 has a canonical implementation
// (google.golang.org/grpc/health) that correctly streams Watch (required
// by grpc_health_probe -watch and Envoy) and returns the
// protocol-mandated NotFound for unknown service names. Do not hand-roll
// a Health service; ask this one.
//
// Audit #68: K8s readiness probes call `Check("")`. Before any
// supervisor tick the status must be NOT_SERVING — the stock server
// defaults "" to SERVING at construction, so pre-seeding here is
// load-bearing.
func NewHealthServer(serviceNames ...string) *health.Server {
	srv := health.NewServer()
	srv.SetServingStatus("", grpc_health_v1.HealthCheckResponse_NOT_SERVING)
	for _, name := range serviceNames {
		srv.SetServingStatus(name, grpc_health_v1.HealthCheckResponse_NOT_SERVING)
	}
	return srv
}

// HealthStatusPublisher is the supervisor's view of a health server.
// Satisfied by *health.Server. On shutdown the runner calls
// health.Server.Shutdown(), after which SetServingStatus calls are
// ignored — closing the supervisor-vs-drain publish race by construction.
type HealthStatusPublisher interface {
	SetServingStatus(service string, status grpc_health_v1.HealthCheckResponse_ServingStatus)
}

// ReadinessSupervisor polls a list of Probes on a fixed cadence and
// publishes the aggregated status to one or more Health service names.
type ReadinessSupervisor struct {
	probes       []Probe
	health       HealthStatusPublisher
	serviceNames []string
	interval     time.Duration
	timeout      time.Duration
	logger       *slog.Logger
	kick         chan struct{}
}

// NewReadinessSupervisor builds a supervisor. `serviceNames` is the list
// of gRPC Health service names to publish to (typically `""` for the
// default name plus the framework service constant). `interval` is the
// tick cadence; `timeout` is the per-probe timeout.
func NewReadinessSupervisor(
	probes []Probe,
	health HealthStatusPublisher,
	serviceNames []string,
	interval, timeout time.Duration,
) *ReadinessSupervisor {
	return &ReadinessSupervisor{
		probes:       probes,
		health:       health,
		serviceNames: serviceNames,
		interval:     interval,
		timeout:      timeout,
		logger:       slog.Default(),
		kick:         make(chan struct{}, 1),
	}
}

// Kick requests an immediate re-check without waiting out the current
// interval. Called after TransportSignal.MarkBound so readiness flips
// green as soon as the listener accepts, instead of up to one full
// interval later (first-tick vs MarkBound race). Non-blocking; safe for
// concurrent use.
func (s *ReadinessSupervisor) Kick() {
	select {
	case s.kick <- struct{}{}:
	default:
	}
}

// WithLogger returns the supervisor with a custom slog.Logger for the
// per-probe WARN lines. Defaults to slog.Default().
func (s *ReadinessSupervisor) WithLogger(logger *slog.Logger) *ReadinessSupervisor {
	s.logger = logger
	return s
}

// Run loops until ctx is cancelled. Each tick polls every probe, then
// publishes SERVING / NOT_SERVING to every service name.
func (s *ReadinessSupervisor) Run(ctx context.Context) {
	for {
		allOK := s.tick(ctx)
		status := grpc_health_v1.HealthCheckResponse_SERVING
		if !allOK {
			status = grpc_health_v1.HealthCheckResponse_NOT_SERVING
		}
		for _, name := range s.serviceNames {
			s.health.SetServingStatus(name, status)
		}
		select {
		case <-ctx.Done():
			return
		case <-s.kick:
			// Immediate re-check requested (e.g. listener just bound).
		case <-time.After(s.interval):
		}
	}
}

// tick polls every probe with the configured per-probe timeout and
// returns true iff all probes report healthy.
//
// Audit #82: a panicking probe must not unwind the supervisor — wraps
// each Check in a `defer recover()` so a misbehaving probe is treated
// as a failure for its tick. Each cause (panic / timeout /
// probe-returned-false) emits its own WARN line; there is no aggregate
// "failed" log on top — the cause warning is sufficient.
func (s *ReadinessSupervisor) tick(ctx context.Context) bool {
	allOK := true
	for _, probe := range s.probes {
		ok := s.runProbe(ctx, probe)
		if !ok {
			allOK = false
		}
	}
	return allOK
}

// runProbe invokes a single probe with the supervisor's per-probe timeout
// and panic guard. Returns false on timeout / panic / probe-returned-false.
func (s *ReadinessSupervisor) runProbe(ctx context.Context, probe Probe) bool {
	probeCtx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()

	resultCh := make(chan bool, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				// Audit #82 / #90: WARN, not ERROR — alert thresholds
				// match Rust/Python.
				s.logger.Warn(
					"readiness probe panicked",
					slog.String("probe", probe.Name()),
					slog.Any("error", r),
				)
				resultCh <- false
			}
		}()
		resultCh <- probe.Check(probeCtx)
	}()

	select {
	case ok := <-resultCh:
		if !ok {
			// Distinguish "probe-returned-false" from timeout (audit #82).
			// The panic path logs its own cause and short-circuits via
			// the goroutine's recover().
			if probeCtx.Err() == nil {
				s.logger.Warn(
					"readiness probe failed",
					slog.String("probe", probe.Name()),
				)
			}
		}
		return ok
	case <-probeCtx.Done():
		s.logger.Warn(
			"readiness probe timed out",
			slog.String("probe", probe.Name()),
		)
		// No drain needed: resultCh is buffered (size 1) and the probe
		// goroutine sends exactly once, so it can never block.
		return false
	}
}

// SupervisorTick is the package-level entry point used by tests for a
// single-shot supervisor evaluation. It uses a default `slog` logger.
func SupervisorTick(probes []Probe, timeout time.Duration) bool {
	s := &ReadinessSupervisor{
		probes:  probes,
		timeout: timeout,
		logger:  slog.Default(),
	}
	return s.tick(context.Background())
}

// Shutdown drain configuration.
//
// Audit #83 (corrected ordering): on SIGTERM the runner flips health to
// NOT_SERVING via health.Server.Shutdown(), waits the drain duration so
// load balancers observe the flip and stop routing, and only then calls
// GracefulStop. Publishing after Serve returns is too late — the
// listener is already closed.
const (
	// EnvShutdownDrain configures the NOT_SERVING → GracefulStop wait.
	// Accepts a Go duration string (e.g. "5s", "250ms", "0s").
	EnvShutdownDrain = "ANGZARR_SHUTDOWN_DRAIN"
	// DefaultShutdownDrain gives load balancers time to observe the
	// NOT_SERVING flip before the listener closes.
	DefaultShutdownDrain = 5 * time.Second
)

// ResolveDrainDuration reads EnvShutdownDrain. Unset falls back to
// DefaultShutdownDrain; a set-but-unparseable value errors loudly
// (audit #40: operator typos surface at startup, never silently default).
func ResolveDrainDuration() (time.Duration, error) {
	raw := os.Getenv(EnvShutdownDrain)
	if raw == "" {
		return DefaultShutdownDrain, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d < 0 {
		return 0, InvalidArgumentErrorWithCode(
			CodeInvalidDuration,
			"invalid shutdown drain env value",
			map[string]string{ExtraKeyInput: raw, ExtraKeyEnvVar: EnvShutdownDrain},
		)
	}
	return d, nil
}

// LogServerStarted emits the cross-language `server_started` structured
// log line (audit #89).
//
// Same event name + field set as Python `_run_server_async` and Rust
// `server.rs::run_server` so operators can query logs by `service` /
// `name` / `transport` / `address` across language deployments.
func LogServerStarted(logger *slog.Logger, service, name, transport, address string) {
	if logger == nil {
		logger = slog.Default()
	}
	logger.Info(
		"server_started",
		slog.String("service", service),
		slog.String("name", name),
		slog.String("transport", transport),
		slog.String("address", address),
	)
}

// LogServerShutdown emits the cross-language `server_shutdown` log line
// (audit #89). Same shape as Python / Rust.
func LogServerShutdown(logger *slog.Logger, service, name string) {
	if logger == nil {
		logger = slog.Default()
	}
	logger.Info(
		"server_shutdown",
		slog.String("service", service),
		slog.String("name", name),
	)
}
