package features

// aggregate_client.go — step bindings for client/aggregate_client.feature,
// driven through the REAL stack end to end:
//
//	CommandHandlerClient (real, via CommandHandlerClientFromService)
//	  → testBackend (in-memory coordinator: sequence checks, persistence,
//	    sync modes, domain routing)
//	  → AggregateDispatchHandler (real engine adapter, real error mapping)
//	  → AggregateDispatch table (real engine) → typed handlers
//
// The former MockAggregateRouter/MockEventRouter (substring matching,
// shadow semantics) are deleted. Error assertions use coded ClientErrors
// extracted from wire ErrorInfo — never message substrings.

import (
	"context"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	angzarr "github.com/benjaminabbitt/angzarr/client/go"
	pb "github.com/benjaminabbitt/angzarr/client/go/proto/angzarr_client/proto/angzarr/v1"
	"github.com/cucumber/godog"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
)

const (
	fqCreateOrder = "test.CreateOrder"
	fqAddItem     = "test.AddItem"
	fqMultiEvent  = "test.MultiEvent"
	fqOrderEvent  = "test.OrderCreated"
	fieldCustomer = "customer"
	domainUnknown = "unknown domain"
)

// orderState is the trivial aggregate state behind the test backend.
type orderState struct{}

// testBackend is an in-memory coordinator implementing
// pb.CommandHandlerCoordinatorServiceClient: it enforces optimistic
// concurrency, persists accepted events, propagates correlation IDs, and
// simulates sync modes. Dispatch and error mapping are the REAL engine
// adapter's.
type testBackend struct {
	mu      sync.Mutex
	store   map[string]*pb.EventBook
	domains map[string]bool
	adapter *angzarr.AggregateDispatchHandler[*orderState]

	unavailable bool
	slow        bool

	projectorsConfigured bool
	sagasConfigured      bool
	projectorRuns        int
	sagaCompleted        bool
}

func newTestBackend() *testBackend {
	table := angzarr.NewAggregateDispatch(
		"orders", "orders",
		angzarr.NewRebuilder(func() *orderState { return &orderState{} }),
	)
	// CreateOrder/AddItem: one OrderCreated-style event; empty data is a
	// missing required field (coded rejection naming the field).
	emitOne := func(cmdAny *anypb.Any, _ *orderState, _ angzarr.CommandContext) (*pb.EventBook, error) {
		carrier := &pb.Projection{}
		if err := proto.Unmarshal(cmdAny.Value, carrier); err != nil {
			return nil, angzarr.AnyDecodeError(cmdAny.TypeUrl, err)
		}
		if carrier.Projector == "" {
			return nil, angzarr.NewInvalidArgumentRejectionWithCode(
				angzarr.CodeValueEmpty, "required field missing",
				map[string]string{"field": fieldCustomer})
		}
		return &pb.EventBook{Pages: []*pb.EventPage{
			{Payload: &pb.EventPage_Event{Event: &anypb.Any{TypeUrl: angzarr.TypeURLPrefix + fqOrderEvent}}},
		}}, nil
	}
	table.OnCommand(fqCreateOrder, emitOne).OnCommand(fqAddItem, emitOne)
	table.OnCommand(fqMultiEvent, func(cmdAny *anypb.Any, _ *orderState, _ angzarr.CommandContext) (*pb.EventBook, error) {
		carrier := &pb.Projection{}
		if err := proto.Unmarshal(cmdAny.Value, carrier); err != nil {
			return nil, angzarr.AnyDecodeError(cmdAny.TypeUrl, err)
		}
		book := &pb.EventBook{}
		for i := uint32(0); i < carrier.Sequence; i++ {
			book.Pages = append(book.Pages, &pb.EventPage{
				Payload: &pb.EventPage_Event{Event: &anypb.Any{TypeUrl: angzarr.TypeURLPrefix + fqOrderEvent}},
			})
		}
		return book, nil
	})

	return &testBackend{
		store:   make(map[string]*pb.EventBook),
		domains: map[string]bool{"orders": true},
		adapter: angzarr.NewAggregateDispatchHandler(table),
	}
}

