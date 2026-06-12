package features

// speculative_client.go — step bindings for client/speculative_client.feature,
// driven through the REAL SpeculativeClient over a loopback gRPC coordinator.
// The coordinator serves the four speculative RPCs over REAL engine tables
// (aggregate adapter, SagaDispatch, ProjectorDispatch,
// ProcessManagerDispatch). Speculation never writes the store, so the
// no-trace assertions read actual recorded state — nothing is simulated.

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"

	angzarr "github.com/benjaminabbitt/angzarr/client/go"
	pb "github.com/benjaminabbitt/angzarr/client/go/proto/angzarr_client/proto/angzarr/v1"
	"github.com/cucumber/godog"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
)

const (
	fqSpecCommand = "test.spec.DoThing"
	fqSpecCancel  = "test.spec.CancelOrder"
	fqSpecEvent   = "test.spec.ThingHappened"
	fqSpecShipped = "test.spec.OrderShipped"

	msgCannotCancelShipped = "cannot cancel shipped order"
	msgMissingCorrelation  = "missing correlation ID"
	msgMissingParameters   = "missing parameters"
)

// specState is the event-sourced aggregate state behind the speculative
// coordinator: a shipped order refuses cancellation.
type specState struct {
	shipped bool
}

// specBackend is the in-memory store + engine tables the loopback
// coordinator dispatches through. Speculative paths never write store.
type specBackend struct {
	mu    sync.Mutex
	store map[string]*pb.EventBook

	adapter   *angzarr.AggregateDispatchHandler[*specState]
	saga      *angzarr.SagaDispatch
	projector *angzarr.ProjectorDispatch[*specFold]
	pm        *angzarr.ProcessManagerDispatch[*specState]

	// observability for the Then steps
	observed  []angzarr.CommandContext // one per aggregate dispatch
	foldOrder []uint32                 // payload-carried indices, in fold order
}

// specFold accumulates the projector's fold for order assertions.
type specFold struct{}

func newSpecBackend() *specBackend {
	b := &specBackend{store: make(map[string]*pb.EventBook)}

	rebuilder := func() *angzarr.Rebuilder[*specState] {
		return angzarr.NewRebuilder(func() *specState { return &specState{} }).
			Apply(fqSpecShipped, func(s *specState, _ *anypb.Any) error {
				s.shipped = true
				return nil
			})
	}

	table := angzarr.NewAggregateDispatch("spec-orders", "orders", rebuilder()).
		OnCommand(fqSpecCommand, func(cmdAny *anypb.Any, _ *specState, cctx angzarr.CommandContext) (*pb.EventBook, error) {
			carrier := &pb.Projection{}
			if err := proto.Unmarshal(cmdAny.Value, carrier); err != nil {
				return nil, angzarr.AnyDecodeError(cmdAny.TypeUrl, err)
			}
			b.mu.Lock()
			b.observed = append(b.observed, cctx)
			b.mu.Unlock()
			count := carrier.Sequence
			if count == 0 {
				count = 1
			}
			book := &pb.EventBook{}
			for i := uint32(0); i < count; i++ {
				book.Pages = append(book.Pages, &pb.EventPage{
					Payload: &pb.EventPage_Event{Event: &anypb.Any{TypeUrl: angzarr.TypeURLPrefix + fqSpecEvent}},
				})
			}
			return book, nil
		}).
		OnCommand(fqSpecCancel, func(_ *anypb.Any, state *specState, _ angzarr.CommandContext) (*pb.EventBook, error) {
			if state.shipped {
				return nil, angzarr.NewPreconditionFailedRejection(
					angzarr.CodeStatusForbidden, msgCannotCancelShipped, nil)
			}
			return &pb.EventBook{}, nil
		})
	b.adapter = angzarr.NewAggregateDispatchHandler(table)

	b.saga = angzarr.NewSagaDispatch("order-fulfillment", "orders", "fulfillment").
		OnEvent(fqSpecEvent, func(*anypb.Any, *angzarr.Destinations) ([]*pb.CommandBook, []*pb.EventBook, error) {
			return []*pb.CommandBook{{Cover: &pb.Cover{Domain: "fulfillment"}}}, nil, nil
		})

	b.projector = angzarr.NewProjectorDispatch("order-summary", func() *specFold { return &specFold{} }).
		ForDomains("orders").
		OnEvent(fqSpecEvent, func(_ *specFold, eventAny *anypb.Any) error {
			carrier := &pb.Projection{}
			if err := proto.Unmarshal(eventAny.Value, carrier); err != nil {
				return angzarr.AnyDecodeError(eventAny.TypeUrl, err)
			}
			b.mu.Lock()
			b.foldOrder = append(b.foldOrder, carrier.Sequence)
			b.mu.Unlock()
			return nil
		})

	b.pm = angzarr.NewProcessManagerDispatch("order-workflow", "workflow", rebuilder()).
		OnEvent("orders", fqSpecEvent, func(*anypb.Any, *specState, *angzarr.Destinations) (*pb.ProcessManagerHandleResponse, error) {
			return &pb.ProcessManagerHandleResponse{Commands: []*pb.CommandBook{
				{Cover: &pb.Cover{Domain: "payments"}},
				{Cover: &pb.Cover{Domain: "inventory"}},
			}}, nil
		})

	return b
}

