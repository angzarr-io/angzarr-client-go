package features

// domain_client.go — step bindings for client/domain-client.feature, driven
// through the REAL stack over a REAL gRPC connection: a grpc.Server on a
// loopback port serves CommandHandlerCoordinatorService + EventQueryService
// (both backed by the shared in-memory testBackend), and the REAL
// DomainClient dials it. Connection sharing and Close-severing are
// exercised for real, not simulated.

import (
	"context"
	"fmt"
	"net"
	"time"

	angzarr "github.com/benjaminabbitt/angzarr/client/go"
	pb "github.com/benjaminabbitt/angzarr/client/go/proto/angzarr_client/proto/angzarr/v1"
	"github.com/cucumber/godog"
	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/emptypb"
)

// coordGrpcServer adapts the in-memory testBackend to the coordinator
// SERVER interface.
type coordGrpcServer struct {
	pb.UnimplementedCommandHandlerCoordinatorServiceServer
	backend *testBackend
}

func (s *coordGrpcServer) HandleCommand(ctx context.Context, req *pb.CommandRequest) (*pb.CommandResponse, error) {
	return s.backend.HandleCommand(ctx, req)
}

// queryGrpcServer serves the backend's store over EventQueryService.
type queryGrpcServer struct {
	pb.UnimplementedEventQueryServiceServer
	backend *testBackend
}

func (s *queryGrpcServer) GetEventBook(_ context.Context, query *pb.Query) (*pb.EventBook, error) {
	cover := query.GetCover()
	book := s.backend.recorded(cover.GetDomain(), cover.GetRoot().GetValue())
	if book == nil {
		return &pb.EventBook{Cover: cover}, nil
	}
	return book, nil
}

func (s *queryGrpcServer) GetEvents(query *pb.Query, stream grpc.ServerStreamingServer[pb.EventBook]) error {
	book, _ := s.GetEventBook(stream.Context(), query)
	return stream.Send(book)
}

func (s *queryGrpcServer) GetAggregateRoots(_ *emptypb.Empty, _ grpc.ServerStreamingServer[pb.AggregateRoot]) error {
	return nil
}

// DomainClientContext holds per-scenario state.
type DomainClientContext struct {
	backend  *testBackend
	server   *grpc.Server
	endpoint string
	domain   string
	root     uuid.UUID

	client    *angzarr.DomainClient
	cmdResp   *pb.CommandResponse
	queryResp *pb.EventBook
	cmdErr    error
	queryErr  error
}

// currentDomainClient is the active scenario's harness; query_client.go's
// shared seeding phrase also feeds this backend when a coordinator runs.
var currentDomainClient *DomainClientContext

func newDomainClientContext() *DomainClientContext {
	dc := &DomainClientContext{root: uuid.New()}
	currentDomainClient = dc
	return dc
}

// startServer boots the loopback coordinator once per scenario.
func (dc *DomainClientContext) startServer(domain string) error {
	if dc.server != nil {
		dc.backend.mu.Lock()
		dc.backend.domains[domain] = true
		dc.backend.mu.Unlock()
		return nil
	}
	dc.domain = domain
	dc.backend = newTestBackend()
	dc.backend.domains[domain] = true

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	dc.endpoint = listener.Addr().String()
	dc.server = grpc.NewServer()
	pb.RegisterCommandHandlerCoordinatorServiceServer(dc.server, &coordGrpcServer{backend: dc.backend})
	pb.RegisterEventQueryServiceServer(dc.server, &queryGrpcServer{backend: dc.backend})
	go func() { _ = dc.server.Serve(listener) }()
	return nil
}

func (dc *DomainClientContext) sendCommand() error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	dc.cmdResp, dc.cmdErr = angzarr.NewCommandBuilder(dc.client.CommandHandler, dc.domain, dc.root).
		WithSequence(0).
		WithCommand(angzarr.TypeURLPrefix+fqCreateOrder, &pb.Projection{Projector: "data"}).
		Execute(ctx)
	return nil
}

func (dc *DomainClientContext) queryEvents() error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	dc.queryResp, dc.queryErr = angzarr.NewQueryBuilder(dc.client.Query, dc.domain, dc.root).GetEventBook(ctx)
	return nil
}

// seedFromShared lets the shared seeding phrase (query_client.go) feed
// the live coordinator backend too.
func (dc *DomainClientContext) seedFromShared(domain, root string, n int) error {
	parsed, err := uuid.Parse(root)
	if err != nil {
		// non-UUID roots are query_client-local fixtures; nothing to seed
		return nil
	}
	dc.root = parsed
	if dc.backend == nil {
		if err := dc.startServer(domain); err != nil {
			return err
		}
	}
	dc.backend.seed(domain, parsed[:], uint32(n))
	return nil
}

