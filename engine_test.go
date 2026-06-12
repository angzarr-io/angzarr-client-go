package angzarr

// Engine tests — the shared dispatch/rebuild core that generated
// per-component tables delegate to (architecture of record: third-heap).
// Semantics pinned from the canonical specs:
//   - saga/projector: EVERY page in the book dispatches (saga.proto:45
//     "source events that triggered"; projector.feature C-0011 "each event
//     in the book triggers a handler call")
//   - rejection dispatch keys on the FULLY-QUALIFIED command type name
//   - corrupt persisted payloads fail the rebuild with
//     PERSISTED_EVENT_CORRUPT (mapped to DATA_LOSS at the adapter)

import (
	"context"
	"errors"
	"testing"

	pb "github.com/benjaminabbitt/angzarr/client/go/proto/angzarr_client/proto/angzarr/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
)

// --- Rebuilder ---------------------------------------------------------------

type counterState struct {
	applied []string
}

func mustAny(t *testing.T, m proto.Message) *anypb.Any {
	t.Helper()
	packed, err := anypb.New(m)
	if err != nil {
		t.Fatalf("anypb.New: %v", err)
	}
	return packed
}

func eventBookOf(t *testing.T, msgs ...proto.Message) *pb.EventBook {
	t.Helper()
	pages := make([]*pb.EventPage, 0, len(msgs))
	for _, m := range msgs {
		pages = append(pages, &pb.EventPage{
			Payload: &pb.EventPage_Event{Event: mustAny(t, m)},
		})
	}
	return &pb.EventBook{Pages: pages}
}

func coverApplier(t *testing.T) func(*counterState, *anypb.Any) error {
	t.Helper()
	return func(s *counterState, payload *anypb.Any) error {
		c := &pb.Cover{}
		if err := payload.UnmarshalTo(c); err != nil {
			return err
		}
		s.applied = append(s.applied, c.Domain)
		return nil
	}
}

func TestRebuilder_AppliesAllPagesInOrder(t *testing.T) {
	r := NewRebuilder(func() *counterState { return &counterState{} }).
		Apply(FullTypeName[*pb.Cover](), coverApplier(t))

	state, info, err := r.Rebuild(eventBookOf(t,
		&pb.Cover{Domain: "a"}, &pb.Cover{Domain: "b"}, &pb.Cover{Domain: "c"}))
	if err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	if len(state.applied) != 3 || state.applied[0] != "a" || state.applied[2] != "c" {
		t.Fatalf("applied = %v, want [a b c]", state.applied)
	}
	if !info.HadPriorEvents {
		t.Error("HadPriorEvents = false with 3 pages (the Exists() bug)")
	}
}

func TestRebuilder_NilBook_NormalizesToFreshState(t *testing.T) {
	r := NewRebuilder(func() *counterState { return &counterState{} })
	state, info, err := r.Rebuild(nil)
	if err != nil {
		t.Fatalf("Rebuild(nil): %v", err)
	}
	if state == nil || len(state.applied) != 0 {
		t.Fatalf("state = %v, want fresh", state)
	}
	if info.HadPriorEvents {
		t.Error("HadPriorEvents = true for nil book")
	}
}

func TestRebuilder_CorruptPayload_FailsWithCodedError(t *testing.T) {
	r := NewRebuilder(func() *counterState { return &counterState{} }).
		Apply(FullTypeName[*pb.Cover](), coverApplier(t))

	book := &pb.EventBook{Pages: []*pb.EventPage{
		{Payload: &pb.EventPage_Event{Event: &anypb.Any{
			TypeUrl: TypeURLPrefix + FullTypeName[*pb.Cover](),
			Value:   []byte{0xFF, 0xFF, 0xFF, 0xFF, 0xFF}, // not a valid Cover
		}}},
	}}

	_, _, err := r.Rebuild(book)
	if err == nil {
		t.Fatal("corrupt payload must fail the rebuild, not yield factory-default state")
	}
	var clientErr *ClientError
	if !errors.As(err, &clientErr) {
		t.Fatalf("uncoded error: %v", err)
	}
	if clientErr.Code != CodePersistedEventCorrupt {
		t.Fatalf("Code = %q, want %q", clientErr.Code, CodePersistedEventCorrupt)
	}
}

func TestRebuilder_UnknownEventType_Skipped(t *testing.T) {
	r := NewRebuilder(func() *counterState { return &counterState{} }).
		Apply(FullTypeName[*pb.Cover](), coverApplier(t))

	state, info, err := r.Rebuild(eventBookOf(t,
		&pb.Notification{}, // no applier registered — not all events fold into state
		&pb.Cover{Domain: "a"}))
	if err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	if len(state.applied) != 1 {
		t.Fatalf("applied = %v, want exactly the Cover", state.applied)
	}
	if !info.HadPriorEvents {
		t.Error("HadPriorEvents must reflect page presence, not applier hits")
	}
}