func bookKey(domain string, root []byte) string {
	return domain + "|" + hex.EncodeToString(root)
}

// seed installs prior history for an aggregate.
func (b *testBackend) seed(domain string, root []byte, seq uint32) {
	book := &pb.EventBook{
		Cover:        &pb.Cover{Domain: domain, Root: &pb.UUID{Value: root}},
		NextSequence: seq,
	}
	for i := uint32(0); i < seq; i++ {
		book.Pages = append(book.Pages, &pb.EventPage{
			Header:  &pb.PageHeader{SequenceType: &pb.PageHeader_Sequence{Sequence: i}},
			Payload: &pb.EventPage_Event{Event: &anypb.Any{TypeUrl: angzarr.TypeURLPrefix + fqOrderEvent}},
		})
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.store[bookKey(domain, root)] = book
}

func (b *testBackend) recorded(domain string, root []byte) *pb.EventBook {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.store[bookKey(domain, root)]
}

// HandleCommand is the coordinator surface: availability, domain routing,
// optimistic concurrency, dispatch, persistence, sync modes.
func (b *testBackend) HandleCommand(ctx context.Context, req *pb.CommandRequest, _ ...grpc.CallOption) (*pb.CommandResponse, error) {
	if b.unavailable {
		return nil, status.Error(codes.Unavailable, "connection refused")
	}
	if b.slow {
		<-ctx.Done()
		return nil, status.FromContextError(ctx.Err()).Err()
	}

	cmd := req.GetCommand()
	cover := cmd.GetCover()
	if !b.domains[cover.GetDomain()] {
		return nil, status.Error(codes.NotFound, domainUnknown)
	}

	b.mu.Lock()
	key := bookKey(cover.GetDomain(), cover.GetRoot().GetValue())
	prior := b.store[key]
	if prior == nil {
		prior = &pb.EventBook{Cover: cover}
	}
	// Optimistic concurrency: the command's stamped sequence must equal
	// the aggregate's next sequence.
	cmdSeq := uint32(0)
	if len(cmd.Pages) > 0 {
		cmdSeq = cmd.Pages[0].GetHeader().GetSequence()
	}
	if cmdSeq != prior.NextSequence {
		b.mu.Unlock()
		return nil, status.Error(codes.FailedPrecondition, "sequence mismatch: aggregate has moved on")
	}
	b.mu.Unlock()

	// Dispatch through the REAL engine adapter (real error mapping).
	resp, err := b.adapter.Handle(ctx, &pb.ContextualCommand{Command: cmd, Events: prior})
	if err != nil {
		return nil, err
	}
	emitted := resp.GetEvents()
	if emitted == nil {
		emitted = &pb.EventBook{}
	}

	// Persist atomically: stamp consecutive sequences, propagate the
	// command's correlation ID onto the recorded book.
	b.mu.Lock()
	next := prior.NextSequence
	for _, page := range emitted.Pages {
		page.Header = &pb.PageHeader{SequenceType: &pb.PageHeader_Sequence{Sequence: next}}
		next++
	}
	stored := b.store[key]
	if stored == nil {
		stored = &pb.EventBook{Cover: &pb.Cover{
			Domain:        cover.GetDomain(),
			Root:          cover.GetRoot(),
			CorrelationId: cover.GetCorrelationId(),
		}}
	} else if cover.GetCorrelationId() != "" {
		stored.Cover.CorrelationId = cover.GetCorrelationId()
	}
	stored.Pages = append(stored.Pages, emitted.Pages...)
	stored.NextSequence = next
	b.store[key] = stored
	b.mu.Unlock()

	out := &pb.CommandResponse{Events: &pb.EventBook{
		Cover:        stored.Cover,
		Pages:        emitted.Pages,
		NextSequence: next,
	}}

	// Sync modes: ASYNC returns immediately; SIMPLE waits for projectors
	// (results included); CASCADE additionally completes the saga chain.
	switch req.GetSyncMode() {
	case pb.SyncMode_SYNC_MODE_SIMPLE:
		if b.projectorsConfigured {
			b.projectorRuns++
			out.Projections = []*pb.Projection{{Projector: "test-projector"}}
		}
	case pb.SyncMode_SYNC_MODE_CASCADE:
		if b.projectorsConfigured {
			b.projectorRuns++
			out.Projections = []*pb.Projection{{Projector: "test-projector"}}
		}
		if b.sagasConfigured {
			b.sagaCompleted = true
		}
	}
	return out, nil
}

func (b *testBackend) HandleEvent(ctx context.Context, in *pb.EventRequest, _ ...grpc.CallOption) (*pb.FactInjectionResponse, error) {
	return &pb.FactInjectionResponse{}, nil
}

func (b *testBackend) HandleSyncSpeculative(ctx context.Context, in *pb.SpeculateCommandHandlerRequest, _ ...grpc.CallOption) (*pb.CommandResponse, error) {
	return &pb.CommandResponse{}, nil
}

func (b *testBackend) HandleCompensation(ctx context.Context, in *pb.CommandRequest, _ ...grpc.CallOption) (*pb.BusinessResponse, error) {
	return &pb.BusinessResponse{}, nil
}

// AggregateClientContext holds per-scenario state.
type AggregateClientContext struct {
	backend *testBackend
	client  *angzarr.CommandHandlerClient

	domain string
	root   []byte

	lastResp  *pb.CommandResponse
	lastErr   error
	otherErr  error // second concurrent command
	lookedUp  uint32
	timeoutMS int
}

func newAggregateClientContext() *AggregateClientContext {
	return &AggregateClientContext{}
}

func (c *AggregateClientContext) ensureBackend() {
	if c.backend == nil {
		c.backend = newTestBackend()
		c.client = angzarr.CommandHandlerClientFromService(c.backend)
	}
}

func payloadFor(data string, count uint32) *anypb.Any {
	value, _ := proto.Marshal(&pb.Projection{Projector: data, Sequence: count})
	return &anypb.Any{Value: value} // TypeUrl set by command builder
}

func (c *AggregateClientContext) command(cmdType, data string, seq, count uint32, correlation string) *pb.CommandBook {
	payload := payloadFor(data, count)
	payload.TypeUrl = angzarr.TypeURLPrefix + cmdType
	return &pb.CommandBook{
		Cover: &pb.Cover{Domain: c.domain, Root: &pb.UUID{Value: c.root}, CorrelationId: correlation},
		Pages: []*pb.CommandPage{
			{
				Header:  &pb.PageHeader{SequenceType: &pb.PageHeader_Sequence{Sequence: seq}},
				Payload: &pb.CommandPage_Command{Command: payload},
			},
		},
	}
}

func (c *AggregateClientContext) send(cmd *pb.CommandBook, mode pb.SyncMode) error {
	ctx := context.Background()
	if c.timeoutMS > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(c.timeoutMS)*time.Millisecond)
		defer cancel()
	}
	c.lastResp, c.lastErr = c.client.HandleCommand(ctx, &pb.CommandRequest{Command: cmd, SyncMode: mode})
	return nil
}

