package angzarr

// Boundary and degenerate-shape contracts for the engine core: empty
// books, missing snapshots/payloads, exact-sequence snapshot coverage,
// introspection slice exactness, and fan-out merge order. These shapes
// are where dispatch silently misbehaves rather than fails loudly —
// mutation testing of the engine showed them unverified by the
// happy-path suites.

import (
	"errors"
	"testing"

	pb "github.com/benjaminabbitt/angzarr/client/go/proto/angzarr_client/proto/angzarr/v1"
	"google.golang.org/protobuf/types/known/anypb"
)

// --- Rebuilder: HadPriorEvents + snapshot-guard boundaries ------------------

func TestRebuilder_EmptyBook_NoPriorHistory(t *testing.T) {
	r := NewRebuilder(func() *counterState { return &counterState{} })
	_, info, err := r.Rebuild(&pb.EventBook{})
	if err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	if info.HadPriorEvents {
		t.Error("HadPriorEvents = true for an empty book — pageless, snapshotless history is fresh state")
	}
}

func TestRebuilder_SnapshotOnly_IsPriorHistory(t *testing.T) {
	r := NewRebuilder(func() *counterState { return &counterState{} })
	book := &pb.EventBook{Snapshot: &pb.Snapshot{Sequence: 5, State: mustAny(t, &pb.Cover{})}}
	_, info, err := r.Rebuild(book)
	if err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	if !info.HadPriorEvents {
		t.Error("HadPriorEvents = false for a snapshot-only book — a snapshot alone is prior history")
	}
}

func TestRebuilder_NoSnapshotInBook_LoaderNotInvoked(t *testing.T) {
	loaderCalls := 0
	r := NewRebuilder(func() *counterState { return &counterState{} }).
		WithSnapshot(func(*counterState, *anypb.Any) error {
			loaderCalls++
			return nil
		})
	_, _, err := r.Rebuild(eventBookOf(t, &pb.Cover{Domain: "a"}))
	if err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	if loaderCalls != 0 {
		t.Errorf("snapshot loader ran %d times with no snapshot in the book", loaderCalls)
	}
}

func TestRebuilder_SnapshotWithoutState_LoaderNotInvoked(t *testing.T) {
	loaderCalls := 0
	r := NewRebuilder(func() *counterState { return &counterState{} }).
		WithSnapshot(func(*counterState, *anypb.Any) error {
			loaderCalls++
			return nil
		})
	book := &pb.EventBook{Snapshot: &pb.Snapshot{Sequence: 3}} // no State payload
	_, _, err := r.Rebuild(book)
	if err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	if loaderCalls != 0 {
		t.Errorf("snapshot loader ran %d times for a stateless snapshot", loaderCalls)
	}
}

func TestRebuilder_SnapshotWithoutLoader_IgnoredSafely(t *testing.T) {
	r := NewRebuilder(func() *counterState { return &counterState{} }).
		Apply(FullTypeName[*pb.Cover](), coverApplier(t))
	book := eventBookOf(t, &pb.Cover{Domain: "after"})
	book.Snapshot = &pb.Snapshot{State: mustAny(t, &pb.Cover{Domain: "base"})}

	state, _, err := r.Rebuild(book)
	if err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	if len(state.applied) != 1 || state.applied[0] != "after" {
		t.Fatalf("applied = %v, want [after] (no loader registered — snapshot state cannot fold)", state.applied)
	}
}

// The covered-page skip is an exact boundary: a snapshot covering through
// sequence 1 skips the sequence-1 page and nothing after it.
func TestRebuilder_CoveredBoundary_ExactSequenceSkipped(t *testing.T) {
	r := NewRebuilder(func() *counterState { return &counterState{} }).
		WithSnapshot(func(s *counterState, _ *anypb.Any) error {
			s.applied = append(s.applied, "snap")
			return nil
		}).
		Apply(FullTypeName[*pb.Cover](), coverApplier(t))

	book := &pb.EventBook{
		Snapshot: &pb.Snapshot{Sequence: 1, State: mustAny(t, &pb.Cover{})},
		Pages: []*pb.EventPage{
			{Header: &pb.PageHeader{SequenceType: &pb.PageHeader_Sequence{Sequence: 1}},
				Payload: &pb.EventPage_Event{Event: mustAny(t, &pb.Cover{Domain: "covered"})}},
			{Header: &pb.PageHeader{SequenceType: &pb.PageHeader_Sequence{Sequence: 2}},
				Payload: &pb.EventPage_Event{Event: mustAny(t, &pb.Cover{Domain: "after"})}},
		},
	}
	state, _, err := r.Rebuild(book)
	if err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	if len(state.applied) != 2 || state.applied[0] != "snap" || state.applied[1] != "after" {
		t.Fatalf("applied = %v, want [snap after] (sequence 1 covered, sequence 2 not)", state.applied)
	}
}