func TestRebuilder_SnapshotAppliedBeforePages(t *testing.T) {
	r := NewRebuilder(func() *counterState { return &counterState{} }).
		WithSnapshot(func(s *counterState, payload *anypb.Any) error {
			c := &pb.Cover{}
			if err := payload.UnmarshalTo(c); err != nil {
				return err
			}
			s.applied = append(s.applied, "snap:"+c.Domain)
			return nil
		}).
		Apply(FullTypeName[*pb.Cover](), coverApplier(t))

	book := eventBookOf(t, &pb.Cover{Domain: "after"})
	book.Snapshot = &pb.Snapshot{State: mustAny(t, &pb.Cover{Domain: "base"})}

	state, info, err := r.Rebuild(book)
	if err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	if len(state.applied) != 2 || state.applied[0] != "snap:base" || state.applied[1] != "after" {
		t.Fatalf("applied = %v, want [snap:base after]", state.applied)
	}
	if !info.HadPriorEvents {
		t.Error("a snapshot alone is prior history")
	}
}

// --- SagaDispatch ------------------------------------------------------------

func notificationPageFor(t *testing.T, fqCommand string) *pb.EventPage {
	t.Helper()
	rejection := &pb.RejectionNotification{
		RejectedCommand: &pb.CommandBook{
			Cover: &pb.Cover{Domain: "inventory"},
			Pages: []*pb.CommandPage{
				{Payload: &pb.CommandPage_Command{Command: &anypb.Any{
					TypeUrl: TypeURLPrefix + fqCommand,
				}}},
			},
		},
	}
	notification := &pb.Notification{Payload: mustAny(t, rejection)}
	return &pb.EventPage{Payload: &pb.EventPage_Event{Event: mustAny(t, notification)}}
}

func TestSagaDispatch_EveryPageDispatches(t *testing.T) {
	var seen []string
	d := NewSagaDispatch("saga-test", "order", "inventory").
		OnEvent(FullTypeName[*pb.Cover](), func(eventAny *anypb.Any, dests *Destinations) ([]*pb.CommandBook, []*pb.EventBook, error) {
			c := &pb.Cover{}
			if err := eventAny.UnmarshalTo(c); err != nil {
				return nil, nil, err
			}
			seen = append(seen, c.Domain)
			return []*pb.CommandBook{{}}, nil, nil
		})

	resp, err := d.Dispatch(eventBookOf(t, &pb.Cover{Domain: "e1"}, &pb.Cover{Domain: "e2"}), nil)
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if len(seen) != 2 {
		t.Fatalf("dispatched %v, want both pages (saga source = triggering events, not full state)", seen)
	}
	if len(resp.Commands) != 2 {
		t.Fatalf("commands = %d, want 2", len(resp.Commands))
	}
}

func TestSagaDispatch_NoMatchingHandler_YieldsNoCommands(t *testing.T) {
	d := NewSagaDispatch("saga-test", "order", "inventory").
		OnEvent(FullTypeName[*pb.Notification](), func(*anypb.Any, *Destinations) ([]*pb.CommandBook, []*pb.EventBook, error) {
			return []*pb.CommandBook{{}}, nil, nil
		})

	resp, err := d.Dispatch(eventBookOf(t, &pb.Cover{Domain: "x"}), nil)
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if len(resp.Commands) != 0 {
		t.Fatalf("commands = %d, want 0 (spec C-0051)", len(resp.Commands))
	}
}

func TestSagaDispatch_DestinationsReachHandler(t *testing.T) {
	var got uint32
	d := NewSagaDispatch("saga-test", "order", "inventory").
		OnEvent(FullTypeName[*pb.Cover](), func(_ *anypb.Any, dests *Destinations) ([]*pb.CommandBook, []*pb.EventBook, error) {
			got, _ = dests.SequenceFor("inventory")
			return nil, nil, nil
		})

	_, err := d.Dispatch(eventBookOf(t, &pb.Cover{}), map[string]uint32{"inventory": 7})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if got != 7 {
		t.Fatalf("observed inventory seq = %d, want 7 (spec C-0052)", got)
	}
}

func TestSagaDispatch_RejectionRoutesByFQCommandType(t *testing.T) {
	fq := "angzarr_client.proto.examples.v1.ReserveStock"
	var fired bool
	d := NewSagaDispatch("saga-test", "order", "inventory").
		OnRejected(fq, func(n *pb.Notification, rj *pb.RejectionNotification) ([]*pb.EventBook, error) {
			fired = true
			return []*pb.EventBook{{}}, nil
		})

	resp, err := d.Dispatch(&pb.EventBook{Pages: []*pb.EventPage{notificationPageFor(t, fq)}}, nil)
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if !fired {
		t.Fatal("FQ-keyed rejection handler did not fire (deep-clerk contract)")
	}
	if len(resp.Events) != 1 {
		t.Fatalf("compensation events = %d, want 1", len(resp.Events))
	}
}

func TestSagaDispatch_UndeclaredRejection_DelegatesToFramework(t *testing.T) {
	d := NewSagaDispatch("saga-test", "order", "inventory")
	resp, err := d.Dispatch(&pb.EventBook{Pages: []*pb.EventPage{
		notificationPageFor(t, "angzarr_client.proto.examples.v1.SomethingElse"),
	}}, nil)
	if err != nil {
		t.Fatalf("undeclared rejection must not error (framework default): %v", err)
	}
	if len(resp.Commands) != 0 || len(resp.Events) != 0 {
		t.Fatal("undeclared rejection must yield an empty response")
	}
}