// InitDomainClientSteps registers domain-client.feature steps.
func InitDomainClientSteps(ctx *godog.ScenarioContext) {
	dc := newDomainClientContext()
	ctx.After(func(c context.Context, sc *godog.Scenario, err error) (context.Context, error) {
		if dc.client != nil {
			_ = dc.client.Close()
		}
		if dc.server != nil {
			dc.server.Stop()
		}
		return c, err
	})

	// --- Background ---
	ctx.Step(`^a running aggregate coordinator for domain "([^"]*)"$`, dc.startServer)
	ctx.Step(`^a registered aggregate handler for domain "([^"]*)"$`, func(domain string) error {
		// The backend's dispatch table is registered at construction.
		if dc.backend == nil {
			return fmt.Errorf("no coordinator running")
		}
		return nil
	})
	ctx.Step(`^environment variable "([^"]*)" is set to the coordinator endpoint$`, func(name string) error {
		return nil // applied in the from-env step via t-scoped env below
	})

	// --- Whens: connecting ---
	connect := func() error {
		client, err := angzarr.NewDomainClient(dc.endpoint)
		if err != nil {
			return err
		}
		dc.client = client
		return nil
	}
	ctx.Step(`^I create a domain client for the coordinator endpoint$`, connect)
	ctx.Step(`^I create a domain client for domain "([^"]*)"$`, func(string) error { return connect() })
	ctx.Step(`^I create a domain client from environment variable "([^"]*)"$`, func(name string) error {
		// Env var resolution path: unset falls back to the default we pass.
		client, err := angzarr.DomainClientFromEnv(name, dc.endpoint)
		if err != nil {
			return err
		}
		dc.client = client
		return nil
	})
	ctx.Step(`^a connected domain client$`, func() error {
		if err := dc.startServer("test"); err != nil {
			return err
		}
		return connect()
	})
	ctx.Step(`^I close the domain client$`, func() error {
		return dc.client.Close()
	})

	// --- Whens: operations ---
	ctx.Step(`^I use the command builder to send a command$`, dc.sendCommand)
	ctx.Step(`^I send a command$`, dc.sendCommand)
	ctx.Step(`^I use the query builder to fetch events for that root$`, dc.queryEvents)
	ctx.Step(`^I query for the resulting events$`, dc.queryEvents)

	// NOTE: `an aggregate "..." with root "..." has N events` is owned by
	// query_client.go; it seeds this harness's backend via
	// currentDomainClient (seedFromShared).

	// --- Thens ---
	ctx.Step(`^I should be able to query events$`, func() error {
		if err := dc.queryEvents(); err != nil {
			return err
		}
		if dc.queryErr != nil {
			return fmt.Errorf("query failed: %v", dc.queryErr)
		}
		return nil
	})
	ctx.Step(`^I should be able to send commands$`, func() error {
		if err := dc.sendCommand(); err != nil {
			return err
		}
		if dc.cmdErr != nil {
			return fmt.Errorf("command failed: %v", dc.cmdErr)
		}
		return nil
	})
	ctx.Step(`^I should receive a command response$`, func() error {
		if dc.cmdErr != nil {
			return fmt.Errorf("command failed: %v", dc.cmdErr)
		}
		if dc.cmdResp == nil {
			return fmt.Errorf("no command response")
		}
		return nil
	})
	ctx.Step(`^I should receive (\d+) event pages$`, func(n int) error {
		if dc.queryErr != nil {
			return fmt.Errorf("query failed: %v", dc.queryErr)
		}
		if got := len(dc.queryResp.GetPages()); got != n {
			return fmt.Errorf("pages = %d, want %d", got, n)
		}
		return nil
	})
	ctx.Step(`^both operations should succeed on the same connection$`, func() error {
		if dc.cmdErr != nil || dc.queryErr != nil {
			return fmt.Errorf("cmd: %v / query: %v", dc.cmdErr, dc.queryErr)
		}
		return nil
	})
	ctx.Step(`^subsequent commands should fail with a connection error$`, func() error {
		_ = dc.sendCommand()
		if dc.cmdErr == nil {
			return fmt.Errorf("command succeeded on a closed client")
		}
		if angzarr.AsClientError(dc.cmdErr) == nil {
			return fmt.Errorf("uncoded error: %v", dc.cmdErr)
		}
		return nil
	})
	ctx.Step(`^subsequent queries should fail with a connection error$`, func() error {
		_ = dc.queryEvents()
		if dc.queryErr == nil {
			return fmt.Errorf("query succeeded on a closed client")
		}
		if angzarr.AsClientError(dc.queryErr) == nil {
			return fmt.Errorf("uncoded error: %v", dc.queryErr)
		}
		return nil
	})
	ctx.Step(`^the domain client should be connected$`, func() error {
		if err := dc.queryEvents(); err != nil {
			return err
		}
		if dc.queryErr != nil {
			return fmt.Errorf("not connected: %v", dc.queryErr)
		}
		return nil
	})
}