// A pageless entry (no event payload) is skipped, never ends the fold.
func TestRebuilder_PagelessEntries_DoNotEndTheFold(t *testing.T) {
	r := NewRebuilder(func() *counterState { return &counterState{} }).
		Apply(FullTypeName[*pb.Cover](), coverApplier(t))

	book := &pb.EventBook{Pages: []*pb.EventPage{
		{}, // no payload
		{Payload: &pb.EventPage_Event{Event: mustAny(t, &pb.Cover{Domain: "after-gap"})}},
	}}
	state, _, err := r.Rebuild(book)
	if err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	if len(state.applied) != 1 || state.applied[0] != "after-gap" {
		t.Fatalf("applied = %v, want [after-gap] (the gap page is skipped, not terminal)", state.applied)
	}
}

// --- extractRejectionKey: degenerate rejection shapes ------------------------

func TestExtractRejectionKey_DegenerateShapes(t *testing.T) {
	tests := []struct {
		name       string
		rejection  *pb.RejectionNotification
		wantDomain string
		wantType   string
	}{
		{"no rejected command", &pb.RejectionNotification{}, "", ""},
		{"no cover", &pb.RejectionNotification{
			RejectedCommand: &pb.CommandBook{Pages: []*pb.CommandPage{
				{Payload: &pb.CommandPage_Command{Command: &anypb.Any{TypeUrl: TypeURLPrefix + "test.Cmd"}}},
			}},
		}, "", "test.Cmd"},
		{"no pages", &pb.RejectionNotification{
			RejectedCommand: &pb.CommandBook{Cover: &pb.Cover{Domain: "orders"}},
		}, "orders", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			domain, cmdType := extractRejectionKey(tt.rejection)
			if domain != tt.wantDomain || cmdType != tt.wantType {
				t.Errorf("extractRejectionKey = (%q, %q), want (%q, %q)",
					domain, cmdType, tt.wantDomain, tt.wantType)
			}
		})
	}
}

// --- AggregateDispatch: envelope guards + introspection ----------------------

func expectCoded(t *testing.T, err error, code string) {
	t.Helper()
	if err == nil {
		t.Fatal("expected a coded error, got nil")
	}
	var clientErr *ClientError
	if !errors.As(err, &clientErr) {
		t.Fatalf("uncoded error: %v", err)
	}
	if clientErr.Code != code {
		t.Fatalf("Code = %q, want %q", clientErr.Code, code)
	}
}

func TestAggregateDispatch_NilPagePayload_Coded(t *testing.T) {
	d := NewAggregateDispatch("agg", "orders",
		NewRebuilder(func() *counterState { return &counterState{} }))
	_, err := d.Dispatch(&pb.ContextualCommand{
		Command: &pb.CommandBook{Pages: []*pb.CommandPage{{}}}, // page without payload
	})
	expectCoded(t, err, CodeMissingCommandPayload)
}

func TestAggregateDispatch_EmptyTypeURL_Coded(t *testing.T) {
	d := NewAggregateDispatch("agg", "orders",
		NewRebuilder(func() *counterState { return &counterState{} }))
	_, err := d.Dispatch(&pb.ContextualCommand{
		Command: &pb.CommandBook{Pages: []*pb.CommandPage{
			{Payload: &pb.CommandPage_Command{Command: &anypb.Any{}}},
		}},
	})
	expectCoded(t, err, CodeMissingCommandPayload)
}

func TestAggregateDispatch_CommandTypes_Exact(t *testing.T) {
	d := NewAggregateDispatch("agg", "orders",
		NewRebuilder(func() *counterState { return &counterState{} })).
		OnCommand("test.CreateOrder", func(*anypb.Any, *counterState, CommandContext) (*pb.EventBook, error) {
			return &pb.EventBook{}, nil
		})
	types := d.CommandTypes()
	if len(types) != 1 || types[0] != "test.CreateOrder" {
		t.Fatalf("CommandTypes = %v, want exactly [test.CreateOrder]", types)
	}
}

// --- PM/Projector introspection exactness ------------------------------------