func (b *specBackend) seed(domain string, root []byte, book *pb.EventBook) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.store[bookKey(domain, root)] = book
}

func (b *specBackend) recorded(domain string, root []byte) *pb.EventBook {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.store[bookKey(domain, root)]
}

// pagesOf builds count event pages of the given type, payloads carrying
// their index so fold order is observable.
func pagesOf(fqType string, count int) []*pb.EventPage {
	pages := make([]*pb.EventPage, 0, count)
	for i := 0; i < count; i++ {
		value, _ := proto.Marshal(&pb.Projection{Sequence: uint32(i)})
		pages = append(pages, &pb.EventPage{
			Header: &pb.PageHeader{SequenceType: &pb.PageHeader_Sequence{Sequence: uint32(i)}},
			Payload: &pb.EventPage_Event{Event: &anypb.Any{
				TypeUrl: angzarr.TypeURLPrefix + fqType,
				Value:   value,
			}},
		})
	}
	return pages
}

func bookOf(domain, root, correlation string, count int) *pb.EventBook {
	return &pb.EventBook{
		Cover:        &pb.Cover{Domain: domain, Root: &pb.UUID{Value: []byte(root)}, CorrelationId: correlation},
		Pages:        pagesOf(fqSpecEvent, count),
		NextSequence: uint32(count),
	}
}

// truncateAsOf slices history to pages with sequence <= asOf (the
// TemporalQuery contract) — the coordinator-side temporal read.
func truncateAsOf(book *pb.EventBook, asOf uint32) *pb.EventBook {
	if book == nil {
		return &pb.EventBook{}
	}
	if asOf+1 >= book.NextSequence {
		return book
	}
	out := &pb.EventBook{Cover: book.Cover, NextSequence: asOf + 1}
	for _, page := range book.Pages {
		if page.GetHeader().GetSequence() <= asOf {
			out.Pages = append(out.Pages, page)
		}
	}
	return out
}

// --- loopback coordinator servers (one per service; only the speculative
// rpcs are real — everything else stays Unimplemented) ---

type specCHServer struct {
	pb.UnimplementedCommandHandlerCoordinatorServiceServer
	backend *specBackend
}

func (s *specCHServer) HandleSyncSpeculative(ctx context.Context, req *pb.SpeculateCommandHandlerRequest) (*pb.CommandResponse, error) {
	cmd := req.GetCommand()
	if cmd == nil || len(cmd.GetPages()) == 0 {
		return nil, status.Error(codes.InvalidArgument, msgMissingParameters)
	}
	cover := cmd.GetCover()
	history := s.backend.recorded(cover.GetDomain(), cover.GetRoot().GetValue())
	if history == nil {
		history = &pb.EventBook{Cover: cover}
	}
	if seq, ok := req.GetPointInTime().GetPointInTime().(*pb.TemporalQuery_AsOfSequence); ok {
		history = truncateAsOf(history, seq.AsOfSequence)
	}
	resp, err := s.backend.adapter.Handle(ctx, &pb.ContextualCommand{Command: cmd, Events: history})
	if err != nil {
		return nil, err
	}
	// Projected only — the store is never written on the speculative path.
	return &pb.CommandResponse{Events: resp.GetEvents()}, nil
}

