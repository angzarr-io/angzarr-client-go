package angzarr

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/reflection"
)

// DefaultBindHost is the default host portion of a TCP bind address.
//
// HIGH-5.2: cross-language canonical is the IPv6 wildcard "[::]" which
// the kernel binds dual-stack (IPv4 + IPv6) on Linux by default. The
// previous "0.0.0.0" default was IPv4-only and made Go servers
// unreachable on IPv6 interfaces in dual-stack clusters.
const DefaultBindHost = "[::]"

// EnvBindAddress is the cross-language env var that lets operators
// override the bind address (host:port) at deploy time. Spec MED-5.6.
const EnvBindAddress = "ANGZARR_BIND_ADDRESS"

// ResolveBindAddress returns the host:port the server should bind to.
//
// Precedence (cross-language convention, audit #77):
//  1. ANGZARR_BIND_ADDRESS env var (full "host:port")
//  2. DefaultBindHost + ":" + PORT env var
//  3. DefaultBindHost + ":" + defaultPort
//
// HIGH-5.2 + MED-5.6: mirrors Py `server.resolve_bind_address`, Rs
// `resolve_bind_address`, Ja `Server.resolveBindAddress`.
func ResolveBindAddress(defaultPort int) string {
	if v := os.Getenv(EnvBindAddress); v != "" {
		return v
	}
	port := os.Getenv("PORT")
	if port == "" {
		port = fmt.Sprintf("%d", defaultPort)
	}
	return DefaultBindHost + ":" + port
}

// TransportConfig holds the transport configuration for a gRPC server.
type TransportConfig struct {
	Type    string // "tcp" or "uds"
	Address string // "[::]:port" for TCP or "/path/to/socket" for UDS
}

// GetTransportConfig reads transport configuration from environment.
//
// Environment variables:
//   - TRANSPORT_TYPE: "tcp" (default) or "uds"
//   - UDS_BASE_PATH: Base directory for sockets (default: /tmp/angzarr)
//   - SERVICE_NAME: Service type ("business", "saga", "projector")
//   - DOMAIN: Domain name for aggregates
//   - SAGA_NAME: Saga name (used if DOMAIN not set)
//   - PROJECTOR_NAME: Projector name (used if DOMAIN and SAGA_NAME not set)
//   - PORT: TCP port (default: 50052)
func GetTransportConfig() TransportConfig {
	transport := os.Getenv("TRANSPORT_TYPE")
	if transport == "" {
		transport = "tcp"
	}

	if transport == "uds" {
		basePath := os.Getenv("UDS_BASE_PATH")
		if basePath == "" {
			basePath = "/tmp/angzarr"
		}
		serviceName := os.Getenv("SERVICE_NAME")
		if serviceName == "" {
			serviceName = "business"
		}

		// Get qualifier from DOMAIN, SAGA_NAME, or PROJECTOR_NAME
		qualifier := os.Getenv("DOMAIN")
		if qualifier == "" {
			qualifier = os.Getenv("SAGA_NAME")
		}
		if qualifier == "" {
			qualifier = os.Getenv("PROJECTOR_NAME")
		}

		var socketPath string
		if qualifier != "" {
			socketPath = filepath.Join(basePath, fmt.Sprintf("%s-%s.sock", serviceName, qualifier))
		} else {
			socketPath = filepath.Join(basePath, serviceName+".sock")
		}

		// Ensure parent directory exists
		_ = os.MkdirAll(filepath.Dir(socketPath), 0755)

		// Remove stale socket file if exists
		_ = os.Remove(socketPath)

		return TransportConfig{
			Type:    "uds",
			Address: socketPath,
		}
	}

	// HIGH-5.2 / MED-5.6: prefer ANGZARR_BIND_ADDRESS when set; otherwise
	// build "[::]:<port>" (dual-stack IPv6 wildcard) from the PORT env or
	// the legacy default 50052.
	if explicit := os.Getenv(EnvBindAddress); explicit != "" {
		return TransportConfig{Type: "tcp", Address: explicit}
	}
	port := os.Getenv("PORT")
	if port == "" {
		port = "50052"
	}
	return TransportConfig{
		Type:    "tcp",
		Address: DefaultBindHost + ":" + port,
	}
}

// ServerOptions configures the gRPC server.
//
// Probes (optional, audit #68): if non-empty, the runner installs a
// `ReadinessHealthServer` instead of the static `health.NewServer`, and
// the gRPC `Health.Check` endpoint reports NOT_SERVING until every probe
// returns ok. The TransportProbe (one-shot, flipped after listener bind)
// is added automatically — caller-supplied probes cover bus / output
// domains. Logger defaults to `slog.Default()`.
type ServerOptions struct {
	ServiceName      string
	Domain           string
	DefaultPort      string
	EnableReflection bool

	// Probes are caller-supplied readiness probes. A built-in
	// TransportProbe is always added in addition to these. When nil,
	// the static health.NewServer is used (legacy behavior).
	Probes []Probe
	// Logger is the structured logger for server_started / server_shutdown
	// + readiness WARN lines. Defaults to slog.Default() when nil.
	Logger *slog.Logger
}