func TestPMDispatch_Introspection_Exact(t *testing.T) {
	d := NewProcessManagerDispatch("pm", "fulfillment",
		NewRebuilder(func() *counterState { return &counterState{} })).
		OnEvent("orders", "test.OrderCreated",
			func(*anypb.Any, *counterState, *Destinations) (*pb.ProcessManagerHandleResponse, error) {
				return &pb.ProcessManagerHandleResponse{}, nil
			})

	if sources := d.Sources(); len(sources) != 1 || sources[0] != "orders" {
		t.Fatalf("Sources = %v, want exactly [orders]", sources)
	}
	subs := d.Subscriptions()
	if len(subs) != 1 || len(subs["orders"]) != 1 || subs["orders"][0] != "test.OrderCreated" {
		t.Fatalf("Subscriptions = %v, want exactly {orders: [test.OrderCreated]}", subs)
	}
}

func TestProjectorDispatch_EventTypes_Exact(t *testing.T) {
	d := NewProjectorDispatch("proj", func() *counterState { return &counterState{} }).
		OnEvent("test.OrderCreated", func(*counterState, *anypb.Any) error { return nil })
	types := d.EventTypes()
	if len(types) != 1 || types[0] != "test.OrderCreated" {
		t.Fatalf("EventTypes = %v, want exactly [test.OrderCreated]", types)
	}
}

func TestProjectorDispatch_NilBook_Coded(t *testing.T) {
	d := NewProjectorDispatch("proj", func() *counterState { return &counterState{} })
	_, err := d.Dispatch(nil)
	expectCoded(t, err, CodeMissingEventBookCover)
}

// --- CommandHandlerMux: envelope guards + routing -----------------------------

func TestCommandHandlerMux_Dispatch_GuardsAndRoutes(t *testing.T) {
	dispatched := 0
	table := NewAggregateDispatch("agg", "orders",
		NewRebuilder(func() *counterState { return &counterState{} })).
		OnCommand("test.CreateOrder", func(*anypb.Any, *counterState, CommandContext) (*pb.EventBook, error) {
			dispatched++
			return &pb.EventBook{}, nil
		})
	mux, err := ComposeCommandHandlers(table)
	if err != nil {
		t.Fatalf("ComposeCommandHandlers: %v", err)
	}

	t.Run("nil request", func(t *testing.T) {
		_, err := mux.Dispatch(nil)
		expectCoded(t, err, CodeMissingCommandBook)
	})
	t.Run("nil command book", func(t *testing.T) {
		_, err := mux.Dispatch(&pb.ContextualCommand{})
		expectCoded(t, err, CodeMissingCommandBook)
	})
	t.Run("empty pages", func(t *testing.T) {
		_, err := mux.Dispatch(&pb.ContextualCommand{Command: &pb.CommandBook{}})
		expectCoded(t, err, CodeMissingCommandBook)
	})
	t.Run("nil page payload", func(t *testing.T) {
		_, err := mux.Dispatch(&pb.ContextualCommand{
			Command: &pb.CommandBook{Pages: []*pb.CommandPage{{}}},
		})
		expectCoded(t, err, CodeMissingCommandPayload)
	})
	t.Run("routes the first page's command to the claiming handler", func(t *testing.T) {
		_, err := mux.Dispatch(&pb.ContextualCommand{
			Command: &pb.CommandBook{
				Cover: &pb.Cover{Domain: "orders"},
				Pages: []*pb.CommandPage{
					{Payload: &pb.CommandPage_Command{Command: &anypb.Any{
						TypeUrl: TypeURLPrefix + "test.CreateOrder",
					}}},
				},
			},
		})
		if err != nil {
			t.Fatalf("Dispatch: %v", err)
		}
		if dispatched != 1 {
			t.Fatalf("handler invoked %d times, want 1", dispatched)
		}
	})
}

// --- RouterBuilder: each single-kind configuration builds its own fan-out ----

type stubPM struct{ calls int }

func (s *stubPM) Name() string { return "stub-pm" }
func (s *stubPM) Dispatch(_, _ *pb.EventBook, _ map[string]uint32) (*pb.ProcessManagerHandleResponse, error) {
	s.calls++
	return &pb.ProcessManagerHandleResponse{}, nil
}

type stubProjector struct{ calls int }

func (s *stubProjector) Name() string { return "stub-projector" }
func (s *stubProjector) Dispatch(*pb.EventBook) (*pb.Projection, error) {
	s.calls++
	return &pb.Projection{Projector: s.Name()}, nil
}

func TestRouterBuilder_PMOnly_BuildsPMFanout(t *testing.T) {
	router, err := NewRouterBuilder().Register(&stubPM{}).Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if router.PMFanout == nil {
		t.Fatal("expected a PM-routing router")
	}
	if router.CommandMux != nil || router.SagaFanout != nil || router.ProjectorFanout != nil {
		t.Fatal("a PM-only configuration must populate only the PM fan-out")
	}
}