func TestSagaDispatch_CorruptNotification_CodedError(t *testing.T) {
	d := NewSagaDispatch("saga-test", "order", "inventory")
	book := &pb.EventBook{Pages: []*pb.EventPage{
		{Payload: &pb.EventPage_Event{Event: &anypb.Any{
			TypeUrl: TypeURLPrefix + FullTypeName[*pb.Notification](),
			Value:   []byte{0xFF, 0xFF, 0xFF, 0xFF, 0xFF},
		}}},
	}}
	_, err := d.Dispatch(book, nil)
	if err == nil {
		t.Fatal("corrupt Notification must error")
	}
	var clientErr *ClientError
	if !errors.As(err, &clientErr) || clientErr.Code != CodeNotificationDecodeFailed {
		t.Fatalf("err = %v, want coded %s", err, CodeNotificationDecodeFailed)
	}
}

func TestSagaDispatch_MissingSource_CodedError(t *testing.T) {
	d := NewSagaDispatch("saga-test", "order", "inventory")
	for name, book := range map[string]*pb.EventBook{
		"nil book":   nil,
		"empty book": {},
	} {
		_, err := d.Dispatch(book, nil)
		if err == nil {
			t.Fatalf("%s: expected error", name)
		}
		var clientErr *ClientError
		if !errors.As(err, &clientErr) {
			t.Fatalf("%s: uncoded error %v", name, err)
		}
	}
}

func TestSagaDispatch_SubscriptionsDeriveFromTable(t *testing.T) {
	d := NewSagaDispatch("saga-test", "order", "inventory").
		OnEvent("examples.OrderCreated", func(*anypb.Any, *Destinations) ([]*pb.CommandBook, []*pb.EventBook, error) {
			return nil, nil, nil
		})
	subs := d.Subscriptions()
	if len(subs["order"]) != 1 || subs["order"][0] != "examples.OrderCreated" {
		t.Fatalf("Subscriptions = %v", subs)
	}
}

// --- AggregateDispatch --------------------------------------------------------

func testAggDispatch(t *testing.T) (*AggregateDispatch[*counterState], *[]CommandContext) {
	t.Helper()
	var contexts []CommandContext
	rebuilder := NewRebuilder(func() *counterState { return &counterState{} }).
		Apply(FullTypeName[*pb.Cover](), coverApplier(t))
	d := NewAggregateDispatch("agg-test", "order", rebuilder).
		OnCommand(FullTypeName[*pb.Cover](), func(cmdAny *anypb.Any, state *counterState, cctx CommandContext) (*pb.EventBook, error) {
			contexts = append(contexts, cctx)
			return &pb.EventBook{Pages: []*pb.EventPage{{}}}, nil
		})
	return d, &contexts
}

func commandFor(t *testing.T, m proto.Message) *pb.ContextualCommand {
	t.Helper()
	return &pb.ContextualCommand{
		Command: &pb.CommandBook{
			Pages: []*pb.CommandPage{
				{Payload: &pb.CommandPage_Command{Command: mustAny(t, m)}},
			},
		},
	}
}

func TestAggregateDispatch_NilEvents_FreshStateNoPriorHistory(t *testing.T) {
	d, contexts := testAggDispatch(t)
	resp, err := d.Dispatch(commandFor(t, &pb.Cover{}))
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if resp.GetEvents() == nil {
		t.Fatal("expected events result")
	}
	cctx := (*contexts)[0]
	if cctx.HadPriorEvents {
		t.Error("HadPriorEvents = true for nil Events (the Exists() bug)")
	}
	if cctx.NextSequence != 0 {
		t.Errorf("NextSequence = %d, want 0", cctx.NextSequence)
	}
}

func TestAggregateDispatch_PriorEvents_ReachStateAndContext(t *testing.T) {
	d, contexts := testAggDispatch(t)
	cmd := commandFor(t, &pb.Cover{})
	cmd.Events = eventBookOf(t, &pb.Cover{Domain: "p1"}, &pb.Cover{Domain: "p2"})
	cmd.Events.NextSequence = 2

	if _, err := d.Dispatch(cmd); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	cctx := (*contexts)[0]
	if !cctx.HadPriorEvents {
		t.Error("HadPriorEvents = false with 2 prior pages")
	}
	if cctx.NextSequence == 0 {
		t.Error("NextSequence not derived from prior events")
	}
}

func TestAggregateDispatch_UnknownCommand_CodedBeforeRebuild(t *testing.T) {
	d, _ := testAggDispatch(t)
	cmd := commandFor(t, &pb.Edition{}) // no handler for this type
	// Corrupt prior events: if the dispatcher rebuilt before validating
	// the command type, we'd see PERSISTED_EVENT_CORRUPT instead.
	cmd.Events = &pb.EventBook{Pages: []*pb.EventPage{
		{Payload: &pb.EventPage_Event{Event: &anypb.Any{
			TypeUrl: TypeURLPrefix + FullTypeName[*pb.Cover](),
			Value:   []byte{0xFF, 0xFF, 0xFF},
		}}},
	}}

	_, err := d.Dispatch(cmd)
	var clientErr *ClientError
	if !errors.As(err, &clientErr) || clientErr.Code != CodeNoHandlerRegistered {
		t.Fatalf("err = %v, want NO_HANDLER_REGISTERED (validate before rebuild)", err)
	}
}

