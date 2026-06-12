package features

// rejected_compensation.go — step bindings for
// client/rejected_compensation.feature (C-0080..C-0084), driven through
// the REAL engine AggregateDispatch shared with rejection.go (that file
// owns the arriving-rejection phrases and the harness context).
//
// Amounts ride harness-internal payloads: a test.* type URL over a
// marshalled Projection whose Sequence field carries the value.

import (
	"fmt"

	angzarr "github.com/benjaminabbitt/angzarr/client/go"
	pb "github.com/benjaminabbitt/angzarr/client/go/proto/angzarr_client/proto/angzarr/v1"
	"github.com/cucumber/godog"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
)

const (
	fqFundsDeposited = "test.FundsDeposited"
	fqWorkflowFailed = "test.WorkflowFailed"
)

func amountPayload(typeName string, amount uint32) (*anypb.Any, error) {
	value, err := proto.Marshal(&pb.Projection{Sequence: amount})
	if err != nil {
		return nil, err
	}
	return &anypb.Any{TypeUrl: angzarr.TypeURLPrefix + typeName, Value: value}, nil
}

func amountOf(event *anypb.Any) (uint32, error) {
	carrier := &pb.Projection{}
	if err := proto.Unmarshal(event.GetValue(), carrier); err != nil {
		return 0, err
	}
	return carrier.Sequence, nil
}

func eventPageFor(typeName string, amount, sequence uint32) (*pb.EventPage, error) {
	payload, err := amountPayload(typeName, amount)
	if err != nil {
		return nil, err
	}
	return &pb.EventPage{
		Header:  &pb.PageHeader{SequenceType: &pb.PageHeader_Sequence{Sequence: sequence}},
		Payload: &pb.EventPage_Event{Event: payload},
	}, nil
}

// pagesOfType filters emitted pages by harness type name.
func pagesOfType(pages []*pb.EventPage, typeName string) []*pb.EventPage {
	var out []*pb.EventPage
	for _, page := range pages {
		if page.GetEvent().GetTypeUrl() == angzarr.TypeURLPrefix+typeName {
			out = append(out, page)
		}
	}
	return out
}