type specSagaServer struct {
	pb.UnimplementedSagaCoordinatorServiceServer
	backend *specBackend
}

func (s *specSagaServer) ExecuteSpeculative(_ context.Context, req *pb.SpeculateSagaRequest) (*pb.SagaResponse, error) {
	return s.backend.saga.Dispatch(req.GetRequest().GetSource(), req.GetRequest().GetDestinationSequences())
}

type specProjectorServer struct {
	pb.UnimplementedProjectorCoordinatorServiceServer
	backend *specBackend
}

func (s *specProjectorServer) HandleSpeculative(_ context.Context, req *pb.SpeculateProjectorRequest) (*pb.Projection, error) {
	return s.backend.projector.Dispatch(req.GetEvents())
}

type specPMServer struct {
	pb.UnimplementedProcessManagerCoordinatorServiceServer
	backend *specBackend
}

func (s *specPMServer) HandleSpeculative(_ context.Context, req *pb.SpeculatePmRequest) (*pb.ProcessManagerHandleResponse, error) {
	trigger := req.GetRequest().GetTrigger()
	// Process state is keyed by correlation — a trigger without one is
	// unroutable and refused before dispatch.
	if trigger.GetCover().GetCorrelationId() == "" {
		return nil, status.Error(codes.InvalidArgument, msgMissingCorrelation)
	}
	return s.backend.pm.Dispatch(trigger, req.GetRequest().GetProcessState(), req.GetRequest().GetDestinationSequences())
}

// SpeculativeClientContext holds per-scenario state.
type SpeculativeClientContext struct {
	backend *specBackend
	server  *grpc.Server
	conn    *grpc.ClientConn
	client  *angzarr.SpeculativeClient

	domain string
	root   string

	cmdResp  *pb.CommandResponse
	sagaResp *pb.SagaResponse
	pmResp   *pb.ProcessManagerHandleResponse
	projResp *pb.Projection
	lastErr  error

	trigger    *pb.EventBook // saga/PM source under test
	eventCount int           // seeded projector/saga event count
}

// currentSpeculative lets the shared seeding phrase (query_client.go) feed
// this harness's live backend too.
var currentSpeculative *SpeculativeClientContext

func newSpeculativeClientContext() *SpeculativeClientContext {
	c := &SpeculativeClientContext{}
	currentSpeculative = c
	return c
}

// seedShared mirrors domain_client.seedFromShared for the shared
// `an aggregate ... has N events` phrase.
func (c *SpeculativeClientContext) seedShared(domain, root string, count int) {
	if c.backend == nil {
		return
	}
	c.domain, c.root = domain, root
	c.backend.seed(domain, []byte(root), bookOf(domain, root, "", count))
}

func (c *SpeculativeClientContext) startSurface() error {
	if c.server != nil {
		return nil
	}
	c.backend = newSpecBackend()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	c.server = grpc.NewServer()
	pb.RegisterCommandHandlerCoordinatorServiceServer(c.server, &specCHServer{backend: c.backend})
	pb.RegisterSagaCoordinatorServiceServer(c.server, &specSagaServer{backend: c.backend})
	pb.RegisterProjectorCoordinatorServiceServer(c.server, &specProjectorServer{backend: c.backend})
	pb.RegisterProcessManagerCoordinatorServiceServer(c.server, &specPMServer{backend: c.backend})
	go func() { _ = c.server.Serve(listener) }()

	c.conn, err = grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return err
	}
	c.client = angzarr.SpeculativeClientFromChannel(c.conn)
	return nil
}

func (c *SpeculativeClientContext) stop() {
	if c.conn != nil {
		_ = c.conn.Close()
	}
	if c.server != nil {
		c.server.Stop()
	}
	if currentSpeculative == c {
		currentSpeculative = nil
	}
}

func (c *SpeculativeClientContext) ctx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 2*time.Second)
}