func TestAggregateDispatch_CorruptPriorEvent_FailsCommand(t *testing.T) {
	d, _ := testAggDispatch(t)
	cmd := commandFor(t, &pb.Cover{})
	cmd.Events = &pb.EventBook{Pages: []*pb.EventPage{
		{Payload: &pb.EventPage_Event{Event: &anypb.Any{
			TypeUrl: TypeURLPrefix + FullTypeName[*pb.Cover](),
			Value:   []byte{0xFF, 0xFF, 0xFF},
		}}},
	}}

	_, err := d.Dispatch(cmd)
	var clientErr *ClientError
	if !errors.As(err, &clientErr) || clientErr.Code != CodePersistedEventCorrupt {
		t.Fatalf("err = %v, want PERSISTED_EVENT_CORRUPT (never validate against truncated state)", err)
	}
}

func TestAggregateDispatch_MissingCommand_Coded(t *testing.T) {
	d, _ := testAggDispatch(t)
	for name, cmd := range map[string]*pb.ContextualCommand{
		"nil request": nil,
		"nil book":    {},
		"empty pages": {Command: &pb.CommandBook{}},
	} {
		_, err := d.Dispatch(cmd)
		var clientErr *ClientError
		if !errors.As(err, &clientErr) {
			t.Fatalf("%s: uncoded error %v", name, err)
		}
	}
}

func TestAggregateDispatch_RejectionRoutesByFQWithState(t *testing.T) {
	fq := "angzarr_client.proto.examples.v1.ReserveStock"
	var sawState *counterState
	rebuilder := NewRebuilder(func() *counterState { return &counterState{} }).
		Apply(FullTypeName[*pb.Cover](), coverApplier(t))
	d := NewAggregateDispatch("agg-test", "order", rebuilder).
		OnRejected(fq, func(n *pb.Notification, rj *pb.RejectionNotification, state *counterState, _ CommandContext) (*pb.BusinessResponse, error) {
			sawState = state
			return &pb.BusinessResponse{}, nil
		})

	notifPage := notificationPageFor(t, fq)
	cmd := &pb.ContextualCommand{
		Command: &pb.CommandBook{Pages: []*pb.CommandPage{
			{Payload: &pb.CommandPage_Command{Command: notifPage.GetEvent()}},
		}},
		Events: eventBookOf(t, &pb.Cover{Domain: "prior"}),
	}

	if _, err := d.Dispatch(cmd); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if sawState == nil || len(sawState.applied) != 1 {
		t.Fatalf("rejection handler state = %v, want rebuilt prior state", sawState)
	}
}

// --- Adapter integration ------------------------------------------------------

// The full decided chain: corrupt persisted event fails the rebuild
// (cheap-hull) and crosses the wire as DATA_LOSS carrying the
// PERSISTED_EVENT_CORRUPT reason (armed-pleat).
func TestAggregateAdapter_CorruptEvent_IsDataLossOnTheWire(t *testing.T) {
	d, _ := testAggDispatch(t)
	h := NewAggregateDispatchHandler(d)

	cmd := commandFor(t, &pb.Cover{})
	cmd.Events = &pb.EventBook{Pages: []*pb.EventPage{
		{Payload: &pb.EventPage_Event{Event: &anypb.Any{
			TypeUrl: TypeURLPrefix + FullTypeName[*pb.Cover](),
			Value:   []byte{0xFF, 0xFF, 0xFF},
		}}},
	}}

	_, err := h.Handle(context.Background(), cmd)
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("not a status error: %v", err)
	}
	if st.Code() != codes.DataLoss {
		t.Fatalf("wire code = %v, want DataLoss", st.Code())
	}
	info := errorInfoOf(t, err)
	if info == nil || info.Reason != CodePersistedEventCorrupt {
		t.Fatalf("ErrorInfo = %v, want Reason PERSISTED_EVENT_CORRUPT", info)
	}
}

// Unknown command crosses the wire as UNIMPLEMENTED (misrouted/stale
// deployment), never InvalidArgument.
func TestAggregateAdapter_UnknownCommand_IsUnimplementedOnTheWire(t *testing.T) {
	d, _ := testAggDispatch(t)
	h := NewAggregateDispatchHandler(d)

	_, err := h.Handle(context.Background(), commandFor(t, &pb.Edition{}))
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("not a status error: %v", err)
	}
	if st.Code() != codes.Unimplemented {
		t.Fatalf("wire code = %v, want Unimplemented", st.Code())
	}
}

// --- ProcessManagerDispatch ---------------------------------------------------

func testPMDispatch(t *testing.T) *ProcessManagerDispatch[*counterState] {
	t.Helper()
	rebuilder := NewRebuilder(func() *counterState { return &counterState{} }).
		Apply(FullTypeName[*pb.Cover](), coverApplier(t))
	return NewProcessManagerDispatch("pm-test", "fulfillment", rebuilder).
		OnEvent("order", FullTypeName[*pb.Cover](), func(eventAny *anypb.Any, state *counterState, dests *Destinations) (*pb.ProcessManagerHandleResponse, error) {
			return &pb.ProcessManagerHandleResponse{
				Commands: []*pb.CommandBook{{}},
			}, nil
		})
}

func triggerBookFor(t *testing.T, domain string, msgs ...proto.Message) *pb.EventBook {
	t.Helper()
	book := eventBookOf(t, msgs...)
	book.Cover = &pb.Cover{Domain: domain}
	return book
}

