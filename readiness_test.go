package angzarr

// Cross-language parity tests for the readiness supervisor (audit #68, #74,
// #82, #83, #90). Port of Python `tests/test_readiness.py` and Rust
// `src/readiness.rs::tests`.
//
// The runner exposes its readiness through the gRPC `Health` service:
// while any probe is failing, the per-service name reports NOT_SERVING;
// once every probe is green it flips to SERVING. Aggregation is binary.

import (
	"context"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc/health/grpc_health_v1"
)

// ---------------------------------------------------------------------------
// probeConfigFromEnv
// ---------------------------------------------------------------------------

func TestProbeConfigFromEnv_DefaultsWhenUnset(t *testing.T) {
	t.Setenv(EnvReadinessProbeInterval, "")
	t.Setenv(EnvReadinessProbeTimeout, "")
	interval, timeout := ProbeConfigFromEnv()
	if interval != DefaultProbeInterval {
		t.Errorf("interval default mismatch: got %v, want %v", interval, DefaultProbeInterval)
	}
	if timeout != DefaultProbeTimeout {
		t.Errorf("timeout default mismatch: got %v, want %v", timeout, DefaultProbeTimeout)
	}
}

func TestProbeConfigFromEnv_ReadsEnv(t *testing.T) {
	t.Setenv(EnvReadinessProbeInterval, "5")
	t.Setenv(EnvReadinessProbeTimeout, "1")
	interval, timeout := ProbeConfigFromEnv()
	if interval != 5*time.Second {
		t.Errorf("interval mismatch: got %v, want 5s", interval)
	}
	if timeout != 1*time.Second {
		t.Errorf("timeout mismatch: got %v, want 1s", timeout)
	}
}

func TestProbeConfigFromEnv_FallsBackOnGarbage(t *testing.T) {
	t.Setenv(EnvReadinessProbeInterval, "thirty")
	t.Setenv(EnvReadinessProbeTimeout, "two")
	interval, timeout := ProbeConfigFromEnv()
	if interval != DefaultProbeInterval {
		t.Errorf("garbage interval should fall back: got %v", interval)
	}
	if timeout != DefaultProbeTimeout {
		t.Errorf("garbage timeout should fall back: got %v", timeout)
	}
}

// ---------------------------------------------------------------------------
// TransportProbe / TransportSignal
// ---------------------------------------------------------------------------

func TestTransportProbe_StartsUnbound(t *testing.T) {
	probe, signal := NewTransportProbe()
	if probe.Name() != "transport" {
		t.Errorf("probe name: got %q, want %q", probe.Name(), "transport")
	}
	if probe.Check(context.Background()) {
		t.Error("expected probe to start unbound")
	}
	if signal.IsBound() {
		t.Error("expected signal to start unbound")
	}
}

func TestTransportProbe_FlipsAfterSignalMarked(t *testing.T) {
	probe, signal := NewTransportProbe()
	signal.MarkBound()
	if !probe.Check(context.Background()) {
		t.Error("expected probe to flip after MarkBound")
	}
}

func TestTransportSignal_OneWay(t *testing.T) {
	_, signal := NewTransportProbe()
	signal.MarkBound()
	if !signal.IsBound() {
		t.Error("signal must be bound after MarkBound")
	}
	// No unmark API. Calling MarkBound again is idempotent.
	signal.MarkBound()
	if !signal.IsBound() {
		t.Error("signal must remain bound")
	}
}

// ---------------------------------------------------------------------------
// OutputDomainProbe / BusProbe — TCP/UDS connect checks
// ---------------------------------------------------------------------------

func TestOutputDomainProbe_TCPSucceedsWhenListenerBound(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer lis.Close()
	go func() {
		for {
			conn, err := lis.Accept()
			if err != nil {
				return
			}
			conn.Close()
		}
	}()

	probe := NewOutputDomainProbe("downstream", lis.Addr().String())
	if probe.Name() != "downstream" {
		t.Errorf("name: got %q", probe.Name())
	}
	if !probe.Check(context.Background()) {
		t.Error("expected probe to report ok with bound listener")
	}
}

func TestOutputDomainProbe_TCPFailsWhenNothingListening(t *testing.T) {
	// Use a port that is unlikely to be bound. Bind+immediately-close to
	// reserve and free the port, then probe.
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := lis.Addr().String()
	lis.Close()

	probe := NewOutputDomainProbe("downstream", addr)
	if probe.Check(context.Background()) {
		t.Error("expected probe to fail with nothing listening")
	}
}

func TestOutputDomainProbe_UDSSucceedsWhenSocketBound(t *testing.T) {
	dir := t.TempDir()
	sockPath := filepath.Join(dir, "ready.sock")
	lis, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("uds listen: %v", err)
	}
	defer lis.Close()
	go func() {
		for {
			conn, err := lis.Accept()
			if err != nil {
				return
			}
			conn.Close()
		}
	}()

	probe := NewOutputDomainProbe("uds-downstream", sockPath)
	if !probe.Check(context.Background()) {
		t.Error("expected probe to succeed with bound UDS")
	}
}

