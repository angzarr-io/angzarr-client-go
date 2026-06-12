package features

// router.go — step bindings for client/router.feature, driven through the
// REAL engine dispatch tables (AggregateDispatch, SagaDispatch,
// ProjectorDispatch, ProcessManagerDispatch, Rebuilder). No simulated
// dispatch: every Then observes the output of an actual Dispatch/Rebuild.
//
// Error assertions use coded ClientErrors. The guard/validate strings
// asserted in the G/V/C scenarios are harness-authored fixture text (the
// scenario defines them), not SDK messages.

import (
	"fmt"
	"strings"

	angzarr "github.com/benjaminabbitt/angzarr/client/go"
	pb "github.com/benjaminabbitt/angzarr/client/go/proto/angzarr_client/proto/angzarr/v1"
	"github.com/cucumber/godog"
	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type routerScenarioCtx struct {
	// shared observation surface
	invoked  map[string]int
	lastResp *pb.BusinessResponse
	lastErr  error

	// aggregate
	rebuilder     *angzarr.Rebuilder[*routerOrderState]
	aggTable      *angzarr.AggregateDispatch[*routerOrderState]
	priorEvents   *pb.EventBook
	stateCaptured bool
	capturedState routerOrderState
	capturedSeq   uint32

	// saga
	sagaTable     *angzarr.SagaDispatch
	sagaCommands  []*pb.CommandBook
	sagaResp      *pb.SagaResponse
	rejectionBook *pb.EventBook
	rejectedSeen  []string
	execCount     int
	perDispatch   []int
	destDomain    string

	// projector
	projTable  *angzarr.ProjectorDispatch[*routerProjAccum]
	projOrder  []uint32
	projCount  int
	projection *pb.Projection

	// process manager
	pmTable *angzarr.ProcessManagerDispatch[*routerPMState]

	// state building
	statePages []*pb.EventPage
	builtState routerOrderState
	stateBuilt bool

	// typed decode
	typedReceived *pb.Snapshot
}

// routerOrderState is rebuilt by the real engine Rebuilder from real proto
// payloads. pb.Cover stands in for "OrderCreated", pb.Snapshot for "ItemAdded".
type routerOrderState struct {
	exists  bool
	items   int
	applied int
}

type routerPMState struct{}

// routerProjAccum is the per-delivery projector instance.
type routerProjAccum struct{}

func newRouterScenarioCtx() *routerScenarioCtx {
	return &routerScenarioCtx{
		invoked:    map[string]int{},
		destDomain: "inventory",
	}
}

func (c *routerScenarioCtx) newRebuilder() *angzarr.Rebuilder[*routerOrderState] {
	return angzarr.NewRebuilder(func() *routerOrderState { return &routerOrderState{} }).
		Apply(routerProtoFullName(&pb.Cover{}), func(s *routerOrderState, payload *anypb.Any) error { // "OrderCreated"
			if err := proto.Unmarshal(payload.Value, &pb.Cover{}); err != nil {
				return err
			}
			s.exists = true
			s.applied++
			return nil
		}).
		Apply(routerProtoFullName(&pb.Snapshot{}), func(s *routerOrderState, payload *anypb.Any) error { // "ItemAdded"
			if err := proto.Unmarshal(payload.Value, &pb.Snapshot{}); err != nil {
				return err
			}
			s.items++
			s.applied++
			return nil
		})
}

// --- builders -------------------------------------------------------------

func routerCover(domain string) *pb.Cover {
	root := uuid.New()
	return &pb.Cover{Domain: domain, Root: &pb.UUID{Value: root[:]}}
}

func routerEventPage(seq uint32, payload *anypb.Any) *pb.EventPage {
	return &pb.EventPage{
		Header:    &pb.PageHeader{SequenceType: &pb.PageHeader_Sequence{Sequence: seq}},
		CreatedAt: timestamppb.Now(),
		Payload:   &pb.EventPage_Event{Event: payload},
	}
}

func routerBareAny(fullName string) *anypb.Any {
	return &anypb.Any{TypeUrl: angzarr.TypeURLPrefix + fullName}
}

func routerEventBook(domain string, pages []*pb.EventPage) *pb.EventBook {
	next := uint32(0)
	if n := len(pages); n > 0 {
		next = pages[n-1].Header.GetSequence() + 1
	}
	return &pb.EventBook{Cover: routerCover(domain), Pages: pages, NextSequence: next}
}

func routerCommandBook(domain string, payload *anypb.Any) *pb.CommandBook {
	return &pb.CommandBook{
		Cover: routerCover(domain),
		Pages: []*pb.CommandPage{{
			Header:        &pb.PageHeader{SequenceType: &pb.PageHeader_Sequence{Sequence: 0}},
			MergeStrategy: pb.MergeStrategy_MERGE_COMMUTATIVE,
			Payload:       &pb.CommandPage_Command{Command: payload},
		}},
	}
}

func routerFinalToken(typeURL string) string {
	s := typeURL
	if i := strings.LastIndex(s, "/"); i >= 0 {
		s = s[i+1:]
	}
	if i := strings.LastIndex(s, "."); i >= 0 {
		s = s[i+1:]
	}
	return s
}

func routerProtoFullName(m proto.Message) string {
	return string(m.ProtoReflect().Descriptor().FullName())
}

// recordingThunk records its invocation and emits `emit` headerless events
// (the engine's fill-only stamping assigns their sequences).
func (c *routerScenarioCtx) recordingThunk(name string, emit int) angzarr.AggregateCommandThunk[*routerOrderState] {
	return func(cmd *anypb.Any, state *routerOrderState, cctx angzarr.CommandContext) (*pb.EventBook, error) {
		c.invoked[name]++
		c.capturedState = *state
		c.capturedSeq = cctx.NextSequence
		c.stateCaptured = true
		pages := make([]*pb.EventPage, emit)
		for i := 0; i < emit; i++ {
			pages[i] = &pb.EventPage{Payload: &pb.EventPage_Event{Event: routerBareAny("test.Event")}}
		}
		return &pb.EventBook{Pages: pages}, nil
	}
}

func (c *routerScenarioCtx) dispatchCommand(payload *anypb.Any) error {
	if c.aggTable == nil {
		return fmt.Errorf("no aggregate table configured")
	}
	c.lastResp, c.lastErr = c.aggTable.Dispatch(&pb.ContextualCommand{
		Command: routerCommandBook("orders", payload),
		Events:  c.priorEvents,
	})
	return nil
}

// --- givens ----------------------------------------------------------------

func (c *routerScenarioCtx) givenAggRouterHandlers(names ...string) error {
	c.rebuilder = c.newRebuilder()
	c.aggTable = angzarr.NewAggregateDispatch("orders", "orders", c.rebuilder)
	for _, n := range names {
		c.aggTable.OnCommand("test."+n, c.recordingThunk(n, 1))
	}
	return nil
}

func (c *routerScenarioCtx) givenAggRouterTwo(h1, h2 string) error {
	return c.givenAggRouterHandlers(h1, h2)
}

func (c *routerScenarioCtx) givenAggRouterOne(h1 string) error {
	return c.givenAggRouterHandlers(h1)
}

func (c *routerScenarioCtx) givenAggRouter() error {
	return c.givenAggRouterHandlers("CreateOrder")
}

func (c *routerScenarioCtx) givenAggregateWithEvents() error {
	created, err := anypb.New(&pb.Cover{Domain: "stand-in"})
	if err != nil {
		return err
	}
	added, err := anypb.New(&pb.Snapshot{Sequence: 1})
	if err != nil {
		return err
	}
	c.priorEvents = routerEventBook("orders", []*pb.EventPage{
		routerEventPage(0, created),
		routerEventPage(1, added),
	})
	return nil
}

func (c *routerScenarioCtx) givenSagaRouterTwo(h1, h2 string) error {
	c.sagaTable = angzarr.NewSagaDispatch("saga-test", "orders", "inventory")
	for _, n := range []string{h1, h2} {
		name := n
		c.sagaTable.OnEvent("test."+name, func(*anypb.Any, *angzarr.Destinations) ([]*pb.CommandBook, []*pb.EventBook, error) {
			c.invoked[name]++
			return nil, nil, nil
		})
	}
	return nil
}

func (c *routerScenarioCtx) givenSagaRouter() error {
	c.sagaTable = angzarr.NewSagaDispatch("saga-test", "orders", "inventory")
	c.sagaTable.OnEvent("test.OrderCreated", func(_ *anypb.Any, d *angzarr.Destinations) ([]*pb.CommandBook, []*pb.EventBook, error) {
		c.execCount++
		cmd := routerCommandBook(c.destDomain, routerBareAny("test.ReserveStock"))
		if d.Has(c.destDomain) {
			if err := d.StampCommand(cmd, c.destDomain); err != nil {
				return nil, nil, err
			}
		}
		c.perDispatch = append(c.perDispatch, 1)
		return []*pb.CommandBook{cmd}, nil, nil
	})
	return nil
}

func (c *routerScenarioCtx) givenSagaRouterWithRejectedCommand() error {
	c.sagaTable = angzarr.NewSagaDispatch("saga-orders-inventory", "orders", "inventory").
		OnEvent("test.OrderCreated", func(*anypb.Any, *angzarr.Destinations) ([]*pb.CommandBook, []*pb.EventBook, error) {
			c.execCount++
			return nil, nil, nil
		}).
		OnRejected("test.ReserveStock", func(n *pb.Notification, rejection *pb.RejectionNotification) ([]*pb.EventBook, error) {
			domain := rejection.GetRejectedCommand().GetCover().GetDomain()
			cmdType := ""
			if pages := rejection.GetRejectedCommand().GetPages(); len(pages) > 0 {
				cmdType = angzarr.TypeNameFromURL(pages[0].GetCommand().GetTypeUrl())
			}
			c.rejectedSeen = append(c.rejectedSeen, domain+"/"+cmdType)
			comp, err := anypb.New(&pb.Cover{Domain: "compensated"})
			if err != nil {
				return nil, err
			}
			return []*pb.EventBook{routerEventBook("orders", []*pb.EventPage{routerEventPage(0, comp)})}, nil
		})

	rejection := &pb.RejectionNotification{
		RejectedCommand: routerCommandBook("inventory", routerBareAny("test.ReserveStock")),
	}
	payload, err := anypb.New(rejection)
	if err != nil {
		return err
	}
	notifAny, err := anypb.New(&pb.Notification{Payload: payload})
	if err != nil {
		return err
	}
	c.rejectionBook = routerEventBook("orders", []*pb.EventPage{routerEventPage(0, notifAny)})
	return nil
}

func (c *routerScenarioCtx) projThunk(name string) angzarr.ProjectorEventThunk[*routerProjAccum] {
	return func(_ *routerProjAccum, eventAny *anypb.Any) error {
		c.invoked[routerFinalToken(eventAny.TypeUrl)]++
		// Order is carried in the payload (Snapshot.Sequence) when the
		// scenario builds distinguishable events; bare events count only.
		snap := &pb.Snapshot{}
		if err := proto.Unmarshal(eventAny.Value, snap); err == nil && len(eventAny.Value) > 0 {
			c.projOrder = append(c.projOrder, snap.Sequence)
		} else {
			c.projOrder = append(c.projOrder, uint32(c.projCount))
		}
		c.projCount++
		return nil
	}
}

func (c *routerScenarioCtx) givenProjectorRouterHandlers(string) error {
	return c.givenProjectorRouter()
}

func (c *routerScenarioCtx) givenProjectorRouter() error {
	c.projTable = angzarr.NewProjectorDispatch("prj-test", func() *routerProjAccum { return &routerProjAccum{} }).
		ForDomains("orders").
		OnEvent("test.OrderCreated", c.projThunk("OrderCreated")).
		OnEvent("test.Event", c.projThunk("Event")).
		Finish(func(_ *routerProjAccum, events *pb.EventBook) (*pb.Projection, error) {
			var last uint32
			for _, page := range events.Pages {
				if page.GetEvent() != nil {
					last = page.GetHeader().GetSequence()
				}
			}
			return &pb.Projection{Cover: events.Cover, Projector: "prj-test", Sequence: last}, nil
		})
	return nil
}

func (c *routerScenarioCtx) givenPMRouterHandlers(h1, h2 string) error {
	c.pmTable = angzarr.NewProcessManagerDispatch("pmg-test", "pmflow",
		angzarr.NewRebuilder(func() *routerPMState { return &routerPMState{} }))
	for _, domain := range []string{"orders", "inventory"} {
		for _, eventType := range []string{"test.OrderCreated", "test.InventoryReserved"} {
			c.pmTable.OnEvent(domain, eventType, func(eventAny *anypb.Any, _ *routerPMState, _ *angzarr.Destinations) (*pb.ProcessManagerHandleResponse, error) {
				c.invoked[routerFinalToken(eventAny.TypeUrl)]++
				return &pb.ProcessManagerHandleResponse{}, nil
			})
		}
	}
	return nil
}

func (c *routerScenarioCtx) givenRouter() error {
	return c.givenAggRouterHandlers()
}

func (c *routerScenarioCtx) givenTypedHandlerRouter() error {
	c.sagaTable = angzarr.NewSagaDispatch("typed-test", "orders", "inventory").
		OnEvent(routerProtoFullName(&pb.Snapshot{}), func(eventAny *anypb.Any, _ *angzarr.Destinations) ([]*pb.CommandBook, []*pb.EventBook, error) {
			snap := &pb.Snapshot{}
			if err := eventAny.UnmarshalTo(snap); err != nil {
				return nil, nil, err
			}
			c.typedReceived = snap
			return nil, nil, nil
		})
	return nil
}

// gvcRouter wires the guard/validate/compute pattern through the real
// engine table using pb.Snapshot as the command payload type.
func (c *routerScenarioCtx) gvcRouter() error {
	c.rebuilder = c.newRebuilder()
	c.aggTable = angzarr.NewAggregateDispatch("orders", "orders", c.rebuilder)
	c.aggTable.OnCommand(routerProtoFullName(&pb.Snapshot{}), func(cmd *anypb.Any, state *routerOrderState, cctx angzarr.CommandContext) (*pb.EventBook, error) {
		// guard: preconditions on state
		if !state.exists {
			return nil, fmt.Errorf("guard rejected: aggregate does not exist")
		}
		// validate: command contents
		snap := &pb.Snapshot{}
		if err := cmd.UnmarshalTo(snap); err != nil {
			return nil, fmt.Errorf("validate rejected: %w", err)
		}
		if snap.Sequence == 0 {
			return nil, fmt.Errorf("validate rejected: sequence must be positive")
		}
		// compute: pure event production
		evt, err := anypb.New(&pb.Snapshot{Sequence: snap.Sequence})
		if err != nil {
			return nil, err
		}
		return &pb.EventBook{Pages: []*pb.EventPage{{Payload: &pb.EventPage_Event{Event: evt}}}}, nil
	})
	return nil
}

func (c *routerScenarioCtx) givenGuardedAggregate() error {
	c.priorEvents = nil // aggregate does not exist
	return c.gvcRouter()
}

func (c *routerScenarioCtx) givenValidatingAggregate() error {
	if err := c.givenAggregateWithEvents(); err != nil {
		return err
	}
	return c.gvcRouter()
}

// --- whens ------------------------------------------------------------------

func (c *routerScenarioCtx) whenReceiveCommand(cmdType string) error {
	return c.dispatchCommand(routerBareAny("test." + cmdType))
}

func (c *routerScenarioCtx) whenReceiveCommandForAggregate() error {
	return c.whenReceiveCommand("CreateOrder")
}

func (c *routerScenarioCtx) whenHandlerEmits(n int) error {
	c.aggTable = angzarr.NewAggregateDispatch("orders", "orders", c.newRebuilder()).
		OnCommand("test.CreateOrder", c.recordingThunk("CreateOrder", n))
	return c.whenReceiveCommand("CreateOrder")
}

func (c *routerScenarioCtx) whenHandlerReturnsError() error {
	c.aggTable.OnCommand("test.FailingCommand", func(*anypb.Any, *routerOrderState, angzarr.CommandContext) (*pb.EventBook, error) {
		return nil, fmt.Errorf("handler failed: simulated business failure")
	})
	return c.whenReceiveCommand("FailingCommand")
}

func (c *routerScenarioCtx) whenHandlerProducesCommand() error {
	if c.sagaTable == nil {
		if err := c.givenSagaRouter(); err != nil {
			return err
		}
	}
	return c.whenReceiveEvent("OrderCreated")
}

func (c *routerScenarioCtx) whenReceiveEvent(eventType string) error {
	book := routerEventBook("orders", []*pb.EventPage{routerEventPage(0, routerBareAny("test."+eventType))})
	if c.projTable != nil {
		c.projection, c.lastErr = c.projTable.Dispatch(book)
		return nil
	}
	if c.sagaTable == nil {
		return fmt.Errorf("no saga table configured")
	}
	c.sagaResp, c.lastErr = c.sagaTable.Dispatch(book, nil)
	if c.sagaResp != nil {
		c.sagaCommands = c.sagaResp.Commands
	}
	return nil
}

func (c *routerScenarioCtx) whenReceiveEventTriggeringCommandTo(domain string) error {
	c.destDomain = domain
	book := routerEventBook("orders", []*pb.EventPage{routerEventPage(0, routerBareAny("test.OrderCreated"))})
	c.sagaResp, c.lastErr = c.sagaTable.Dispatch(book, map[string]uint32{domain: 7})
	if c.sagaResp != nil {
		c.sagaCommands = c.sagaResp.Commands
	}
	return c.lastErr
}

func (c *routerScenarioCtx) whenRouterProcessesRejection() error {
	c.sagaResp, c.lastErr = c.sagaTable.Dispatch(c.rejectionBook, nil)
	return c.lastErr
}

func (c *routerScenarioCtx) whenProcessTwoEventsSameType() error {
	for i := 0; i < 2; i++ {
		if err := c.whenReceiveEvent("OrderCreated"); err != nil {
			return err
		}
		if c.lastErr != nil {
			return c.lastErr
		}
	}
	return nil
}

func (c *routerScenarioCtx) whenReceiveEventsInBatch(n int) error {
	pages := make([]*pb.EventPage, n)
	for i := 0; i < n; i++ {
		// Distinguishable payloads carry their order for the in-order pin.
		payload := mustAny(&pb.Snapshot{Sequence: uint32(i)})
		payload.TypeUrl = angzarr.TypeURLPrefix + "test.Event"
		pages[i] = routerEventPage(uint32(i), payload)
	}
	c.projection, c.lastErr = c.projTable.Dispatch(routerEventBook("orders", pages))
	return c.lastErr
}

func (c *routerScenarioCtx) whenReceiveEventFromDomain(eventType, domain string) error {
	book := routerEventBook(domain, []*pb.EventPage{routerEventPage(0, routerBareAny("test."+eventType))})
	_, c.lastErr = c.pmTable.Dispatch(book, nil, nil)
	return c.lastErr
}

func (c *routerScenarioCtx) whenRegisterThreeHandlers(t1, t2, t3 string) error {
	for _, n := range []string{t1, t2, t3} {
		c.aggTable.OnCommand("test."+n, c.recordingThunk(n, 1))
	}
	return nil
}

func (c *routerScenarioCtx) whenReceiveEventWithThatType() error {
	payload, err := anypb.New(&pb.Snapshot{Sequence: 42})
	if err != nil {
		return err
	}
	book := routerEventBook("orders", []*pb.EventPage{routerEventPage(0, payload)})
	_, c.lastErr = c.sagaTable.Dispatch(book, nil)
	return c.lastErr
}

func (c *routerScenarioCtx) givenStandardEvents() error {
	created, err := anypb.New(&pb.Cover{Domain: "stand-in"})
	if err != nil {
		return err
	}
	c.statePages = []*pb.EventPage{routerEventPage(0, created)}
	for i := 1; i <= 2; i++ {
		added, err := anypb.New(&pb.Snapshot{Sequence: uint32(i)})
		if err != nil {
			return err
		}
		c.statePages = append(c.statePages, routerEventPage(uint32(i), added))
	}
	return nil
}

func (c *routerScenarioCtx) givenNoEvents() error {
	c.statePages = nil
	return nil
}

func (c *routerScenarioCtx) whenBuildStateFromEvents() error {
	if c.rebuilder == nil {
		c.rebuilder = c.newRebuilder()
	}
	state, _, err := c.rebuilder.Rebuild(&pb.EventBook{Pages: c.statePages})
	if err != nil {
		return err
	}
	c.builtState = *state
	c.stateBuilt = true
	return nil
}

func (c *routerScenarioCtx) whenBuildState() error {
	return c.whenBuildStateFromEvents()
}

func (c *routerScenarioCtx) whenReceiveInvalidPayload() error {
	// A Notification-typed Any with garbage bytes forces the engine's real
	// rejection-decode path to fail with a coded error.
	return c.dispatchCommand(&anypb.Any{
		TypeUrl: angzarr.TypeURLPrefix + routerProtoFullName(&pb.Notification{}),
		Value:   []byte{0xFF, 0xFF, 0xFF, 0xFF},
	})
}

func (c *routerScenarioCtx) whenSendCommandToNonexistentAggregate() error {
	return c.dispatchCommand(mustAny(&pb.Snapshot{Sequence: 9}))
}

func (c *routerScenarioCtx) whenSendCommandWithInvalidData() error {
	return c.dispatchCommand(mustAny(&pb.Snapshot{Sequence: 0}))
}

func (c *routerScenarioCtx) whenGuardAndValidatePass() error {
	return c.dispatchCommand(mustAny(&pb.Snapshot{Sequence: 9}))
}

func mustAny(m proto.Message) *anypb.Any {
	a, err := anypb.New(m)
	if err != nil {
		panic(err)
	}
	return a
}

// --- thens -------------------------------------------------------------------

func (c *routerScenarioCtx) thenHandlerInvoked(name string) error {
	if c.invoked[name] == 0 {
		return fmt.Errorf("expected handler %q to be invoked, invocations: %v", name, c.invoked)
	}
	return nil
}

func (c *routerScenarioCtx) thenHandlerNotInvoked(name string) error {
	if c.invoked[name] != 0 {
		return fmt.Errorf("expected handler %q NOT to be invoked, got %d invocations", name, c.invoked[name])
	}
	return nil
}

func (c *routerScenarioCtx) thenHandlerReceivedFullHistory() error {
	if !c.stateCaptured {
		return fmt.Errorf("handler never received state")
	}
	want := len(c.priorEvents.GetPages())
	if c.capturedState.applied != want {
		return fmt.Errorf("expected state built from %d recorded events, handler saw %d applied", want, c.capturedState.applied)
	}
	return nil
}

func (c *routerScenarioCtx) thenRouterReturnsEvents() error {
	if c.lastErr != nil {
		return fmt.Errorf("dispatch failed: %v", c.lastErr)
	}
	if c.lastResp.GetEvents() == nil || len(c.lastResp.GetEvents().Pages) == 0 {
		return fmt.Errorf("expected returned events, got %v", c.lastResp)
	}
	return nil
}

func (c *routerScenarioCtx) thenEventsConsecutiveFromHistory() error {
	events := c.lastResp.GetEvents()
	if events == nil {
		return fmt.Errorf("no events returned")
	}
	start := angzarr.NextSequence(c.priorEvents)
	for i, page := range events.Pages {
		want := start + uint32(i)
		if got := page.Header.GetSequence(); got != want {
			return fmt.Errorf("event %d: expected sequence %d (continuing history from %d), got %d", i, want, start, got)
		}
	}
	return nil
}

func (c *routerScenarioCtx) thenRouterReturnsError() error {
	if c.lastErr == nil {
		return fmt.Errorf("expected an error, got nil (resp: %v)", c.lastResp)
	}
	return nil
}

func (c *routerScenarioCtx) thenErrorUnknownCommand() error {
	clientErr := angzarr.AsClientError(c.lastErr)
	if clientErr == nil || clientErr.Code != angzarr.CodeNoHandlerRegistered {
		return fmt.Errorf("expected coded %s, got: %v", angzarr.CodeNoHandlerRegistered, c.lastErr)
	}
	return nil
}

func (c *routerScenarioCtx) thenCommandSequencedFor(domain string) error {
	if len(c.sagaCommands) == 0 {
		return fmt.Errorf("no commands emitted")
	}
	cmd := c.sagaCommands[0]
	if cmd.Cover.GetDomain() != domain {
		return fmt.Errorf("command targets %q, expected %q", cmd.Cover.GetDomain(), domain)
	}
	if got := cmd.Pages[0].Header.GetSequence(); got != 7 {
		return fmt.Errorf("expected command stamped with destination sequence 7, got %d", got)
	}
	return nil
}

func (c *routerScenarioCtx) thenRouterReturnsCommand() error {
	if c.lastErr != nil {
		return fmt.Errorf("dispatch failed: %v", c.lastErr)
	}
	if len(c.sagaCommands) == 0 {
		return fmt.Errorf("expected emitted command, got none")
	}
	return nil
}

func (c *routerScenarioCtx) thenRejectionNotificationEmitted() error {
	want := "inventory/test.ReserveStock"
	for _, seen := range c.rejectedSeen {
		if seen == want {
			return nil
		}
	}
	return fmt.Errorf("rejection handler never received %q, saw %v", want, c.rejectedSeen)
}

func (c *routerScenarioCtx) thenCompensationInitiated() error {
	if c.sagaResp == nil || len(c.sagaResp.Events) == 0 {
		return fmt.Errorf("expected compensation events in saga response, got %v", c.sagaResp)
	}
	return nil
}

func (c *routerScenarioCtx) thenProcessedIndependently() error {
	if c.execCount != 2 {
		return fmt.Errorf("expected 2 independent executions, got %d", c.execCount)
	}
	return nil
}

func (c *routerScenarioCtx) thenNoStateCarryOver() error {
	if len(c.perDispatch) != 2 {
		return fmt.Errorf("expected 2 dispatch records, got %d", len(c.perDispatch))
	}
	if c.perDispatch[0] != c.perDispatch[1] {
		return fmt.Errorf("dispatches diverged (%d vs %d) — state carried over", c.perDispatch[0], c.perDispatch[1])
	}
	return nil
}

func (c *routerScenarioCtx) thenEventsProcessedInOrder(n int) error {
	if len(c.projOrder) != n {
		return fmt.Errorf("expected %d events processed, got %d", n, len(c.projOrder))
	}
	for i, seq := range c.projOrder {
		if seq != uint32(i) {
			return fmt.Errorf("out of order at index %d: sequence %d", i, seq)
		}
	}
	return nil
}

func (c *routerScenarioCtx) thenProjectionReflectsAll(n int) error {
	if c.projection == nil {
		return fmt.Errorf("no projection returned")
	}
	if c.projCount != n {
		return fmt.Errorf("projection reflects %d events, expected %d", c.projCount, n)
	}
	if got := c.projection.Sequence; got != uint32(n-1) {
		return fmt.Errorf("projection sequence %d, expected %d", got, n-1)
	}
	return nil
}

func (c *routerScenarioCtx) thenAllThreeRoutable() error {
	for _, n := range []string{"TypeA", "TypeB", "TypeC"} {
		if err := c.whenReceiveCommand(n); err != nil {
			return err
		}
		if c.lastErr != nil {
			return fmt.Errorf("type %q not routable: %v", n, c.lastErr)
		}
	}
	return nil
}

func (c *routerScenarioCtx) thenEachSpecificHandler() error {
	for _, n := range []string{"TypeA", "TypeB", "TypeC"} {
		if c.invoked[n] != 1 {
			return fmt.Errorf("handler %q invoked %d times, expected exactly 1", n, c.invoked[n])
		}
	}
	return nil
}

func (c *routerScenarioCtx) thenHandlerReceivedTypedMessage() error {
	if c.lastErr != nil {
		return fmt.Errorf("dispatch failed: %v", c.lastErr)
	}
	if c.typedReceived == nil {
		return fmt.Errorf("handler never received the typed message")
	}
	if c.typedReceived.Sequence != 42 {
		return fmt.Errorf("decoded message lost data: sequence %d, expected 42", c.typedReceived.Sequence)
	}
	return nil
}

func (c *routerScenarioCtx) thenStateReflectsThreeEvents() error {
	if !c.stateBuilt {
		return fmt.Errorf("state never built")
	}
	if c.builtState.applied != 3 {
		return fmt.Errorf("expected 3 events applied, got %d", c.builtState.applied)
	}
	return nil
}

func (c *routerScenarioCtx) thenStateHasItems(n int) error {
	if c.builtState.items != n {
		return fmt.Errorf("expected %d items, got %d", n, c.builtState.items)
	}
	return nil
}

func (c *routerScenarioCtx) thenStateIsDefault() error {
	if !c.stateBuilt {
		return fmt.Errorf("state never built")
	}
	if c.builtState != (routerOrderState{}) {
		return fmt.Errorf("expected default state, got %+v", c.builtState)
	}
	return nil
}

func (c *routerScenarioCtx) thenCallerInformedOfFailure() error {
	if c.lastErr == nil {
		return fmt.Errorf("expected failure to surface to caller, got nil error")
	}
	return nil
}

func (c *routerScenarioCtx) thenNoEventsEmitted() error {
	if c.lastResp.GetEvents() != nil && len(c.lastResp.GetEvents().Pages) > 0 {
		return fmt.Errorf("expected no events, got %d", len(c.lastResp.GetEvents().Pages))
	}
	return nil
}

func (c *routerScenarioCtx) thenRequestFails() error {
	return c.thenCallerInformedOfFailure()
}

func (c *routerScenarioCtx) thenFailureIdentifiesMalformedPayload() error {
	clientErr := angzarr.AsClientError(c.lastErr)
	if clientErr == nil || clientErr.Code != angzarr.CodeNotificationDecodeFailed {
		return fmt.Errorf("expected coded %s identifying the malformed payload, got: %v",
			angzarr.CodeNotificationDecodeFailed, c.lastErr)
	}
	return nil
}

func (c *routerScenarioCtx) thenGuardRejects() error {
	// Fixture-authored guard text (defined by this harness's gvcRouter).
	if c.lastErr == nil || !strings.Contains(c.lastErr.Error(), "guard rejected") {
		return fmt.Errorf("expected guard rejection, got: %v", c.lastErr)
	}
	return nil
}

func (c *routerScenarioCtx) thenValidateRejects() error {
	if c.lastErr == nil || !strings.Contains(c.lastErr.Error(), "validate rejected") {
		return fmt.Errorf("expected validate rejection, got: %v", c.lastErr)
	}
	return nil
}

func (c *routerScenarioCtx) thenRejectionReasonDescribesIssue() error {
	if c.lastErr == nil || !strings.Contains(c.lastErr.Error(), "sequence must be positive") {
		return fmt.Errorf("expected reason describing the issue, got: %v", c.lastErr)
	}
	return nil
}

func (c *routerScenarioCtx) thenComputeProducesEvents() error {
	return c.thenRouterReturnsEvents()
}

func (c *routerScenarioCtx) thenEventsReflectStateChange() error {
	events := c.lastResp.GetEvents()
	if events == nil || len(events.Pages) == 0 {
		return fmt.Errorf("no events produced")
	}
	snap := &pb.Snapshot{}
	if err := events.Pages[0].GetEvent().UnmarshalTo(snap); err != nil {
		return fmt.Errorf("emitted event not decodable: %v", err)
	}
	if snap.Sequence != 9 {
		return fmt.Errorf("event does not reflect the commanded change: sequence %d, expected 9", snap.Sequence)
	}
	return nil
}

// InitRouterSteps registers router step definitions driving the real engine
// dispatch tables. Coordinator/bus-side scenarios in router.feature are
// tagged @wip and intentionally have no bindings here.
func InitRouterSteps(ctx *godog.ScenarioContext) {
	c := newRouterScenarioCtx()

	// Givens
	ctx.Step(`^an aggregate router with handlers for "([^"]*)" and "([^"]*)"$`, c.givenAggRouterTwo)
	ctx.Step(`^an aggregate router with handlers for "([^"]*)"$`, c.givenAggRouterOne)
	ctx.Step(`^an aggregate router$`, c.givenAggRouter)
	ctx.Step(`^an aggregate with existing events$`, c.givenAggregateWithEvents)
	ctx.Step(`^a saga router with handlers for "([^"]*)" and "([^"]*)"$`, c.givenSagaRouterTwo)
	ctx.Step(`^a saga router with a rejected command$`, c.givenSagaRouterWithRejectedCommand)
	ctx.Step(`^a saga router$`, c.givenSagaRouter)
	ctx.Step(`^a projector router with handlers for "([^"]*)"$`, c.givenProjectorRouterHandlers)
	ctx.Step(`^a projector router$`, c.givenProjectorRouter)
	ctx.Step(`^a PM router with handlers for "([^"]*)" and "([^"]*)"$`, c.givenPMRouterHandlers)
	ctx.Step(`^a router with handler for protobuf message type$`, c.givenTypedHandlerRouter)
	ctx.Step(`^a router$`, c.givenRouter)
	ctx.Step(`^an aggregate with guard checking aggregate exists$`, c.givenGuardedAggregate)
	ctx.Step(`^an aggregate handler with validation$`, c.givenValidatingAggregate)
	ctx.Step(`^an aggregate handler$`, c.givenValidatingAggregate)
	ctx.Step(`^events: OrderCreated, ItemAdded, ItemAdded$`, c.givenStandardEvents)
	ctx.Step(`^no events for the aggregate$`, c.givenNoEvents)

	// Whens
	ctx.Step(`^I receive an? "([^"]*)" command$`, c.whenReceiveCommand)
	ctx.Step(`^I receive a command for that aggregate$`, c.whenReceiveCommandForAggregate)
	ctx.Step(`^a handler emits (\d+) events$`, c.whenHandlerEmits)
	ctx.Step(`^a handler returns an error$`, c.whenHandlerReturnsError)
	ctx.Step(`^a handler produces a command$`, c.whenHandlerProducesCommand)
	ctx.Step(`^I receive an "([^"]*)" event from domain "([^"]*)"$`, c.whenReceiveEventFromDomain)
	ctx.Step(`^I receive an "([^"]*)" event$`, c.whenReceiveEvent)
	ctx.Step(`^I receive an event that triggers command to "([^"]*)"$`, c.whenReceiveEventTriggeringCommandTo)
	ctx.Step(`^the router processes the rejection$`, c.whenRouterProcessesRejection)
	ctx.Step(`^I process two events with same type$`, c.whenProcessTwoEventsSameType)
	ctx.Step(`^I receive (\d+) events in a batch$`, c.whenReceiveEventsInBatch)
	ctx.Step(`^I register handlers for "([^"]*)", "([^"]*)", and "([^"]*)"$`, c.whenRegisterThreeHandlers)
	ctx.Step(`^I receive an event with that type$`, c.whenReceiveEventWithThatType)
	ctx.Step(`^I build state from these events$`, c.whenBuildStateFromEvents)
	ctx.Step(`^I build state$`, c.whenBuildState)
	ctx.Step(`^I receive an event with invalid payload$`, c.whenReceiveInvalidPayload)
	ctx.Step(`^I send command to non-existent aggregate$`, c.whenSendCommandToNonexistentAggregate)
	ctx.Step(`^I send command to nonexistent aggregate$`, c.whenSendCommandToNonexistentAggregate)
	ctx.Step(`^I send command with invalid data$`, c.whenSendCommandWithInvalidData)
	ctx.Step(`^guard and validate pass$`, c.whenGuardAndValidatePass)

	// Thens
	ctx.Step(`^the (\w+) handler should be invoked$`, c.thenHandlerInvoked)
	ctx.Step(`^the (\w+) handler should NOT be invoked$`, c.thenHandlerNotInvoked)
	ctx.Step(`^the handler should receive state reflecting all previously recorded events$`, c.thenHandlerReceivedFullHistory)
	ctx.Step(`^the router should return those events$`, c.thenRouterReturnsEvents)
	ctx.Step(`^the events should carry consecutive sequences continuing the aggregate's history$`, c.thenEventsConsecutiveFromHistory)
	ctx.Step(`^the router should return an error$`, c.thenRouterReturnsError)
	ctx.Step(`^the error should indicate unknown command type$`, c.thenErrorUnknownCommand)
	ctx.Step(`^the emitted command should be sequenced to follow the current history of "([^"]*)"$`, c.thenCommandSequencedFor)
	ctx.Step(`^the router should return the command$`, c.thenRouterReturnsCommand)
	ctx.Step(`^a rejection notification should be emitted$`, c.thenRejectionNotificationEmitted)
	ctx.Step(`^compensation should be initiated for the rejected command$`, c.thenCompensationInitiated)
	ctx.Step(`^each should be processed independently$`, c.thenProcessedIndependently)
	ctx.Step(`^no state should carry over between events$`, c.thenNoStateCarryOver)
	ctx.Step(`^all (\d+) events should be processed in order$`, c.thenEventsProcessedInOrder)
	ctx.Step(`^the resulting projection should reflect all (\d+) events$`, c.thenProjectionReflectsAll)
	ctx.Step(`^all three types should be routable$`, c.thenAllThreeRoutable)
	ctx.Step(`^each should invoke its specific handler$`, c.thenEachSpecificHandler)
	ctx.Step(`^the handler should receive the message as its declared protobuf type$`, c.thenHandlerReceivedTypedMessage)
	ctx.Step(`^the state should reflect all three events applied$`, c.thenStateReflectsThreeEvents)
	ctx.Step(`^the state should have (\d+) items$`, c.thenStateHasItems)
	ctx.Step(`^the state should be the default/initial state$`, c.thenStateIsDefault)
	ctx.Step(`^the caller should be informed of the failure$`, c.thenCallerInformedOfFailure)
	ctx.Step(`^no events should be emitted$`, c.thenNoEventsEmitted)
	ctx.Step(`^the request should fail$`, c.thenRequestFails)
	ctx.Step(`^the failure should identify the malformed payload$`, c.thenFailureIdentifiesMalformedPayload)
	ctx.Step(`^guard should reject$`, c.thenGuardRejects)
	ctx.Step(`^no event should be emitted$`, c.thenNoEventsEmitted)
	ctx.Step(`^validate should reject$`, c.thenValidateRejects)
	ctx.Step(`^rejection reason should describe the issue$`, c.thenRejectionReasonDescribesIssue)
	ctx.Step(`^compute should produce events$`, c.thenComputeProducesEvents)
	ctx.Step(`^events should reflect the state change$`, c.thenEventsReflectStateChange)
}
