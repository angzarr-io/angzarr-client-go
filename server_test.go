package angzarr

// Per-instance transport configuration (taskloom nutty-trout): a server's
// port is an explicit per-instance input, never written back to the
// process environment. These tests pin the two bugs the env write-back
// caused — cross-server port contamination and the logged-vs-bound port
// mismatch — plus the resolution precedence (env is config INPUT only).

import (
	"net"
	"os"
	"strconv"
	"testing"

	"google.golang.org/grpc"
)

func noopRegistrar(*grpc.Server) {}

// freePort grabs an ephemeral port the OS considers free right now.
func freePort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("free port probe: %v", err)
	}
	defer l.Close()
	return strconv.Itoa(l.Addr().(*net.TCPAddr).Port)
}

func clearTransportEnv(t *testing.T) {
	t.Helper()
	t.Setenv("TRANSPORT_TYPE", "")
	t.Setenv(EnvBindAddress, "")
	t.Setenv("PORT", "")
}

// Two servers in one process bind their own declared ports. Pre-fix the
// first CreateServerE wrote its DefaultPort into the PORT env, so the
// second instance inherited the first's port instead of its own.
func TestCreateServerE_TwoInstancesBindDistinctPorts(t *testing.T) {
	clearTransportEnv(t)
	p1, p2 := freePort(t), freePort(t)

	srv1, l1, _, _, _, cleanup1, err := CreateServerE(noopRegistrar, ServerOptions{DefaultPort: p1})
	if err != nil {
		t.Fatalf("first server: %v", err)
	}
	defer func() { srv1.Stop(); cleanup1() }()

	srv2, l2, _, _, _, cleanup2, err := CreateServerE(noopRegistrar, ServerOptions{DefaultPort: p2})
	if err != nil {
		t.Fatalf("second server: %v (port contamination from the first instance?)", err)
	}
	defer func() { srv2.Stop(); cleanup2() }()

	got1 := l1.Addr().(*net.TCPAddr).Port
	got2 := l2.Addr().(*net.TCPAddr).Port
	if strconv.Itoa(got1) != p1 {
		t.Errorf("first server bound %d, want its declared %s", got1, p1)
	}
	if strconv.Itoa(got2) != p2 {
		t.Errorf("second server bound %d, want its declared %s — it inherited the first instance's port", got2, p2)
	}
}

// Env is config INPUT only: resolving a server's port from DefaultPort
// must not store it back into the process environment.
func TestCreateServerE_DoesNotWriteEnv(t *testing.T) {
	clearTransportEnv(t)

	srv, _, _, _, _, cleanup, err := CreateServerE(noopRegistrar, ServerOptions{DefaultPort: freePort(t)})
	if err != nil {
		t.Fatalf("CreateServerE: %v", err)
	}
	defer func() { srv.Stop(); cleanup() }()

	if got := os.Getenv("PORT"); got != "" {
		t.Errorf("CreateServerE wrote PORT=%q into the environment; env must stay an input-only channel", got)
	}
}

// Resolution precedence for one instance: ANGZARR_BIND_ADDRESS > PORT env
// > per-instance DefaultPort > builtin 50052.
func TestResolveTransportConfig_Precedence(t *testing.T) {
	tests := []struct {
		name        string
		bindAddress string
		portEnv     string
		defaultPort string
		want        string
	}{
		{"explicit bind address wins", "[::1]:9999", "12345", "50061", "[::1]:9999"},
		{"PORT env beats per-instance default", "", "12345", "50061", DefaultBindHost + ":12345"},
		{"per-instance default beats builtin", "", "", "50061", DefaultBindHost + ":50061"},
		{"builtin fallback", "", "", "", DefaultBindHost + ":50052"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("TRANSPORT_TYPE", "")
			t.Setenv(EnvBindAddress, tt.bindAddress)
			t.Setenv("PORT", tt.portEnv)
			cfg := ResolveTransportConfig(ServerOptions{DefaultPort: tt.defaultPort})
			if cfg.Type != "tcp" || cfg.Address != tt.want {
				t.Errorf("config = %+v, want tcp %s", cfg, tt.want)
			}
		})
	}
}