func TestOutputDomainProbe_UDSFailsWhenSocketMissing(t *testing.T) {
	dir := t.TempDir()
	probe := NewOutputDomainProbe("uds", filepath.Join(dir, "missing.sock"))
	if probe.Check(context.Background()) {
		t.Error("expected probe to fail for missing UDS")
	}
}

func TestBusProbe_FromEnvReturnsNilWhenUnset(t *testing.T) {
	t.Setenv(EnvBusEndpoint, "")
	if p := BusProbeFromEnv(); p != nil {
		t.Errorf("expected nil when env unset, got %v", p)
	}
}

func TestBusProbe_FromEnvParsesTCP(t *testing.T) {
	t.Setenv(EnvBusEndpoint, "kafka.bus.svc:9092")
	p := BusProbeFromEnv()
	if p == nil {
		t.Fatal("expected non-nil bus probe")
	}
	if p.Name() != "bus" {
		t.Errorf("expected name=bus, got %q", p.Name())
	}
}

// ---------------------------------------------------------------------------
// ReadinessSupervisor — aggregation + WARN logging on cause
// ---------------------------------------------------------------------------

// fakeProbe is a Probe whose check() result is controlled per call.
type fakeProbe struct {
	name    string
	results []bool
	calls   atomic.Int64
	mu      sync.Mutex
}

func (p *fakeProbe) Name() string { return p.name }
func (p *fakeProbe) Check(_ context.Context) bool {
	p.calls.Add(1)
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.results) == 0 {
		return false
	}
	r := p.results[0]
	if len(p.results) > 1 {
		p.results = p.results[1:]
	}
	return r
}

// slowProbe sleeps for `delay` before returning true. Used to exercise
// the per-probe timeout path.
type slowProbe struct {
	name  string
	delay time.Duration
}

func (p *slowProbe) Name() string { return p.name }
func (p *slowProbe) Check(ctx context.Context) bool {
	select {
	case <-time.After(p.delay):
		return true
	case <-ctx.Done():
		return false
	}
}

// panickingProbe panics inside Check — supervisor must survive.
type panickingProbe struct {
	name string
	msg  string
}

func (p *panickingProbe) Name() string               { return p.name }
func (p *panickingProbe) Check(context.Context) bool { panic(p.msg) }

func TestSupervisorTick_AllProbesOkReturnsTrue(t *testing.T) {
	probes := []Probe{
		&fakeProbe{name: "a", results: []bool{true}},
		&fakeProbe{name: "b", results: []bool{true}},
	}
	if !SupervisorTick(probes, 100*time.Millisecond) {
		t.Error("expected all-ok tick to return true")
	}
}

func TestSupervisorTick_AnyFailReturnsFalse(t *testing.T) {
	probes := []Probe{
		&fakeProbe{name: "a", results: []bool{true}},
		&fakeProbe{name: "b", results: []bool{false}},
	}
	if SupervisorTick(probes, 100*time.Millisecond) {
		t.Error("expected mixed tick to return false")
	}
}

func TestSupervisorTick_SurvivesPanickingProbe(t *testing.T) {
	probes := []Probe{
		&fakeProbe{name: "a", results: []bool{true}},
		&panickingProbe{name: "boom", msg: "supervisor must survive this"},
		&fakeProbe{name: "c", results: []bool{true}},
	}
	if SupervisorTick(probes, 100*time.Millisecond) {
		t.Error("expected panicking probe to fail tick")
	}
}

func TestSupervisorTick_SlowProbeIsTimeout(t *testing.T) {
	probes := []Probe{&slowProbe{name: "slow", delay: 200 * time.Millisecond}}
	if SupervisorTick(probes, 20*time.Millisecond) {
		t.Error("expected slow probe to time out and fail tick")
	}
}

// ---------------------------------------------------------------------------
// gRPC Health.Check wiring — server.go must reflect probe status
// ---------------------------------------------------------------------------
//
// This is the load-bearing parity assertion of audit #68: while any
// probe is failing, the health service must report NOT_SERVING; once all
// probes flip to ok, it must report SERVING.

// liveSignal lets a test toggle a probe's result mid-flight.
type liveSignal struct {
	ok atomic.Bool
}

func (s *liveSignal) Name() string               { return "live" }
func (s *liveSignal) Check(context.Context) bool { return s.ok.Load() }
func (s *liveSignal) Set(v bool)                 { s.ok.Store(v) }