func TestPMDispatch_NewestPageOnly(t *testing.T) {
	var dispatched int
	rebuilder := NewRebuilder(func() *counterState { return &counterState{} })
	d := NewProcessManagerDispatch("pm-test", "fulfillment", rebuilder).
		OnEvent("order", FullTypeName[*pb.Cover](), func(*anypb.Any, *counterState, *Destinations) (*pb.ProcessManagerHandleResponse, error) {
			dispatched++
			return &pb.ProcessManagerHandleResponse{}, nil
		})

	// Trigger = FULL STATE of the triggering domain (process_manager.proto:73);
	// only the newest event is the trigger — history must not re-fire.
	_, err := d.Dispatch(triggerBookFor(t, "order",
		&pb.Cover{Domain: "old1"}, &pb.Cover{Domain: "old2"}, &pb.Cover{Domain: "new"}), nil, nil)
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if dispatched != 1 {
		t.Fatalf("dispatched %d, want 1 (newest page only — trigger is full state)", dispatched)
	}
}

func TestPMDispatch_StateRebuiltFromProcessEvents(t *testing.T) {
	var sawApplied int
	rebuilder := NewRebuilder(func() *counterState { return &counterState{} }).
		Apply(FullTypeName[*pb.Cover](), coverApplier(t))
	d := NewProcessManagerDispatch("pm-test", "fulfillment", rebuilder).
		OnEvent("order", FullTypeName[*pb.Cover](), func(_ *anypb.Any, state *counterState, _ *Destinations) (*pb.ProcessManagerHandleResponse, error) {
			sawApplied = len(state.applied)
			return &pb.ProcessManagerHandleResponse{}, nil
		})

	processState := eventBookOf(t, &pb.Cover{Domain: "c1"}, &pb.Cover{Domain: "c2"})
	_, err := d.Dispatch(triggerBookFor(t, "order", &pb.Cover{}), processState, nil)
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if sawApplied != 2 {
		t.Fatalf("PM state saw %d process events, want 2 (spec C-0021)", sawApplied)
	}
}

func TestPMDispatch_OutsideSources_EmptyResponse(t *testing.T) {
	d := testPMDispatch(t)
	// Spec C-0022: a trigger from a domain outside sources yields NO
	// commands — an empty response, not an error (legacy Unimplemented
	// error was drift).
	resp, err := d.Dispatch(triggerBookFor(t, "weather", &pb.Cover{}), nil, nil)
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if len(resp.Commands) != 0 {
		t.Fatalf("commands = %d, want 0 (spec C-0022)", len(resp.Commands))
	}
}

func TestPMDispatch_RejectionEscalatesViaNotificationField(t *testing.T) {
	fq := "angzarr_client.proto.examples.v1.ReserveStock"
	rebuilder := NewRebuilder(func() *counterState { return &counterState{} })
	escalation := &pb.Notification{}
	d := NewProcessManagerDispatch("pm-test", "fulfillment", rebuilder).
		OnRejected(fq, func(n *pb.Notification, rj *pb.RejectionNotification, state *counterState) ([]*pb.EventBook, *pb.Notification, error) {
			return []*pb.EventBook{{}}, escalation, nil
		})

	trigger := &pb.EventBook{
		Cover: &pb.Cover{Domain: "order"},
		Pages: []*pb.EventPage{notificationPageFor(t, fq)},
	}
	resp, err := d.Dispatch(trigger, nil, nil)
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if len(resp.ProcessEvents) != 1 {
		t.Fatalf("process events = %d, want 1", len(resp.ProcessEvents))
	}
	// mean-shack: escalation crosses the wire via the new field 4.
	if resp.Notification == nil {
		t.Fatal("escalation Notification not populated on the response (field 4)")
	}
}

func TestPMDispatch_MissingTrigger_Coded(t *testing.T) {
	d := testPMDispatch(t)
	for name, trigger := range map[string]*pb.EventBook{
		"nil trigger":   nil,
		"empty trigger": {Cover: &pb.Cover{Domain: "order"}},
	} {
		_, err := d.Dispatch(trigger, nil, nil)
		var clientErr *ClientError
		if !errors.As(err, &clientErr) {
			t.Fatalf("%s: uncoded error %v", name, err)
		}
	}
}

// --- ProjectorDispatch --------------------------------------------------------

func TestProjectorDispatch_EveryPageFoldsIntoOneProjection(t *testing.T) {
	d := NewProjectorDispatch("prj-test", func() *counterState { return &counterState{} }).
		OnEvent(FullTypeName[*pb.Cover](), func(p *counterState, eventAny *anypb.Any) error {
			c := &pb.Cover{}
			if err := eventAny.UnmarshalTo(c); err != nil {
				return err
			}
			p.applied = append(p.applied, c.Domain)
			return nil
		}).
		Finish(func(p *counterState, events *pb.EventBook) (*pb.Projection, error) {
			return &pb.Projection{Projector: "prj-test", Sequence: uint32(len(p.applied))}, nil
		})

	book := eventBookOf(t, &pb.Cover{Domain: "a"}, &pb.Cover{Domain: "b"})
	book.Cover = &pb.Cover{Domain: "order"}

	proj, err := d.Dispatch(book)
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	// Spec C-0011 + C-0012: each event triggers a call; one instance is
	// reused across the delivery and yields ONE projection.
	if proj.Sequence != 2 {
		t.Fatalf("folded %d events, want 2", proj.Sequence)
	}
}