func TestRouterBuilder_ProjectorOnly_BuildsProjectorFanout(t *testing.T) {
	router, err := NewRouterBuilder().Register(&stubProjector{}).Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if router.ProjectorFanout == nil {
		t.Fatal("expected a projector-routing router")
	}
	if router.CommandMux != nil || router.SagaFanout != nil || router.PMFanout != nil {
		t.Fatal("a projector-only configuration must populate only the projector fan-out")
	}
}

// --- Every-page walks: skipped pages never terminate the walk ----------------

func sagaCountingTable(dispatched *int) *SagaDispatch {
	return NewSagaDispatch("saga", "orders", "inventory").
		OnEvent("test.OrderCreated", func(*anypb.Any, *Destinations) ([]*pb.CommandBook, []*pb.EventBook, error) {
			*dispatched++
			return []*pb.CommandBook{{}}, nil, nil
		})
}

func TestSagaDispatch_NilEventPage_DoesNotEndTheWalk(t *testing.T) {
	dispatched := 0
	d := sagaCountingTable(&dispatched)
	source := &pb.EventBook{Cover: &pb.Cover{Domain: "orders"}, Pages: []*pb.EventPage{
		{}, // no payload
		{Payload: &pb.EventPage_Event{Event: &anypb.Any{TypeUrl: TypeURLPrefix + "test.OrderCreated"}}},
	}}
	if _, err := d.Dispatch(source, nil); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if dispatched != 1 {
		t.Fatalf("dispatched = %d, want the page after the gap to fire", dispatched)
	}
}

// Spec C-0051: an undeclared type is skipped; the walk continues to later
// pages.
func TestSagaDispatch_UndeclaredType_DoesNotEndTheWalk(t *testing.T) {
	dispatched := 0
	d := sagaCountingTable(&dispatched)
	source := &pb.EventBook{Cover: &pb.Cover{Domain: "orders"}, Pages: []*pb.EventPage{
		{Payload: &pb.EventPage_Event{Event: &anypb.Any{TypeUrl: TypeURLPrefix + "test.Undeclared"}}},
		{Payload: &pb.EventPage_Event{Event: &anypb.Any{TypeUrl: TypeURLPrefix + "test.OrderCreated"}}},
	}}
	if _, err := d.Dispatch(source, nil); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if dispatched != 1 {
		t.Fatalf("dispatched = %d, want the declared page after the undeclared one to fire", dispatched)
	}
}

func TestSagaDispatch_RejectionPage_WalkContinues(t *testing.T) {
	fq := "angzarr_client.proto.examples.v1.ReserveStock"
	dispatched, compensated := 0, 0
	d := sagaCountingTable(&dispatched).
		OnRejected(fq, func(*pb.Notification, *pb.RejectionNotification) ([]*pb.EventBook, error) {
			compensated++
			return []*pb.EventBook{{}}, nil
		})
	source := &pb.EventBook{Cover: &pb.Cover{Domain: "orders"}, Pages: []*pb.EventPage{
		notificationPageFor(t, fq),
		{Payload: &pb.EventPage_Event{Event: &anypb.Any{TypeUrl: TypeURLPrefix + "test.OrderCreated"}}},
	}}
	resp, err := d.Dispatch(source, nil)
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if compensated != 1 || dispatched != 1 {
		t.Fatalf("compensated = %d, dispatched = %d — a rejection page must not end the walk", compensated, dispatched)
	}
	if len(resp.Events) != 1 || len(resp.Commands) != 1 {
		t.Fatalf("events = %d, commands = %d, want both the compensation and the command", len(resp.Events), len(resp.Commands))
	}
}

func TestProjectorDispatch_NilEventPage_DoesNotEndTheFold(t *testing.T) {
	folded := 0
	d := NewProjectorDispatch("proj", func() *counterState { return &counterState{} }).
		OnEvent("test.OrderCreated", func(*counterState, *anypb.Any) error {
			folded++
			return nil
		})
	book := &pb.EventBook{Cover: &pb.Cover{Domain: "orders"}, Pages: []*pb.EventPage{
		{}, // no payload
		{Payload: &pb.EventPage_Event{Event: &anypb.Any{TypeUrl: TypeURLPrefix + "test.OrderCreated"}}},
	}}
	if _, err := d.Dispatch(book); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if folded != 1 {
		t.Fatalf("folded = %d, want the page after the gap to fold", folded)
	}
}

