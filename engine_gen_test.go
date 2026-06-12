package angzarr_test

// Round-trip proof for the generated dispatch wiring (P2): a hand-written
// typed business handler implements the generated strict interface; books
// flow through the generated table into typed methods — no Any, no
// type-URLs, no gRPC anywhere in business code. This is also the
// in-process entry the godog harness uses (no server stand-up).

import (
	"testing"

	angzarr "github.com/benjaminabbitt/angzarr/client/go"
	pb "github.com/benjaminabbitt/angzarr/client/go/proto/angzarr_client/proto/angzarr/v1"
	examples "github.com/benjaminabbitt/angzarr/client/go/proto/angzarr_client/proto/examples/v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
)

// tableHandSaga implements the GENERATED TableHandSagaHandler interface.
type tableHandSaga struct {
	sawHandNumber int64
	sawSeq        uint32
	compensated   bool
}

func (s *tableHandSaga) HandleHandStarted(event *examples.HandStarted, dests *angzarr.Destinations) ([]*pb.CommandBook, []*pb.EventBook, error) {
	s.sawHandNumber = event.HandNumber
	s.sawSeq, _ = dests.SequenceFor("hand")
	return []*pb.CommandBook{{Cover: &pb.Cover{Domain: "hand"}}}, nil, nil
}

func (s *tableHandSaga) OnDealCardsRejected(n *pb.Notification, rejection *pb.RejectionNotification) ([]*pb.EventBook, error) {
	s.compensated = true
	return []*pb.EventBook{{}}, nil
}

func packAny(t *testing.T, m proto.Message) *anypb.Any {
	t.Helper()
	packed, err := anypb.New(m)
	if err != nil {
		t.Fatalf("anypb.New: %v", err)
	}
	return packed
}

func TestGenerated_SagaRoundTrip_TypedSeam(t *testing.T) {
	handler := &tableHandSaga{}
	table := examples.NewTableHandSagaDispatch(handler)

	source := &pb.EventBook{
		Cover: &pb.Cover{Domain: "table"},
		Pages: []*pb.EventPage{
			{Payload: &pb.EventPage_Event{Event: packAny(t, &examples.HandStarted{HandNumber: 42})}},
		},
	}
	resp, err := table.Dispatch(source, map[string]uint32{"hand": 7})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if handler.sawHandNumber != 42 {
		t.Fatalf("typed event lost payload: hand_number = %d", handler.sawHandNumber)
	}
	if handler.sawSeq != 7 {
		t.Fatalf("destinations did not reach the seam: seq = %d", handler.sawSeq)
	}
	if len(resp.Commands) != 1 {
		t.Fatalf("commands = %d, want 1", len(resp.Commands))
	}
	// Declaration-derived metadata.
	if table.InputDomain() != "table" {
		t.Errorf("InputDomain = %q", table.InputDomain())
	}
	subs := table.Subscriptions()
	if len(subs["table"]) != 1 || subs["table"][0] != "angzarr_client.proto.examples.v1.HandStarted" {
		t.Errorf("Subscriptions = %v", subs)
	}
}

func TestGenerated_SagaRejection_FQRouted(t *testing.T) {
	handler := &tableHandSaga{}
	table := examples.NewTableHandSagaDispatch(handler)

	rejection := &pb.RejectionNotification{
		RejectedCommand: &pb.CommandBook{
			Cover: &pb.Cover{Domain: "hand"},
			Pages: []*pb.CommandPage{
				{Payload: &pb.CommandPage_Command{Command: packAny(t, &examples.DealCards{})}},
			},
		},
	}
	notification := &pb.Notification{Payload: packAny(t, rejection)}
	source := &pb.EventBook{
		Cover: &pb.Cover{Domain: "table"},
		Pages: []*pb.EventPage{
			{Payload: &pb.EventPage_Event{Event: packAny(t, notification)}},
		},
	}

	resp, err := table.Dispatch(source, nil)
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if !handler.compensated {
		t.Fatal("FQ-declared rejection did not reach the typed compensation method")
	}
	if len(resp.Events) != 1 {
		t.Fatalf("compensation events = %d, want 1", len(resp.Events))
	}
}

// tableAggregate implements the GENERATED TableAggregateHandler interface.
type tableAggregate struct {
	sawTables int
	sawCctx   angzarr.CommandContext
}

func (a *tableAggregate) HandleCreateTable(cmd *examples.CreateTable, state *examples.TableState, cctx angzarr.CommandContext) (*pb.EventBook, error) {
	a.sawTables = len(state.Seats)
	a.sawCctx = cctx
	return &pb.EventBook{Pages: []*pb.EventPage{{}}}, nil
}

func (a *tableAggregate) HandleStartHand(cmd *examples.StartHand, state *examples.TableState, cctx angzarr.CommandContext) (*pb.EventBook, error) {
	return &pb.EventBook{}, nil
}

func (a *tableAggregate) ApplyTableCreated(state *examples.TableState, event *examples.TableCreated) {
	state.Seats = append(state.Seats, &examples.Seat{})
}

func (a *tableAggregate) ApplyHandStarted(state *examples.TableState, event *examples.HandStarted) {}

func (a *tableAggregate) OnDealCardsRejected(n *pb.Notification, rejection *pb.RejectionNotification, state *examples.TableState, cctx angzarr.CommandContext) (*pb.BusinessResponse, error) {
	return &pb.BusinessResponse{}, nil
}

func TestGenerated_AggregateRoundTrip_StateRebuilt(t *testing.T) {
	handler := &tableAggregate{}
	table := examples.NewTableAggregateDispatch(handler)

	cmd := &pb.ContextualCommand{
		Command: &pb.CommandBook{Pages: []*pb.CommandPage{
			{Payload: &pb.CommandPage_Command{Command: packAny(t, &examples.CreateTable{})}},
		}},
		Events: &pb.EventBook{
			NextSequence: 1,
			Pages: []*pb.EventPage{
				{Payload: &pb.EventPage_Event{Event: packAny(t, &examples.TableCreated{})}},
			},
		},
	}

	resp, err := table.Dispatch(cmd)
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if resp.GetEvents() == nil {
		t.Fatal("expected events result")
	}
	if handler.sawTables != 1 {
		t.Fatalf("state saw %d applied events, want 1 (generated applier wiring)", handler.sawTables)
	}
	if !handler.sawCctx.HadPriorEvents {
		t.Error("HadPriorEvents = false with prior history")
	}
}