func TestProjectorDispatch_MissingCover_Coded(t *testing.T) {
	d := NewProjectorDispatch("prj-test", func() *counterState { return &counterState{} })
	_, err := d.Dispatch(&pb.EventBook{})
	var clientErr *ClientError
	if !errors.As(err, &clientErr) {
		t.Fatalf("uncoded error: %v", err)
	}
}

func TestRebuilder_PagesCoveredBySnapshotAreSkipped(t *testing.T) {
	r := NewRebuilder(func() *counterState { return &counterState{} }).
		WithSnapshot(func(s *counterState, _ *anypb.Any) error {
			s.applied = append(s.applied, "snap")
			return nil
		}).
		Apply(FullTypeName[*pb.Cover](), coverApplier(t))

	// Pages 1..3; snapshot covers through sequence 2 — re-applying covered
	// pages would double-fold their effects into state.
	book := &pb.EventBook{
		Snapshot: &pb.Snapshot{Sequence: 2, State: mustAny(t, &pb.Cover{})},
		Pages: []*pb.EventPage{
			{Header: &pb.PageHeader{SequenceType: &pb.PageHeader_Sequence{Sequence: 1}},
				Payload: &pb.EventPage_Event{Event: mustAny(t, &pb.Cover{Domain: "covered1"})}},
			{Header: &pb.PageHeader{SequenceType: &pb.PageHeader_Sequence{Sequence: 2}},
				Payload: &pb.EventPage_Event{Event: mustAny(t, &pb.Cover{Domain: "covered2"})}},
			{Header: &pb.PageHeader{SequenceType: &pb.PageHeader_Sequence{Sequence: 3}},
				Payload: &pb.EventPage_Event{Event: mustAny(t, &pb.Cover{Domain: "after"})}},
		},
	}
	state, _, err := r.Rebuild(book)
	if err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	if len(state.applied) != 2 || state.applied[0] != "snap" || state.applied[1] != "after" {
		t.Fatalf("applied = %v, want [snap after] (covered pages skipped)", state.applied)
	}
}

// Spec C-0042: multiple compensators for the same rejection ALL run, in
// registration order — distinct undoings (release funds AND notify) are
// independently registered.
func TestAggregateDispatch_MultipleCompensators_AllRunInOrder(t *testing.T) {
	fq := "angzarr_client.proto.examples.v1.ReserveStock"
	var order []string
	rebuilder := NewRebuilder(func() *counterState { return &counterState{} })
	d := NewAggregateDispatch("agg-test", "payment", rebuilder).
		OnRejected(fq, func(n *pb.Notification, rj *pb.RejectionNotification, s *counterState, _ CommandContext) (*pb.BusinessResponse, error) {
			order = append(order, "first")
			return &pb.BusinessResponse{Result: &pb.BusinessResponse_Events{Events: &pb.EventBook{Pages: []*pb.EventPage{{}}}}}, nil
		}).
		OnRejected(fq, func(n *pb.Notification, rj *pb.RejectionNotification, s *counterState, _ CommandContext) (*pb.BusinessResponse, error) {
			order = append(order, "second")
			return &pb.BusinessResponse{Result: &pb.BusinessResponse_Events{Events: &pb.EventBook{Pages: []*pb.EventPage{{}}}}}, nil
		})

	notifPage := notificationPageFor(t, fq)
	resp, err := d.Dispatch(&pb.ContextualCommand{
		Command: &pb.CommandBook{Pages: []*pb.CommandPage{
			{Payload: &pb.CommandPage_Command{Command: notifPage.GetEvent()}},
		}},
	})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if len(order) != 2 || order[0] != "first" || order[1] != "second" {
		t.Fatalf("compensators ran %v, want [first second] (C-0042)", order)
	}
	if got := len(resp.GetEvents().GetPages()); got != 2 {
		t.Fatalf("merged compensation events = %d, want 2", got)
	}
}

// Spec C-0083: compensation events append after prior history — the
// rejection thunk needs the aggregate's NextSequence to stamp them.
func TestAggregateDispatch_RejectionReceivesCommandContext(t *testing.T) {
	fq := "angzarr_client.proto.examples.v1.ReserveStock"
	var saw CommandContext
	rebuilder := NewRebuilder(func() *counterState { return &counterState{} })
	d := NewAggregateDispatch("agg-test", "payment", rebuilder).
		OnRejected(fq, func(n *pb.Notification, rj *pb.RejectionNotification, s *counterState, cctx CommandContext) (*pb.BusinessResponse, error) {
			saw = cctx
			return &pb.BusinessResponse{}, nil
		})

	notifPage := notificationPageFor(t, fq)
	_, err := d.Dispatch(&pb.ContextualCommand{
		Command: &pb.CommandBook{Pages: []*pb.CommandPage{
			{Payload: &pb.CommandPage_Command{Command: notifPage.GetEvent()}},
		}},
		Events: &pb.EventBook{NextSequence: 7, Pages: []*pb.EventPage{{}}},
	})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if saw.NextSequence != 7 {
		t.Fatalf("rejection cctx.NextSequence = %d, want 7 (C-0083 stamping)", saw.NextSequence)
	}
	if !saw.HadPriorEvents {
		t.Error("rejection cctx.HadPriorEvents = false with prior history")
	}
}