func TestHealthCheck_ReflectsProbeStatus(t *testing.T) {
	// One live probe that starts NOT_SERVING; flip it and expect SERVING.
	signal := &liveSignal{}
	signal.Set(false)

	healthSrv := NewReadinessHealthServer()
	supervisor := NewReadinessSupervisor(
		[]Probe{signal},
		healthSrv,
		[]string{"", "MyService"},
		10*time.Millisecond,
		100*time.Millisecond,
	)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go supervisor.Run(ctx)

	// Wait for at least one tick.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		resp, err := healthSrv.Check(context.Background(), &grpc_health_v1.HealthCheckRequest{})
		if err == nil && resp.Status == grpc_health_v1.HealthCheckResponse_NOT_SERVING {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	resp, err := healthSrv.Check(context.Background(), &grpc_health_v1.HealthCheckRequest{})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if resp.Status != grpc_health_v1.HealthCheckResponse_NOT_SERVING {
		t.Fatalf("expected NOT_SERVING while probe is down, got %v", resp.Status)
	}

	// Flip to ok and expect the supervisor to publish SERVING.
	signal.Set(true)
	got := waitForStatus(t, healthSrv, "", grpc_health_v1.HealthCheckResponse_SERVING, 1*time.Second)
	if got != grpc_health_v1.HealthCheckResponse_SERVING {
		t.Errorf("after recover: expected SERVING, got %v", got)
	}
	gotNamed := waitForStatus(t, healthSrv, "MyService", grpc_health_v1.HealthCheckResponse_SERVING, 1*time.Second)
	if gotNamed != grpc_health_v1.HealthCheckResponse_SERVING {
		t.Errorf("after recover (MyService): expected SERVING, got %v", gotNamed)
	}
}

func TestHealthCheck_StartsNotServingUntilTransportProbeBinds(t *testing.T) {
	// The transport probe is the canonical "started but not yet bound"
	// gate. Without MarkBound, Health.Check must report NOT_SERVING.
	probe, signal := NewTransportProbe()
	healthSrv := NewReadinessHealthServer()
	supervisor := NewReadinessSupervisor(
		[]Probe{probe},
		healthSrv,
		[]string{""},
		10*time.Millisecond,
		100*time.Millisecond,
	)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go supervisor.Run(ctx)

	// Wait for the first tick.
	time.Sleep(40 * time.Millisecond)
	resp, err := healthSrv.Check(context.Background(), &grpc_health_v1.HealthCheckRequest{})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if resp.Status != grpc_health_v1.HealthCheckResponse_NOT_SERVING {
		t.Fatalf("expected NOT_SERVING before MarkBound, got %v", resp.Status)
	}

	signal.MarkBound()
	got := waitForStatus(t, healthSrv, "", grpc_health_v1.HealthCheckResponse_SERVING, 1*time.Second)
	if got != grpc_health_v1.HealthCheckResponse_SERVING {
		t.Errorf("after MarkBound: expected SERVING, got %v", got)
	}
}

// waitForStatus polls Health.Check until it reports `want` or `timeout` elapses.
func waitForStatus(
	t *testing.T,
	srv *ReadinessHealthServer,
	service string,
	want grpc_health_v1.HealthCheckResponse_ServingStatus,
	timeout time.Duration,
) grpc_health_v1.HealthCheckResponse_ServingStatus {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last grpc_health_v1.HealthCheckResponse_ServingStatus
	for time.Now().Before(deadline) {
		resp, err := srv.Check(context.Background(), &grpc_health_v1.HealthCheckRequest{Service: service})
		if err == nil {
			last = resp.Status
			if resp.Status == want {
				return resp.Status
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	return last
}

// ---------------------------------------------------------------------------
// Shutdown — flips all registered names to NOT_SERVING (audit #83)
// ---------------------------------------------------------------------------

func TestReadinessHealthServer_PublishShutdown(t *testing.T) {
	srv := NewReadinessHealthServer()
	srv.SetServingStatus("", grpc_health_v1.HealthCheckResponse_SERVING)
	srv.SetServingStatus("MyService", grpc_health_v1.HealthCheckResponse_SERVING)

	PublishShutdownStatus(srv, []string{"", "MyService"})

	r1, _ := srv.Check(context.Background(), &grpc_health_v1.HealthCheckRequest{})
	if r1.Status != grpc_health_v1.HealthCheckResponse_NOT_SERVING {
		t.Errorf("default service: expected NOT_SERVING, got %v", r1.Status)
	}
	r2, _ := srv.Check(context.Background(), &grpc_health_v1.HealthCheckRequest{Service: "MyService"})
	if r2.Status != grpc_health_v1.HealthCheckResponse_NOT_SERVING {
		t.Errorf("MyService: expected NOT_SERVING, got %v", r2.Status)
	}
}

// ---------------------------------------------------------------------------
// Smoke: ReadinessSupervisor loops until ctx cancelled
// ---------------------------------------------------------------------------

func TestReadinessSupervisor_LoopsUntilCancelled(t *testing.T) {
	probe := &fakeProbe{name: "p", results: []bool{true}}
	srv := NewReadinessHealthServer()
	supervisor := NewReadinessSupervisor(
		[]Probe{probe},
		srv,
		[]string{""},
		5*time.Millisecond,
		100*time.Millisecond,
	)
	ctx, cancel := context.WithCancel(context.Background())
	go supervisor.Run(ctx)
	time.Sleep(40 * time.Millisecond)
	first := probe.calls.Load()
	time.Sleep(40 * time.Millisecond)
	second := probe.calls.Load()
	cancel()
	if second <= first {
		t.Errorf("supervisor stopped ticking: first=%d second=%d", first, second)
	}
}

// Ensure the supervisor's panic recovery preserves the test for go vet:
// unused imports flagged here would fail compilation.
var _ = errors.New
var _ = fmt.Sprintf