// ServiceRegistrar registers a service with the gRPC server.
type ServiceRegistrar func(server *grpc.Server)

// CreateServer creates a gRPC server with health checking and optional reflection.
//
// HIGH-5.5 (audit #68): the health server starts at NOT_SERVING for every
// queried name. A supervisor or caller is responsible for flipping it to
// SERVING once readiness probes go green. The previous SERVING-on-start
// behavior caused load balancers to route traffic before the process was
// actually ready.
//
// Bind failures panic. Prefer CreateServerE for a recoverable error.
//
// Returns: (server, listener, cleanup function)
func CreateServer(registrar ServiceRegistrar, opts ServerOptions) (*grpc.Server, net.Listener, func()) {
	server, listener, _, _, _, cleanup, err := CreateServerE(registrar, opts)
	if err != nil {
		panic(fmt.Sprintf("failed to listen: %v", err))
	}
	return server, listener, cleanup
}

// CreateServerE is CreateServer that returns a bind error instead of panicking.
//
// LOW-5.14: callers can recover from bind failures (e.g. port already in
// use during graceful restarts). Mirrors the structured error path used
// by Py/Rs/Ja `run_*_server`.
//
// Returns: server, listener, readiness health server (starts NOT_SERVING),
// supervisor (always non-nil; wraps a TransportProbe plus opts.Probes),
// transport signal (call MarkBound after Serve is ready), cleanup, error.
func CreateServerE(
	registrar ServiceRegistrar,
	opts ServerOptions,
) (
	*grpc.Server,
	net.Listener,
	*health.Server,
	*ReadinessSupervisor,
	*TransportSignal,
	func(),
	error,
) {
	if opts.DefaultPort != "" && os.Getenv("PORT") == "" {
		os.Setenv("PORT", opts.DefaultPort)
	}

	config := GetTransportConfig()

	var listener net.Listener
	var err error

	if config.Type == "uds" {
		listener, err = net.Listen("unix", config.Address)
	} else {
		listener, err = net.Listen("tcp", config.Address)
	}
	if err != nil {
		return nil, nil, nil, nil, nil, func() {}, &ClientError{
			Kind:    ErrConnection,
			Code:    CodeConnectionFailed,
			Message: MessageConnectionFailed,
			Extras:  map[string]string{ExtraKeyEndpoint: config.Address, ExtraKeyCause: err.Error()},
			Cause:   err,
		}
	}

	server := grpc.NewServer()

	// Add the main service
	registrar(server)

	serviceNames := []string{""}
	if opts.ServiceName != "" {
		serviceNames = append(serviceNames, opts.ServiceName)
	}

	// HIGH-5.5 (audit #68): start NOT_SERVING. Stock grpc-go health
	// server, pre-seeded, so K8s readiness reads NOT_SERVING until
	// probes go green (see NewHealthServer).
	healthSrv := NewHealthServer(serviceNames...)
	grpc_health_v1.RegisterHealthServer(server, healthSrv)

	// Always wire a TransportProbe so the supervisor can flip the health
	// status to SERVING once Serve is ready. Caller-supplied probes layer
	// on top.
	transportProbe, transportSignal := NewTransportProbe()
	probes := append([]Probe{transportProbe}, opts.Probes...)
	interval, timeout := ProbeConfigFromEnv()
	supervisor := NewReadinessSupervisor(probes, healthSrv, serviceNames, interval, timeout)
	if opts.Logger != nil {
		supervisor = supervisor.WithLogger(opts.Logger)
	}

	// Add reflection if enabled
	if opts.EnableReflection {
		reflection.Register(server)
	}

	cleanup := func() {
		if config.Type == "uds" {
			_ = os.Remove(config.Address)
		}
	}

	return server, listener, healthSrv, supervisor, transportSignal, cleanup, nil
}

// CreateServerWithReadiness creates a gRPC server whose Health.Check is
// driven by a ReadinessSupervisor.
//
// Mirrors Python's `create_server` + readiness supervisor wiring and
// Rust's `server.rs::run_server` (audit #68, #74, #83). The supervisor
// is returned but NOT started — callers must invoke supervisor.Run(ctx)
// on a goroutine after `server.Serve(listener)` is ready. The
// TransportSignal must be marked bound once the listener is accepting
// traffic so the TransportProbe flips to ok.
//
// Delegates to CreateServerE; bind failures panic to preserve legacy
// semantics. Use CreateServerE for a recoverable error path.
//
// Returns: (server, listener, healthSrv, supervisor, transportSignal, cleanup)
func CreateServerWithReadiness(
	registrar ServiceRegistrar,
	opts ServerOptions,
) (
	*grpc.Server,
	net.Listener,
	*health.Server,
	*ReadinessSupervisor,
	*TransportSignal,
	func(),
) {
	server, listener, healthSrv, supervisor, transportSignal, cleanup, err := CreateServerE(registrar, opts)
	if err != nil {
		panic(fmt.Sprintf("failed to listen: %v", err))
	}
	return server, listener, healthSrv, supervisor, transportSignal, cleanup
}