// Spec C-0032: events from domains the projector does not consume must
// not fire handlers — declared event types alone are not sufficient.
func TestProjectorDispatch_UndeclaredDomain_DoesNotFire(t *testing.T) {
	var fired int
	d := NewProjectorDispatch("prj-test", func() *counterState { return &counterState{} }).
		ForDomains("order").
		OnEvent(FullTypeName[*pb.Cover](), func(p *counterState, _ *anypb.Any) error {
			fired++
			return nil
		})

	book := eventBookOf(t, &pb.Cover{Domain: "x"})
	book.Cover = &pb.Cover{Domain: "inventory"} // not a consumed domain

	if _, err := d.Dispatch(book); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if fired != 0 {
		t.Fatalf("handlers fired %d times for an undeclared domain, want 0 (C-0032)", fired)
	}
}

// --- Composition (multi-component processes; spec C-0010..C-0015) -------------

// Audit #18: two CommandHandlers may not claim the same
// (domain, command type) — rejected at BUILD, not dispatch.
func TestComposeCommandHandlers_DuplicateClaimRejected(t *testing.T) {
	rebuilder := func() *Rebuilder[*counterState] {
		return NewRebuilder(func() *counterState { return &counterState{} })
	}
	noop := func(*anypb.Any, *counterState, CommandContext) (*pb.EventBook, error) {
		return &pb.EventBook{}, nil
	}
	alpha := NewAggregateDispatch("Alpha", "order", rebuilder()).OnCommand("test.CreateOrder", noop)
	beta := NewAggregateDispatch("Beta", "order", rebuilder()).OnCommand("test.CreateOrder", noop)

	_, err := ComposeCommandHandlers(alpha, beta)
	if err == nil {
		t.Fatal("duplicate (domain, command) claim must fail at build (C-0010)")
	}

	// Same type under different domains is fine (C-0011).
	gamma := NewAggregateDispatch("Gamma", "orderB", rebuilder()).OnCommand("test.CreateOrder", noop)
	if _, err := ComposeCommandHandlers(alpha, gamma); err != nil {
		t.Fatalf("cross-domain same-type claim must build: %v", err)
	}
}

// Sagas fan out: every matching component runs, commands merge in
// registration order (C-0013).
func TestComposeSagas_FanOutInRegistrationOrder(t *testing.T) {
	emit := func(domain string) *SagaDispatch {
		return NewSagaDispatch("saga-"+domain, "order", domain).
			OnEvent(FullTypeName[*pb.Cover](), func(*anypb.Any, *Destinations) ([]*pb.CommandBook, []*pb.EventBook, error) {
				return []*pb.CommandBook{{Cover: &pb.Cover{Domain: domain}}}, nil, nil
			})
	}
	fanout := ComposeSagas(emit("inventory"), emit("fulfillment"))

	book := eventBookOf(t, &pb.Cover{})
	book.Cover = &pb.Cover{Domain: "order"}
	resp, err := fanout.Dispatch(book, nil)
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if len(resp.Commands) != 2 ||
		resp.Commands[0].GetCover().GetDomain() != "inventory" ||
		resp.Commands[1].GetCover().GetDomain() != "fulfillment" {
		t.Fatalf("commands = %v, want [inventory fulfillment] in order", resp.Commands)
	}
}

// C-0146..C-0148: the dispatch path stamps the command's cover.ext onto
// the emitted EventBook's cover — FILL-ONLY, never overriding a
// handler-set ext. C-0001: emitted pages without headers get consecutive
// sequences from the aggregate's next sequence (fill-only).
func TestAggregateDispatch_StampsExtAndSequencesFillOnly(t *testing.T) {
	parent := mustAny(t, &pb.Cover{Domain: "parent"})
	handlerExt := mustAny(t, &pb.Cover{Domain: "handler-set"})

	rebuilder := NewRebuilder(func() *counterState { return &counterState{} })
	d := NewAggregateDispatch("agg", "order", rebuilder).
		OnCommand("test.Create", func(*anypb.Any, *counterState, CommandContext) (*pb.EventBook, error) {
			return &pb.EventBook{Pages: []*pb.EventPage{{}, {}}}, nil
		}).
		OnCommand("test.Explicit", func(*anypb.Any, *counterState, CommandContext) (*pb.EventBook, error) {
			return &pb.EventBook{
				Cover: &pb.Cover{Ext: handlerExt},
				Pages: []*pb.EventPage{
					{Header: &pb.PageHeader{SequenceType: &pb.PageHeader_Sequence{Sequence: 99}}},
				},
			}, nil
		})

	cmdWith := func(fqType string, ext *anypb.Any, nextSeq uint32) *pb.ContextualCommand {
		req := &pb.ContextualCommand{
			Command: &pb.CommandBook{
				Cover: &pb.Cover{Domain: "order", Ext: ext},
				Pages: []*pb.CommandPage{
					{Payload: &pb.CommandPage_Command{Command: &anypb.Any{TypeUrl: TypeURLPrefix + fqType}}},
				},
			},
		}
		if nextSeq > 0 {
			req.Events = &pb.EventBook{NextSequence: nextSeq, Pages: []*pb.EventPage{{}}}
		}
		return req
	}

	// Fill: command ext propagates to the emitted book's cover; headerless
	// pages get sequences nextSeq, nextSeq+1.
	resp, err := d.Dispatch(cmdWith("test.Create", parent, 5))
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	events := resp.GetEvents()
	if events.GetCover().GetExt().GetTypeUrl() != parent.TypeUrl {
		t.Fatal("command cover.ext not stamped onto emitted book (C-0146)")
	}
	if events.Pages[0].GetHeader().GetSequence() != 5 || events.Pages[1].GetHeader().GetSequence() != 6 {
		t.Fatalf("sequences = %d,%d, want 5,6 (fill-only stamping)",
			events.Pages[0].GetHeader().GetSequence(), events.Pages[1].GetHeader().GetSequence())
	}

	// Never override: handler-set ext and explicit headers survive.
	resp, err = d.Dispatch(cmdWith("test.Explicit", parent, 5))
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	events = resp.GetEvents()
	if !proto.Equal(events.GetCover().GetExt(), handlerExt) {
		t.Fatal("handler-set ext was overridden (C-0147)")
	}
	if events.Pages[0].GetHeader().GetSequence() != 99 {
		t.Fatal("explicit page header was overridden")
	}

	// No ext on the command → emitted cover.ext stays unset (C-0148).
	resp, err = d.Dispatch(cmdWith("test.Create", nil, 0))
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if resp.GetEvents().GetCover().GetExt() != nil {
		t.Fatal("ext invented from nothing (C-0148)")
	}
}