// InitRejectedCompensationSteps registers rejected_compensation.feature
// step definitions. Wired into features_test.go's InitializeScenario.
func InitRejectedCompensationSteps(ctx *godog.ScenarioContext) {
	// --- Payment handler configurations (shared harness: rejection.go) ---

	ctx.Step(`^a command handler "Payment" for domain "([^"]*)" with stateful rejection$`, func(domain string) error {
		return currentRejection.givenComponentInDomain(domain)
	})
	ctx.Step(`^a command handler "Payment" for domain "([^"]*)" with two compensation handlers$`, func(domain string) error {
		return currentRejection.givenComponentInDomain(domain)
	})
	ctx.Step(`^a command handler "Payment" for domain "([^"]*)" with no rejection handlers$`, func(domain string) error {
		return currentRejection.givenComponentInDomain(domain)
	})

	ctx.Step(`^deposits update Payment's bankroll$`, func() error {
		currentRejection.rebuilder.Apply(fqFundsDeposited, func(state *rejectionState, payload *anypb.Any) error {
			amount, err := amountOf(payload)
			if err != nil {
				return err
			}
			state.Bankroll += amount
			return nil
		})
		return nil
	})

	ctx.Step(`^Payment compensates a rejected ReserveStock from inventory by emitting FundsReleased with the current bankroll$`, func() error {
		currentRejection.table.OnRejected(fqReserveStock, func(_ *pb.Notification, _ *pb.RejectionNotification, state *rejectionState, cctx angzarr.CommandContext) (*pb.BusinessResponse, error) {
			page, err := eventPageFor(fqFundsReleased, state.Bankroll, cctx.NextSequence)
			if err != nil {
				return nil, err
			}
			return &pb.BusinessResponse{Result: &pb.BusinessResponse_Events{
				Events: &pb.EventBook{Pages: []*pb.EventPage{page}},
			}}, nil
		})
		return nil
	})
	ctx.Step(`^Payment compensates a rejected ReserveStock from inventory by emitting FundsReleased$`, func() error {
		return currentRejection.givenCompensatesReserveStock()
	})
	ctx.Step(`^Payment compensates a rejected ProcessPayment from payment by emitting WorkflowFailed$`, func() error {
		currentRejection.table.OnRejected(fqProcessPayment, func(_ *pb.Notification, _ *pb.RejectionNotification, _ *rejectionState, cctx angzarr.CommandContext) (*pb.BusinessResponse, error) {
			page, err := eventPageFor(fqWorkflowFailed, 0, cctx.NextSequence)
			if err != nil {
				return nil, err
			}
			return &pb.BusinessResponse{Result: &pb.BusinessResponse_Events{
				Events: &pb.EventBook{Pages: []*pb.EventPage{page}},
			}}, nil
		})
		return nil
	})
	ctx.Step(`^Payment compensates a rejected ReserveStock from inventory by emitting two FundsReleased events$`, func() error {
		currentRejection.table.OnRejected(fqReserveStock, func(_ *pb.Notification, _ *pb.RejectionNotification, state *rejectionState, cctx angzarr.CommandContext) (*pb.BusinessResponse, error) {
			first, err := eventPageFor(fqFundsReleased, state.Bankroll, cctx.NextSequence)
			if err != nil {
				return nil, err
			}
			second, err := eventPageFor(fqFundsReleased, state.Bankroll, cctx.NextSequence+1)
			if err != nil {
				return nil, err
			}
			return &pb.BusinessResponse{Result: &pb.BusinessResponse_Events{
				Events: &pb.EventBook{Pages: []*pb.EventPage{first, second}},
			}}, nil
		})
		return nil
	})
	ctx.Step(`^Payment is configured$`, func() error {
		if currentRejection.table == nil {
			return fmt.Errorf("no Payment handler configured")
		}
		return nil
	})

	// --- Prior history ---

	ctx.Step(`^a prior history with a FundsDeposited event of bankroll (\d+)$`, func(amount int) error {
		page, err := eventPageFor(fqFundsDeposited, uint32(amount), 0)
		if err != nil {
			return err
		}
		currentRejection.priorEvents = &pb.EventBook{
			NextSequence: 1,
			Pages:        []*pb.EventPage{page},
		}
		return nil
	})
	ctx.Step(`^a prior history ending at sequence (\d+)$`, func(seq int) error {
		page, err := eventPageFor(fqFundsDeposited, 0, uint32(seq))
		if err != nil {
			return err
		}
		currentRejection.priorEvents = &pb.EventBook{
			NextSequence: uint32(seq) + 1,
			Pages:        []*pb.EventPage{page},
		}
		return nil
	})

	// --- Arriving rejections not owned by rejection.go ---

	ctx.Step(`^a rejection of ProcessPayment arrives from payment$`, func() error {
		return currentRejection.whenRejectionArrives(fqProcessPayment)
	})
	ctx.Step(`^a rejection of CreateShipment arrives from fulfillment$`, func() error {
		return currentRejection.whenRejectionArrives("test.CreateShipment")
	})

	// --- Response assertions ---

	ctx.Step(`^the response contains one FundsReleased event$`, func() error {
		if got := len(pagesOfType(currentRejection.emittedPages(), fqFundsReleased)); got != 1 {
			return fmt.Errorf("FundsReleased events = %d, want 1", got)
		}
		return nil
	})
	ctx.Step(`^the FundsReleased event carries amount (\d+)$`, func(amount int) error {
		pages := pagesOfType(currentRejection.emittedPages(), fqFundsReleased)
		if len(pages) != 1 {
			return fmt.Errorf("FundsReleased events = %d, want 1", len(pages))
		}
		got, err := amountOf(pages[0].GetEvent())
		if err != nil {
			return err
		}
		if got != uint32(amount) {
			return fmt.Errorf("amount = %d, want %d (state rebuilt before compensation)", got, amount)
		}
		return nil
	})
	ctx.Step(`^the response contains one WorkflowFailed event$`, func() error {
		if got := len(pagesOfType(currentRejection.emittedPages(), fqWorkflowFailed)); got != 1 {
			return fmt.Errorf("WorkflowFailed events = %d, want 1", got)
		}
		return nil
	})
	ctx.Step(`^no FundsReleased event is emitted$`, func() error {
		if got := len(pagesOfType(currentRejection.emittedPages(), fqFundsReleased)); got != 0 {
			return fmt.Errorf("FundsReleased events = %d, want 0 (no cross-fire)", got)
		}
		return nil
	})
	ctx.Step(`^the response contains no events$`, func() error {
		return currentRejection.thenNoEventsEmitted()
	})
	ctx.Step(`^compensation events are appended after sequence (\d+), taking sequences (\d+) and (\d+)$`, func(prior, first, second int) error {
		pages := currentRejection.emittedPages()
		if len(pages) != 2 {
			return fmt.Errorf("emitted %d events, want 2", len(pages))
		}
		gotFirst := pages[0].GetHeader().GetSequence()
		gotSecond := pages[1].GetHeader().GetSequence()
		if gotFirst != uint32(first) || gotSecond != uint32(second) {
			return fmt.Errorf("sequences = %d,%d, want %d,%d (appended after %d)",
				gotFirst, gotSecond, first, second, prior)
		}
		return nil
	})
}