func TestProjectorDispatch_UnknownType_DoesNotEndTheFold(t *testing.T) {
	folded := 0
	d := NewProjectorDispatch("proj", func() *counterState { return &counterState{} }).
		OnUnknown(func(string) {}).
		OnEvent("test.OrderCreated", func(*counterState, *anypb.Any) error {
			folded++
			return nil
		})
	book := &pb.EventBook{Cover: &pb.Cover{Domain: "orders"}, Pages: []*pb.EventPage{
		{Payload: &pb.EventPage_Event{Event: &anypb.Any{TypeUrl: TypeURLPrefix + "test.Unknown"}}},
		{Payload: &pb.EventPage_Event{Event: &anypb.Any{TypeUrl: TypeURLPrefix + "test.OrderCreated"}}},
	}}
	if _, err := d.Dispatch(book); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if folded != 1 {
		t.Fatalf("folded = %d, want the declared page after the unknown one to fold", folded)
	}
}

// The correlation ID flows through all commands/events (Cover contract):
// commands a saga emits carry the source book's correlation unless the
// handler stamped their cover itself — fill-only, like the aggregate's
// ext/sequence stamping.
func TestSagaDispatch_FillOnlyCorrelationOnEmittedCommands(t *testing.T) {
	d := NewSagaDispatch("saga", "orders", "inventory").
		OnEvent("test.OrderCreated", func(*anypb.Any, *Destinations) ([]*pb.CommandBook, []*pb.EventBook, error) {
			return []*pb.CommandBook{
				{Cover: &pb.Cover{Domain: "inventory"}},                            // blank correlation — filled
				{Cover: &pb.Cover{Domain: "inventory", CorrelationId: "explicit"}}, // handler-set — preserved
				{}, // no cover at all — created and filled
			}, nil, nil
		})
	source := &pb.EventBook{
		Cover: &pb.Cover{Domain: "orders", CorrelationId: "workflow-7"},
		Pages: []*pb.EventPage{
			{Payload: &pb.EventPage_Event{Event: &anypb.Any{TypeUrl: TypeURLPrefix + "test.OrderCreated"}}},
		},
	}
	resp, err := d.Dispatch(source, nil)
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if len(resp.Commands) != 3 {
		t.Fatalf("commands = %d, want 3", len(resp.Commands))
	}
	if got := resp.Commands[0].GetCover().GetCorrelationId(); got != "workflow-7" {
		t.Errorf("blank correlation = %q, want the source's workflow-7 filled in", got)
	}
	if got := resp.Commands[1].GetCover().GetCorrelationId(); got != "explicit" {
		t.Errorf("handler-set correlation = %q, want explicit preserved (fill-only)", got)
	}
	if got := resp.Commands[2].GetCover().GetCorrelationId(); got != "workflow-7" {
		t.Errorf("coverless command correlation = %q, want workflow-7 on a created cover", got)
	}
}

// --- Fan-out merge semantics --------------------------------------------------

type notifyingPM struct {
	stubPM
	notification *pb.Notification
}

func (n *notifyingPM) Dispatch(_, _ *pb.EventBook, _ map[string]uint32) (*pb.ProcessManagerHandleResponse, error) {
	n.calls++
	return &pb.ProcessManagerHandleResponse{Notification: n.notification}, nil
}

func TestPMFanout_AllTablesRun_FirstNotificationWins(t *testing.T) {
	first := &notifyingPM{notification: &pb.Notification{Cover: &pb.Cover{Domain: "first"}}}
	second := &notifyingPM{notification: &pb.Notification{Cover: &pb.Cover{Domain: "second"}}}

	resp, err := ComposePMs(first, second).Dispatch(&pb.EventBook{}, nil, nil)
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if first.calls != 1 || second.calls != 1 {
		t.Fatalf("calls = (%d, %d), want every composed PM to run", first.calls, second.calls)
	}
	if got := resp.Notification.GetCover().GetDomain(); got != "first" {
		t.Fatalf("Notification from %q, want the first escalation to win", got)
	}
}

func TestProjectorFanout_OneProjectionPerTable(t *testing.T) {
	a, b := &stubProjector{}, &stubProjector{}
	projections, err := ComposeProjectors(a, b).Dispatch(&pb.EventBook{Cover: &pb.Cover{Domain: "orders"}})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if len(projections) != 2 {
		t.Fatalf("projections = %d, want exactly one per composed projector", len(projections))
	}
	if a.calls != 1 || b.calls != 1 {
		t.Fatalf("calls = (%d, %d), want every composed projector to run", a.calls, b.calls)
	}
}