func (c *AggregateClientContext) clientError() *angzarr.ClientError {
	return angzarr.AsClientError(c.lastErr)
}

// InitAggregateSteps registers aggregate_client.feature step definitions.
func InitAggregateSteps(ctx *godog.ScenarioContext) {
	c := newAggregateClientContext()

	// --- Background / givens ---
	ctx.Step(`^a client connected to the test backend$`, func() error {
		c.ensureBackend()
		return nil
	})
	ctx.Step(`^a new aggregate root in domain "([^"]*)"$`, func(domain string) error {
		c.ensureBackend()
		c.domain, c.root = domain, []byte("fresh-root")
		return nil
	})
	ctx.Step(`^an aggregate "([^"]*)" with root "([^"]*)" at sequence (\d+)$`, func(domain, root string, seq int) error {
		c.ensureBackend()
		c.domain, c.root = domain, []byte(root)
		c.backend.seed(domain, c.root, uint32(seq))
		return nil
	})
	ctx.Step(`^an aggregate "([^"]*)" with root "([^"]*)"$`, func(domain, root string) error {
		c.ensureBackend()
		c.domain, c.root = domain, []byte(root)
		c.backend.seed(domain, c.root, 0)
		return nil
	})
	ctx.Step(`^no aggregate exists for domain "([^"]*)" root "([^"]*)"$`, func(domain, root string) error {
		c.ensureBackend()
		c.domain, c.root = domain, []byte(root)
		return nil
	})
	ctx.Step(`^projectors are configured for "([^"]*)" domain$`, func(string) error {
		c.backend.projectorsConfigured = true
		return nil
	})
	ctx.Step(`^sagas are configured for "([^"]*)" domain$`, func(string) error {
		c.backend.sagasConfigured = true
		return nil
	})
	ctx.Step(`^the aggregate service is unavailable$`, func() error {
		c.ensureBackend()
		c.domain, c.root = "orders", []byte("any")
		c.backend.unavailable = true
		return nil
	})
	ctx.Step(`^the aggregate service does not respond in time$`, func() error {
		c.ensureBackend()
		c.domain, c.root = "orders", []byte("any")
		c.backend.slow = true
		return nil
	})

	// --- Whens: sending commands ---
	ctx.Step(`^I send a "([^"]*)" command with data "([^"]*)"$`, func(cmdType, data string) error {
		return c.send(c.command("test."+cmdType, data, 0, 0, ""), pb.SyncMode_SYNC_MODE_ASYNC)
	})
	ctx.Step(`^I send an? "([^"]*)" command at sequence (\d+)$`, func(cmdType string, seq int) error {
		return c.send(c.command("test."+cmdType, "data", uint32(seq), 0, ""), pb.SyncMode_SYNC_MODE_ASYNC)
	})
	ctx.Step(`^I send a command at sequence (\d+)$`, func(seq int) error {
		return c.send(c.command(fqAddItem, "data", uint32(seq), 0, ""), pb.SyncMode_SYNC_MODE_ASYNC)
	})
	ctx.Step(`^I send a command tagged with correlation ID "([^"]*)"$`, func(cid string) error {
		return c.send(c.command(fqCreateOrder, "data", 0, 0, cid), pb.SyncMode_SYNC_MODE_ASYNC)
	})
	ctx.Step(`^two commands are sent concurrently at sequence (\d+)$`, func(seq int) error {
		_ = c.send(c.command(fqAddItem, "data", uint32(seq), 0, ""), pb.SyncMode_SYNC_MODE_ASYNC)
		first := c.lastErr
		_ = c.send(c.command(fqAddItem, "data", uint32(seq), 0, ""), pb.SyncMode_SYNC_MODE_ASYNC)
		c.otherErr = c.lastErr
		c.lastErr = first
		return nil
	})
	ctx.Step(`^I look up the current sequence for "([^"]*)" root "([^"]*)"$`, func(domain, root string) error {
		book := c.backend.recorded(domain, []byte(root))
		if book == nil {
			return fmt.Errorf("aggregate not found")
		}
		c.lookedUp = book.NextSequence
		return nil
	})
	ctx.Step(`^I retry the command at that sequence$`, func() error {
		return c.send(c.command(fqAddItem, "data", c.lookedUp, 0, ""), pb.SyncMode_SYNC_MODE_ASYNC)
	})
	ctx.Step(`^I send a command without waiting for downstream work$`, func() error {
		return c.send(c.command(fqCreateOrder, "data", 0, 0, ""), pb.SyncMode_SYNC_MODE_ASYNC)
	})
	ctx.Step(`^I send a command and wait for projectors$`, func() error {
		return c.send(c.command(fqCreateOrder, "data", 0, 0, ""), pb.SyncMode_SYNC_MODE_SIMPLE)
	})
	ctx.Step(`^I send a command and wait for downstream sagas$`, func() error {
		return c.send(c.command(fqCreateOrder, "data", 0, 0, ""), pb.SyncMode_SYNC_MODE_CASCADE)
	})
	ctx.Step(`^I send a command with a malformed payload$`, func() error {
		cmd := c.command(fqCreateOrder, "", 0, 0, "")
		cmd.Pages[0].GetCommand().Value = []byte{0xFF, 0xFF, 0xFF, 0xFF}
		return c.send(cmd, pb.SyncMode_SYNC_MODE_ASYNC)
	})
	ctx.Step(`^I send a command missing required fields$`, func() error {
		return c.send(c.command(fqCreateOrder, "", 0, 0, ""), pb.SyncMode_SYNC_MODE_ASYNC)
	})
	ctx.Step(`^I send a command to domain "([^"]*)"$`, func(domain string) error {
		c.domain, c.root = domain, []byte("any")
		return c.send(c.command(fqCreateOrder, "data", 0, 0, ""), pb.SyncMode_SYNC_MODE_ASYNC)
	})
	ctx.Step(`^I send a command that produces (\d+) events$`, func(n int) error {
		return c.send(c.command(fqMultiEvent, "data", 0, uint32(n), ""), pb.SyncMode_SYNC_MODE_ASYNC)
	})
	ctx.Step(`^I attempt to send a command$`, func() error {
		return c.send(c.command(fqCreateOrder, "data", 0, 0, ""), pb.SyncMode_SYNC_MODE_ASYNC)
	})
	ctx.Step(`^I send a command with a short timeout$`, func() error {
		c.timeoutMS = 50
		return c.send(c.command(fqCreateOrder, "data", 0, 0, ""), pb.SyncMode_SYNC_MODE_ASYNC)
	})
	ctx.Step(`^I send a "([^"]*)" command for root "([^"]*)" at sequence (\d+)$`, func(cmdType, root string, seq int) error {
		c.root = []byte(root)
		return c.send(c.command("test."+cmdType, "data", uint32(seq), 0, ""), pb.SyncMode_SYNC_MODE_ASYNC)
	})
	ctx.Step(`^I read back the events for "([^"]*)" root "([^"]*)"$`, func(domain, root string) error {
		if c.backend.recorded(domain, []byte(root)) == nil {
			return fmt.Errorf("no events recorded")
		}
		return nil
	})

	// --- Thens: acceptance ---
	ctx.Step(`^the command is accepted$`, func() error {
		if c.lastErr != nil {
			return fmt.Errorf("command refused: %v", c.lastErr)
		}
		return nil
	})
	ctx.Step(`^a single "([^"]*)" event is recorded$`, func(eventType string) error {
		book := c.backend.recorded(c.domain, c.root)
		if book == nil || len(book.Pages) != 1 {
			return fmt.Errorf("recorded %d events, want 1", len(book.GetPages()))
		}
		want := angzarr.TypeURLPrefix + "test." + eventType
		if got := book.Pages[0].GetEvent().GetTypeUrl(); got != want {
			return fmt.Errorf("recorded %q, want %q", got, want)
		}
		return nil
	})
	ctx.Step(`^the new events continue the history from sequence (\d+)$`, func(seq int) error {
		pages := c.lastResp.GetEvents().GetPages()
		if len(pages) == 0 {
			return fmt.Errorf("no new events")
		}
		if got := pages[0].GetHeader().GetSequence(); got != uint32(seq) {
			return fmt.Errorf("first new event at sequence %d, want %d", got, seq)
		}
		return nil
	})
	ctx.Step(`^the resulting events carry correlation ID "([^"]*)"$`, func(cid string) error {
		if got := c.lastResp.GetEvents().GetCover().GetCorrelationId(); got != cid {
			return fmt.Errorf("correlation = %q, want %q", got, cid)
		}
		return nil
	})

	// --- Thens: refusals (coded, never substrings) ---
	ctx.Step(`^the command is refused because the aggregate has moved on$`, func() error {
		clientErr := c.clientError()
		if clientErr == nil || !clientErr.IsPreconditionFailed() {
			return fmt.Errorf("want FAILED_PRECONDITION, got %v", c.lastErr)
		}
		return nil
	})
	ctx.Step(`^one command is accepted$`, func() error {
		if c.lastErr != nil && c.otherErr != nil {
			return fmt.Errorf("both refused: %v / %v", c.lastErr, c.otherErr)
		}
		return nil
	})
	ctx.Step(`^the other is refused because the aggregate has moved on$`, func() error {
		refused := angzarr.AsClientError(c.otherErr)
		if refused == nil || !refused.IsPreconditionFailed() {
			return fmt.Errorf("want FAILED_PRECONDITION on second write, got %v", c.otherErr)
		}
		return nil
	})
	ctx.Step(`^the command is refused as invalid$`, func() error {
		clientErr := c.clientError()
		if clientErr == nil || !clientErr.IsInvalidArgument() {
			return fmt.Errorf("want InvalidArgument, got %v", c.lastErr)
		}
		return nil
	})
	ctx.Step(`^the refusal names the missing field$`, func() error {
		clientErr := c.clientError()
		if clientErr == nil || clientErr.Extras["field"] != fieldCustomer {
			return fmt.Errorf("refusal extras = %v, want field=%s (via wire ErrorInfo)", clientErr.Extras, fieldCustomer)
		}
		return nil
	})
	ctx.Step(`^the command is refused because the domain is unknown$`, func() error {
		clientErr := c.clientError()
		if clientErr == nil || !clientErr.IsNotFound() {
			return fmt.Errorf("want NotFound for unknown domain, got %v", c.lastErr)
		}
		return nil
	})

	// --- Thens: sync modes ---
	ctx.Step(`^the response returns before any projectors have caught up$`, func() error {
		if c.lastErr != nil {
			return c.lastErr
		}
		if len(c.lastResp.GetProjections()) != 0 || c.backend.projectorRuns != 0 {
			return fmt.Errorf("ASYNC response waited for projectors")
		}
		return nil
	})
	ctx.Step(`^the response reflects the projectors having processed the event$`, func() error {
		if len(c.lastResp.GetProjections()) == 0 {
			return fmt.Errorf("no projector results in SIMPLE response")
		}
		return nil
	})
	ctx.Step(`^the response reflects the downstream sagas having completed$`, func() error {
		if !c.backend.sagaCompleted {
			return fmt.Errorf("saga chain did not complete before response")
		}
		return nil
	})

	// --- Thens: multi-event ---
	ctx.Step(`^(\d+) events are recorded$`, func(n int) error {
		book := c.backend.recorded(c.domain, c.root)
		if got := len(book.GetPages()); got != n {
			return fmt.Errorf("recorded %d events, want %d", got, n)
		}
		return nil
	})
	ctx.Step(`^the events occupy consecutive sequences starting at (\d+)$`, func(start int) error {
		book := c.backend.recorded(c.domain, c.root)
		for i, page := range book.GetPages() {
			if got := page.GetHeader().GetSequence(); got != uint32(start+i) {
				return fmt.Errorf("event %d at sequence %d, want %d", i, got, start+i)
			}
		}
		return nil
	})
	ctx.Step(`^either all (\d+) events are present or none of them are$`, func(n int) error {
		book := c.backend.recorded(c.domain, c.root)
		if got := len(book.GetPages()); got != 0 && got != n {
			return fmt.Errorf("partial write: %d of %d events", got, n)
		}
		return nil
	})

	// --- Thens: connection handling ---
	ctx.Step(`^the call fails because the service cannot be reached$`, func() error {
		clientErr := c.clientError()
		if clientErr == nil || clientErr.GRPCCode() != codes.Unavailable {
			return fmt.Errorf("want Unavailable, got %v", c.lastErr)
		}
		return nil
	})
	ctx.Step(`^the call fails because the deadline was exceeded$`, func() error {
		clientErr := c.clientError()
		if clientErr == nil || clientErr.GRPCCode() != codes.DeadlineExceeded {
			return fmt.Errorf("want DeadlineExceeded, got %v", c.lastErr)
		}
		return nil
	})

	// --- Thens: creation ---
	ctx.Step(`^the aggregate now exists with one event$`, func() error {
		book := c.backend.recorded(c.domain, c.root)
		if book == nil || len(book.Pages) != 1 {
			return fmt.Errorf("aggregate has %d events, want 1", len(book.GetPages()))
		}
		return nil
	})
}