// RunServer runs a gRPC server until SIGINT or SIGTERM.
//
// Always installs the ReadinessSupervisor + ReadinessHealthServer wiring
// (audit #68, HIGH-5.5) and emits structured `server_started` /
// `server_shutdown` log lines via `opts.Logger` (audit #89).
//
// LOW-5.14: bind failures log + exit rather than panic.
func RunServer(registrar ServiceRegistrar, opts ServerOptions) {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	config := GetTransportConfig()
	runServerWithReadiness(registrar, opts, config, logger)
}

// runServerWithReadiness is the audit-#68/74/83/89 runner path.
func runServerWithReadiness(
	registrar ServiceRegistrar,
	opts ServerOptions,
	config TransportConfig,
	logger *slog.Logger,
) {
	server, listener, healthSrv, supervisor, transportSignal, cleanup, err := CreateServerE(registrar, opts)
	if err != nil {
		logger.Error("bind failed", slog.Any("error", err))
		return
	}
	defer cleanup()

	// Audit #40: a set-but-bad drain value refuses to start, loudly —
	// validated before any goroutine launches.
	drain, err := ResolveDrainDuration()
	if err != nil {
		logger.Error("invalid shutdown drain configuration", slog.Any("error", err))
		return
	}

	// Audit #89: cross-language structured log shape.
	LogServerStarted(logger, opts.ServiceName, opts.Domain, config.Type, config.Address)

	// Supervisor lifetime: run alongside Serve; cancel on shutdown.
	supCtx, supCancel := context.WithCancel(context.Background())
	supDone := make(chan struct{})
	go func() {
		defer close(supDone)
		supervisor.Run(supCtx)
	}()

	// Mark transport bound, then kick the supervisor so readiness flips
	// green immediately instead of waiting out the current interval.
	transportSignal.MarkBound()
	supervisor.Kick()

	// Audit #83 (corrected ordering): NOT_SERVING → drain → GracefulStop.
	// health.Server.Shutdown flips every registered name to NOT_SERVING
	// and ignores all later SetServingStatus calls, so a still-running
	// supervisor cannot flip readiness back during the drain. The drain
	// wait gives load balancers time to observe the flip and stop routing
	// BEFORE GracefulStop closes the listener — publishing after Serve
	// returns (the old order) was too late.
	sigCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		<-sigCtx.Done()
		healthSrv.Shutdown()
		time.Sleep(drain)
		server.GracefulStop()
	}()

	serveErr := server.Serve(listener)

	// Supervisor teardown after Serve returns.
	supCancel()
	select {
	case <-supDone:
	case <-time.After(5 * time.Second):
		logger.Warn("readiness supervisor did not exit cleanly")
	}

	// Audit #89: cross-language structured shutdown log.
	LogServerShutdown(logger, opts.ServiceName, opts.Domain)

	if serveErr != nil {
		logger.Error("server error", slog.Any("error", serveErr))
	}
}

// CleanupSocket removes a UDS socket file.
func CleanupSocket(socketPath string) {
	if socketPath != "" {
		_ = os.Remove(socketPath)
	}
}

// ----------------------------------------------------------------------------
// Per-kind server runners — HIGH-5.4
// ----------------------------------------------------------------------------
//
// Mirrors Py/Rs/Ja `run_command_handler_server` / `run_saga_server` / etc.
// Each runner registers a kind-specific health name and otherwise delegates
// to RunServer. The kind-specific health names are conventionally
// `angzarr.<kind>` matching audit #74 service-name strings.

// Cross-language health-name constants. Audit #74.
const (
	HealthNameCommandHandler = "angzarr.command_handler"
	HealthNameSaga           = "angzarr.saga"
	HealthNameProcessManager = "angzarr.process_manager"
	HealthNameProjector      = "angzarr.projector"
	HealthNameUpcaster       = "angzarr.upcaster"
)

// Per-kind runner functions live in handler.go (typed-router specific
// signatures: RunCommandHandlerServer[S], RunSagaServer, etc.). The
// per-kind health-name constants above are also used by those runners
// so cross-language service names match Py/Rs `HEALTH_NAME_*` constants
// at audit #74 call sites.