// --- RouterBuilder (spec C-0060..C-0065, C-0088) -------------------------------

func builderCH(t *testing.T, name, domain string, cmdTypes ...string) *AggregateDispatch[*counterState] {
	t.Helper()
	table := NewAggregateDispatch(name, domain, NewRebuilder(func() *counterState { return &counterState{} }))
	for _, fqType := range cmdTypes {
		table.OnCommand(fqType, func(*anypb.Any, *counterState, CommandContext) (*pb.EventBook, error) {
			return &pb.EventBook{}, nil
		})
	}
	return table
}

func TestRouterBuilder_Validation(t *testing.T) {
	t.Run("empty configuration is rejected", func(t *testing.T) {
		_, err := NewRouterBuilder().Build()
		var clientErr *ClientError
		if !errors.As(err, &clientErr) || clientErr.Code != CodeRouterNoHandlers {
			t.Fatalf("err = %v, want ROUTER_NO_HANDLERS (C-0060)", err)
		}
	})
	t.Run("unmarked component is rejected", func(t *testing.T) {
		_, err := NewRouterBuilder().Register(struct{}{}).Build()
		var clientErr *ClientError
		if !errors.As(err, &clientErr) || clientErr.Code != CodeHandlerUnknownKind {
			t.Fatalf("err = %v, want UNRECOGNIZED_HANDLER_KIND (C-0061)", err)
		}
	})
	t.Run("mixed kinds are rejected", func(t *testing.T) {
		_, err := NewRouterBuilder().
			Register(builderCH(t, "Order", "order", "test.Create")).
			Register(NewSagaDispatch("s", "order", "inventory")).
			Build()
		var clientErr *ClientError
		if !errors.As(err, &clientErr) || clientErr.Code != CodeMixedHandlerKinds {
			t.Fatalf("err = %v, want MIXED_HANDLER_KINDS (C-0063)", err)
		}
	})
	t.Run("duplicate claims are rejected at build", func(t *testing.T) {
		_, err := NewRouterBuilder().
			Register(builderCH(t, "Alpha", "order", "test.Create")).
			Register(builderCH(t, "Beta", "order", "test.Create")).
			Build()
		var clientErr *ClientError
		if !errors.As(err, &clientErr) || clientErr.Code != CodeDuplicateCommandHandler {
			t.Fatalf("err = %v, want DUPLICATE_COMMAND_CLAIM (C-0064)", err)
		}
	})
	t.Run("distinct domains build a command router", func(t *testing.T) {
		router, err := NewRouterBuilder().
			Register(builderCH(t, "Order", "order", "test.Create")).
			Register(builderCH(t, "Payment", "payment", "test.Create")).
			Build()
		if err != nil {
			t.Fatalf("Build: %v", err)
		}
		if router.CommandMux == nil {
			t.Fatal("expected a command-routing router (C-0062)")
		}
	})
	t.Run("a saga builds a saga router", func(t *testing.T) {
		router, err := NewRouterBuilder().
			Register(NewSagaDispatch("s", "order", "inventory")).
			Build()
		if err != nil {
			t.Fatalf("Build: %v", err)
		}
		if router.SagaFanout == nil {
			t.Fatal("expected a saga-routing router (C-0088)")
		}
	})
}

// countingDispatcher pins C-0065: build introspects each handler once.
type countingDispatcher struct {
	CommandDispatcher
	introspections int
}

func (d *countingDispatcher) CommandTypes() []string {
	d.introspections++
	return d.CommandDispatcher.CommandTypes()
}

func TestRouterBuilder_IntrospectsEachHandlerOnce(t *testing.T) {
	counter := &countingDispatcher{CommandDispatcher: builderCH(t, "Order", "order", "test.Create")}
	if _, err := NewRouterBuilder().Register(counter).Build(); err != nil {
		t.Fatalf("Build: %v", err)
	}
	if counter.introspections != 1 {
		t.Fatalf("introspected %d times, want exactly 1 (C-0065)", counter.introspections)
	}
}
