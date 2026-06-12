package features

// process_manager.go — step bindings for client/process_manager.feature
// (C-0020..C-0022), driven through the REAL engine ProcessManagerDispatch
// in-process: trigger = full state of the triggering domain (newest page
// fires), PM state rebuilds from its own process events, outside-sources
// triggers yield an empty response.

import (
	"fmt"

	angzarr "github.com/benjaminabbitt/angzarr/client/go"
	pb "github.com/benjaminabbitt/angzarr/client/go/proto/angzarr_client/proto/angzarr/v1"
	"github.com/cucumber/godog"
	"google.golang.org/protobuf/types/known/anypb"
)

// pmState tracks the number of completed orders the PM has seen.
type pmState struct {
	ordersSeen int
}

// PMContext holds per-scenario state for process_manager.feature.
type PMContext struct {
	name         string
	sources      []string
	target       string
	rebuilder    *angzarr.Rebuilder[*pmState]
	table        *angzarr.ProcessManagerDispatch[*pmState]
	sawState     *pmState
	processState *pb.EventBook
	// dispatchOverride reroutes the shared trigger phrase (multi_handler
	// fan-out scenarios dispatch through a PMFanout instead of the table).
	dispatchOverride func(domain, eventType string) error
	resp             *pb.ProcessManagerHandleResponse
	err              error
}

// currentPM is the active scenario's PM harness (the shared command-count
// phrases in saga.go consult it when the PM dispatched last).
var currentPM *PMContext

func newPMContext() *PMContext {
	c := &PMContext{}
	currentPM = c
	return c
}

func (c *PMContext) ensureTable() {
	if c.table == nil {
		c.rebuilder = angzarr.NewRebuilder(func() *pmState { return &pmState{} })
		c.table = angzarr.NewProcessManagerDispatch(c.name, c.name, c.rebuilder)
	}
}

func (c *PMContext) dispatchTrigger(domain, eventType string) error {
	if c.dispatchOverride != nil {
		return c.dispatchOverride(domain, eventType)
	}
	trigger := &pb.EventBook{
		Cover: &pb.Cover{Domain: domain},
		Pages: []*pb.EventPage{
			{Payload: &pb.EventPage_Event{Event: &anypb.Any{
				TypeUrl: angzarr.TypeURLPrefix + eventType,
			}}},
		},
	}
	c.resp, c.err = c.table.Dispatch(trigger, c.processState, map[string]uint32{c.target: 1})
	return c.err
}

// InitProcessManagerSteps registers process_manager.feature steps.
func InitProcessManagerSteps(ctx *godog.ScenarioContext) {
	c := newPMContext()

	// --- Background ---
	ctx.Step(`^a process manager "([^"]*)" for the fulfillment domain$`, func(name string) error {
		c.name = name
		return nil
	})
	ctx.Step(`^the PM sources from "([^"]*)" and "([^"]*)"$`, func(a, b string) error {
		c.sources = []string{a, b}
		return nil
	})
	ctx.Step(`^the PM targets "([^"]*)"$`, func(t string) error {
		c.target = t
		return nil
	})
	ctx.Step(`^the PM tracks the number of orders seen$`, func() error {
		c.ensureTable()
		return nil
	})
	ctx.Step(`^OrderCompleted advances the orders-seen count$`, func() error {
		c.ensureTable()
		c.rebuilder.Apply(fqOrderCompleted, func(state *pmState, _ *anypb.Any) error {
			state.ordersSeen++
			return nil
		})
		return nil
	})
	ctx.Step(`^the PM handles OrderCreated by emitting a ReserveStock command$`, func() error {
		c.ensureTable()
		for _, source := range c.sources {
			c.table.OnEvent(source, fqOrderCreated, func(_ *anypb.Any, state *pmState, dests *angzarr.Destinations) (*pb.ProcessManagerHandleResponse, error) {
				c.sawState = state
				return &pb.ProcessManagerHandleResponse{
					Commands: []*pb.CommandBook{{Cover: &pb.Cover{Domain: c.target}}},
				}, nil
			})
		}
		return nil
	})
	ctx.Step(`^Fulfillment is the active process manager$`, func() error {
		if c.table == nil {
			return fmt.Errorf("no PM configured")
		}
		return nil
	})

	// --- Whens ---
	ctx.Step(`^an OrderCreated trigger is dispatched to the PM router$`, func() error {
		return c.dispatchTrigger(c.sources[0], fqOrderCreated)
	})
	ctx.Step(`^a StockReserved trigger with a domain outside sources is dispatched$`, func() error {
		return c.dispatchTrigger("weather", fqStockReserved)
	})

	// --- Givens: process state ---
	ctx.Step(`^process state events: OrderCompleted, OrderCompleted$`, func() error {
		page := func() *pb.EventPage {
			return &pb.EventPage{Payload: &pb.EventPage_Event{Event: &anypb.Any{
				TypeUrl: angzarr.TypeURLPrefix + fqOrderCompleted,
			}}}
		}
		c.processState = &pb.EventBook{Pages: []*pb.EventPage{page(), page()}}
		return nil
	})

	// --- Thens ---
	ctx.Step(`^the PM has seen (\d+) completed orders$`, func(n int) error {
		if c.sawState == nil {
			return fmt.Errorf("PM handler never ran")
		}
		if c.sawState.ordersSeen != n {
			return fmt.Errorf("orders seen = %d, want %d (C-0021 state rebuild)", c.sawState.ordersSeen, n)
		}
		return nil
	})
}