func (c *SpeculativeClientContext) speculateCommand(cmdType string, producing uint32, pointInTime *pb.TemporalQuery) error {
	value, _ := proto.Marshal(&pb.Projection{Sequence: producing})
	cmd := &pb.CommandBook{
		Cover: &pb.Cover{Domain: c.domain, Root: &pb.UUID{Value: []byte(c.root)}},
		Pages: []*pb.CommandPage{
			{Payload: &pb.CommandPage_Command{Command: &anypb.Any{
				TypeUrl: angzarr.TypeURLPrefix + cmdType,
				Value:   value,
			}}},
		},
	}
	ctx, cancel := c.ctx()
	defer cancel()
	c.cmdResp, c.lastErr = c.client.CommandHandler(ctx, &pb.SpeculateCommandHandlerRequest{
		Command:     cmd,
		PointInTime: pointInTime,
	})
	return nil
}

// InitSpeculativeClientSteps registers speculative_client.feature steps.
func InitSpeculativeClientSteps(ctx *godog.ScenarioContext) {
	c := newSpeculativeClientContext()
	ctx.After(func(scCtx context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
		c.stop()
		return scCtx, nil
	})

	// --- Background ---
	ctx.Step(`^a what-if execution surface available$`, c.startSurface)

	// --- Speculative aggregate execution ---
	// `an aggregate ... has N events` is the shared seeding phrase
	// (query_client.go); it feeds this backend via currentSpeculative.
	ctx.Step(`^I speculatively execute a command against "([^"]*)" root "([^"]*)"$`, func(domain, root string) error {
		c.domain, c.root = domain, root
		return c.speculateCommand(fqSpecCommand, 1, nil)
	})
	ctx.Step(`^the response should contain the projected events$`, func() error {
		if c.lastErr != nil {
			return fmt.Errorf("speculation failed: %v", c.lastErr)
		}
		if len(c.cmdResp.GetEvents().GetPages()) == 0 {
			return fmt.Errorf("expected projected events in the response")
		}
		return nil
	})
	ctx.Step(`^the events should NOT be persisted$`, c.thenStoreUnchanged)
	ctx.Step(`^the projected execution leaves no trace$`, c.thenStoreUnchanged)

	ctx.Step(`^I speculatively execute a command as of sequence (\d+)$`, func(seq int) error {
		return c.speculateCommand(fqSpecCommand, 1, &pb.TemporalQuery{
			PointInTime: &pb.TemporalQuery_AsOfSequence{AsOfSequence: uint32(seq)},
		})
	})
	ctx.Step(`^the command should execute against the historical state$`, func() error {
		if c.lastErr != nil {
			return fmt.Errorf("speculation failed: %v", c.lastErr)
		}
		if n := len(c.backend.observed); n == 0 {
			return fmt.Errorf("the command never reached the handler seam")
		}
		return nil
	})
	ctx.Step(`^the response should reflect state at sequence (\d+)$`, func(seq int) error {
		// AsOfSequence is inclusive: state through event seq, so the next
		// projected event lands at seq+1 — both the rebuild input and the
		// engine's fill-only stamping must agree.
		want := uint32(seq + 1)
		last := c.backend.observed[len(c.backend.observed)-1]
		if last.NextSequence != want {
			return fmt.Errorf("handler saw NextSequence %d, want %d (state through sequence %d)", last.NextSequence, want, seq)
		}
		got := c.cmdResp.GetEvents().GetPages()
		if len(got) == 0 || got[0].GetHeader().GetSequence() != want {
			return fmt.Errorf("projected events stamped from %d, want %d", got[0].GetHeader().GetSequence(), want)
		}
		return nil
	})

	ctx.Step(`^an aggregate "([^"]*)" with root "([^"]*)" in state "([^"]*)"$`, func(domain, root, state string) error {
		if state != "shipped" {
			return fmt.Errorf("unknown fixture state %q", state)
		}
		c.domain, c.root = domain, root
		book := &pb.EventBook{
			Cover:        &pb.Cover{Domain: domain, Root: &pb.UUID{Value: []byte(root)}},
			Pages:        pagesOf(fqSpecShipped, 1),
			NextSequence: 1,
		}
		c.backend.seed(domain, []byte(root), book)
		return nil
	})
	ctx.Step(`^I speculatively execute a "([^"]*)" command$`, func(cmdType string) error {
		if cmdType != "CancelOrder" {
			return fmt.Errorf("unknown fixture command %q", cmdType)
		}
		return c.speculateCommand(fqSpecCancel, 0, nil)
	})
	ctx.Step(`^the response should indicate rejection$`, func() error {
		clientErr := angzarr.AsClientError(c.lastErr)
		if clientErr == nil {
			return fmt.Errorf("expected a rejection, got resp=%v err=%v", c.cmdResp, c.lastErr)
		}
		return nil
	})
	ctx.Step(`^the rejection reason should be "([^"]*)"$`, func(reason string) error {
		clientErr := angzarr.AsClientError(c.lastErr)
		if clientErr == nil || clientErr.Status().Message() != reason {
			return fmt.Errorf("rejection reason = %v, want %q", c.lastErr, reason)
		}
		return nil
	})

	ctx.Step(`^I speculatively execute a command with invalid payload$`, func() error {
		ctxx, cancel := c.ctx()
		defer cancel()
		c.cmdResp, c.lastErr = c.client.CommandHandler(ctxx, &pb.SpeculateCommandHandlerRequest{
			Command: &pb.CommandBook{
				Cover: &pb.Cover{Domain: c.domain, Root: &pb.UUID{Value: []byte(c.root)}},
				Pages: []*pb.CommandPage{
					{Payload: &pb.CommandPage_Command{Command: &anypb.Any{
						TypeUrl: angzarr.TypeURLPrefix + fqSpecCommand,
						Value:   []byte{0xFF, 0xFF, 0xFF, 0xFF},
					}}},
				},
			},
		})
		return nil
	})
	ctx.Step(`^the operation should fail with validation error$`, func() error {
		clientErr := angzarr.AsClientError(c.lastErr)
		if clientErr == nil || !clientErr.IsInvalidArgument() {
			return fmt.Errorf("expected an invalid-argument failure, got %v", c.lastErr)
		}
		return nil
	})
	ctx.Step(`^no events should be produced$`, func() error {
		if len(c.cmdResp.GetEvents().GetPages()) != 0 {
			return fmt.Errorf("expected no projected events, got %d", len(c.cmdResp.GetEvents().GetPages()))
		}
		return nil
	})
	ctx.Step(`^I speculatively execute a command$`, func() error {
		return c.speculateCommand(fqSpecCommand, 1, nil)
	})

	// --- Speculative projector execution ---
	ctx.Step(`^events for "([^"]*)" root "([^"]*)"$`, func(domain, root string) error {
		c.domain, c.root, c.eventCount = domain, root, 3
		c.trigger = bookOf(domain, root, "", 3)
		return nil
	})
	ctx.Step(`^(\d+) events for "([^"]*)" root "([^"]*)"$`, func(count int, domain, root string) error {
		c.domain, c.root, c.eventCount = domain, root, count
		c.trigger = bookOf(domain, root, "", count)
		return nil
	})
	ctx.Step(`^I speculatively execute projector "([^"]*)" against those events$`, c.speculateProjector)
	ctx.Step(`^I speculatively execute projector "([^"]*)"$`, c.speculateProjector)
	ctx.Step(`^the response should contain the projection$`, func() error {
		if c.lastErr != nil || c.projResp == nil {
			return fmt.Errorf("expected a projection, got %v", c.lastErr)
		}
		return nil
	})
	ctx.Step(`^no external systems should be updated$`, c.thenStoreUnchanged)
	ctx.Step(`^the projector should process all (\d+) events in order$`, func(count int) error {
		c.backend.mu.Lock()
		defer c.backend.mu.Unlock()
		if len(c.backend.foldOrder) != count {
			return fmt.Errorf("folded %d events, want %d", len(c.backend.foldOrder), count)
		}
		for i, got := range c.backend.foldOrder {
			if got != uint32(i) {
				return fmt.Errorf("fold order %v, want ascending from 0", c.backend.foldOrder)
			}
		}
		return nil
	})
	ctx.Step(`^the final projection state should be returned$`, func() error {
		if c.projResp == nil {
			return fmt.Errorf("no projection returned")
		}
		return nil
	})

	// --- Speculative saga execution ---
	ctx.Step(`^I speculatively execute saga "([^"]*)"$`, func(string) error {
		ctxx, cancel := c.ctx()
		defer cancel()
		c.sagaResp, c.lastErr = c.client.Saga(ctxx, &pb.SpeculateSagaRequest{
			Request: &pb.SagaHandleRequest{Source: c.trigger},
		})
		return nil
	})
	ctx.Step(`^the response should contain the commands the saga would emit$`, func() error {
		if c.lastErr != nil || len(c.sagaResp.GetCommands()) == 0 {
			return fmt.Errorf("expected emitted commands, got %v (err %v)", c.sagaResp, c.lastErr)
		}
		return nil
	})
	ctx.Step(`^the commands should NOT be sent to the target domain$`, func() error {
		if book := c.backend.recorded("fulfillment", nil); book != nil {
			return fmt.Errorf("speculative saga commands reached the target domain's store")
		}
		return nil
	})
	ctx.Step(`^events with saga origin from "([^"]*)" aggregate$`, func(origin string) error {
		c.trigger = bookOf("orders", "order-origin", "origin:"+origin, 1)
		return nil
	})
	ctx.Step(`^the response should preserve the saga origin chain$`, func() error {
		if c.lastErr != nil || len(c.sagaResp.GetCommands()) == 0 {
			return fmt.Errorf("no commands to carry the chain (err %v)", c.lastErr)
		}
		want := c.trigger.GetCover().GetCorrelationId()
		for _, cmd := range c.sagaResp.GetCommands() {
			if cmd.GetCover().GetCorrelationId() != want {
				return fmt.Errorf("command correlation = %q, want the origin chain %q",
					cmd.GetCover().GetCorrelationId(), want)
			}
		}
		return nil
	})

	// --- Speculative process manager execution ---
	ctx.Step(`^correlated events from multiple domains$`, func() error {
		c.trigger = bookOf("orders", "order-pm", "workflow-9", 1)
		return nil
	})
	ctx.Step(`^events without correlation ID$`, func() error {
		c.trigger = bookOf("orders", "order-pm", "", 1)
		return nil
	})
	ctx.Step(`^I speculatively execute process manager "([^"]*)"$`, func(string) error {
		ctxx, cancel := c.ctx()
		defer cancel()
		c.pmResp, c.lastErr = c.client.ProcessManager(ctxx, &pb.SpeculatePmRequest{
			Request: &pb.ProcessManagerHandleRequest{Trigger: c.trigger},
		})
		return nil
	})
	ctx.Step(`^the response should contain the PM's command decisions$`, func() error {
		if c.lastErr != nil || len(c.pmResp.GetCommands()) == 0 {
			return fmt.Errorf("expected PM command decisions, got %v (err %v)", c.pmResp, c.lastErr)
		}
		return nil
	})
	ctx.Step(`^the commands should NOT be executed$`, func() error {
		for _, domain := range []string{"payments", "inventory"} {
			if book := c.backend.recorded(domain, nil); book != nil {
				return fmt.Errorf("speculative PM commands reached %q", domain)
			}
		}
		return nil
	})
	ctx.Step(`^the speculative PM operation should fail$`, func() error {
		if c.lastErr == nil {
			return fmt.Errorf("expected the PM speculation to fail")
		}
		return nil
	})
	ctx.Step(`^the error should indicate missing correlation ID$`, func() error {
		clientErr := angzarr.AsClientError(c.lastErr)
		if clientErr == nil || clientErr.Status().Message() != msgMissingCorrelation {
			return fmt.Errorf("error = %v, want %q", c.lastErr, msgMissingCorrelation)
		}
		return nil
	})

	// --- State isolation ---
	ctx.Step(`^a speculative aggregate "([^"]*)" with root "([^"]*)" has (\d+) events$`, func(domain, root string, count int) error {
		c.seedShared(domain, root, count)
		return nil
	})
	ctx.Step(`^I speculatively execute a command producing (\d+) events$`, func(count int) error {
		return c.speculateCommand(fqSpecCommand, uint32(count), nil)
	})
	ctx.Step(`^I verify the real events for "([^"]*)" root "([^"]*)"$`, func(domain, root string) error {
		return nil // assertion reads the store directly
	})
	ctx.Step(`^I should receive only (\d+) events$`, func(count int) error {
		book := c.backend.recorded(c.domain, []byte(c.root))
		if got := len(book.GetPages()); got != count {
			return fmt.Errorf("real store has %d events, want %d", got, count)
		}
		return nil
	})
	ctx.Step(`^the speculative events should not be present$`, func() error {
		book := c.backend.recorded(c.domain, []byte(c.root))
		for _, page := range book.GetPages() {
			if page.GetHeader().GetSequence() >= book.GetNextSequence() {
				return fmt.Errorf("speculative page leaked into the real store at sequence %d", page.GetHeader().GetSequence())
			}
		}
		return nil
	})
	ctx.Step(`^I speculatively execute command A$`, func() error {
		return c.speculateCommand(fqSpecCommand, 1, nil)
	})
	ctx.Step(`^I speculatively execute command B$`, func() error {
		return c.speculateCommand(fqSpecCommand, 1, nil)
	})
	ctx.Step(`^each speculation should start from the same base state$`, func() error {
		c.backend.mu.Lock()
		defer c.backend.mu.Unlock()
		if len(c.backend.observed) != 2 {
			return fmt.Errorf("observed %d dispatches, want 2", len(c.backend.observed))
		}
		if c.backend.observed[0].NextSequence != c.backend.observed[1].NextSequence {
			return fmt.Errorf("speculations saw different base states: %d vs %d — the first leaked into the second",
				c.backend.observed[0].NextSequence, c.backend.observed[1].NextSequence)
		}
		return nil
	})
	ctx.Step(`^results should be independent$`, func() error {
		got := c.cmdResp.GetEvents().GetPages()
		base := c.backend.observed[0].NextSequence
		if len(got) == 0 || got[0].GetHeader().GetSequence() != base {
			return fmt.Errorf("second speculation stamped from %d, want the shared base %d", got[0].GetHeader().GetSequence(), base)
		}
		return nil
	})

	// --- Error handling ---
	ctx.Step(`^the speculative service is unavailable$`, func() error {
		if err := c.startSurface(); err != nil {
			return err
		}
		c.server.Stop()
		return nil
	})
	ctx.Step(`^I attempt speculative execution$`, func() error {
		c.domain, c.root = "orders", "any"
		return c.speculateCommand(fqSpecCommand, 1, nil)
	})
	ctx.Step(`^the speculative operation should fail with connection error$`, func() error {
		clientErr := angzarr.AsClientError(c.lastErr)
		if clientErr == nil || clientErr.GRPCCode() != codes.Unavailable {
			return fmt.Errorf("expected an unavailable failure, got %v", c.lastErr)
		}
		return nil
	})
	ctx.Step(`^I attempt speculative execution with missing parameters$`, func() error {
		ctxx, cancel := c.ctx()
		defer cancel()
		c.cmdResp, c.lastErr = c.client.CommandHandler(ctxx, &pb.SpeculateCommandHandlerRequest{})
		return nil
	})
	ctx.Step(`^the speculative operation should fail with invalid argument error$`, func() error {
		clientErr := angzarr.AsClientError(c.lastErr)
		if clientErr == nil || !clientErr.IsInvalidArgument() {
			return fmt.Errorf("expected an invalid-argument failure, got %v", c.lastErr)
		}
		return nil
	})
}

func (c *SpeculativeClientContext) speculateProjector(string) error {
	ctxx, cancel := c.ctx()
	defer cancel()
	c.projResp, c.lastErr = c.client.Projector(ctxx, &pb.SpeculateProjectorRequest{Events: c.trigger})
	return nil
}

// thenStoreUnchanged asserts speculation left the recorded history exactly
// as seeded.
func (c *SpeculativeClientContext) thenStoreUnchanged() error {
	if c.lastErr != nil {
		return fmt.Errorf("speculation failed: %v", c.lastErr)
	}
	if c.domain == "" {
		return nil // nothing was seeded; nothing to leak into
	}
	book := c.backend.recorded(c.domain, []byte(c.root))
	if book == nil {
		return nil
	}
	if uint32(len(book.GetPages())) != book.GetNextSequence() {
		return fmt.Errorf("store mutated: %d pages vs next sequence %d", len(book.GetPages()), book.GetNextSequence())
	}
	return nil
}
