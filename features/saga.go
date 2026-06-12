package features

// saga.go — step bindings for client/saga.feature (C-0050..C-0053), driven
// through the REAL engine SagaDispatch in-process.
//
// NOTE: the phrase `^a saga "([^"]*)" translating from "([^"]*)" to "([^"]*)"$`
// and the single-command handler given are registered by
// edition_propagation.go (shared phrases); they operate on this file's
// shared currentSaga harness.
//
// Also owns the shared phrases "the response contains exactly one command",
// "the response contains no commands", and the saga-side
// "an OrderCreated event is dispatched to the saga router" (used by
// multi_handler.feature C-0013, C-0087 too).

import (
	"fmt"

	angzarr "github.com/benjaminabbitt/angzarr/client/go"
	pb "github.com/benjaminabbitt/angzarr/client/go/proto/angzarr_client/proto/angzarr/v1"
	"github.com/cucumber/godog"
	"google.golang.org/protobuf/types/known/anypb"
)

const (
	fqOrderCreated  = "test.OrderCreated"
	fqStockReserved = "test.StockReserved"
)

// SagaContext holds per-scenario state for saga.feature and the shared
// saga phrases other features reuse.
type SagaContext struct {
	name     string
	from     string
	targets  []string
	table    *angzarr.SagaDispatch
	fanout   *angzarr.SagaFanout // multi-saga scenarios (multi_handler.feature)
	observed map[string]uint32   // destination sequences seen by the handler
	destSeqs map[string]uint32
	resp     *pb.SagaResponse
	err      error
}

// currentSaga is the active scenario's saga harness (shared with
// edition_propagation.go's phrase registrations).
var currentSaga *SagaContext

func newSagaContext() *SagaContext {
	c := &SagaContext{observed: map[string]uint32{}}
	currentSaga = c
	return c
}

// configure builds the dispatch table for the declared saga.
func (c *SagaContext) configure(name, from string, targets ...string) {
	c.name, c.from, c.targets = name, from, targets
	c.table = angzarr.NewSagaDispatch(name, from, targets...)
}

// handleOrderCreatedEmit registers the OrderCreated handler emitting one
// stamped command per target domain, recording observed sequences.
func (c *SagaContext) handleOrderCreatedEmit(targets ...string) {
	c.table.OnEvent(fqOrderCreated, func(_ *anypb.Any, dests *angzarr.Destinations) ([]*pb.CommandBook, []*pb.EventBook, error) {
		// The saga observes every coordinator-supplied destination sequence.
		for _, domain := range dests.Domains() {
			if seq, ok := dests.SequenceFor(domain); ok {
				c.observed[domain] = seq
			}
		}
		var commands []*pb.CommandBook
		for _, domain := range targets {
			cmd := &pb.CommandBook{
				Cover: &pb.Cover{Domain: domain},
				Pages: []*pb.CommandPage{
					{Payload: &pb.CommandPage_Command{Command: &anypb.Any{
						TypeUrl: angzarr.TypeURLPrefix + "test.Command",
					}}},
				},
			}
			// Stamp when the coordinator supplied a sequence (scenarios
			// without a destinations table exercise emission only).
			if dests.Has(domain) {
				if err := dests.StampCommand(cmd, domain); err != nil {
					return nil, nil, err
				}
			}
			commands = append(commands, cmd)
		}
		return commands, nil, nil
	})
}

func (c *SagaContext) dispatch(eventType string) error {
	payload := &anypb.Any{TypeUrl: angzarr.TypeURLPrefix + eventType}
	source := &pb.EventBook{
		Cover: &pb.Cover{Domain: c.from},
		Pages: []*pb.EventPage{
			{Payload: &pb.EventPage_Event{Event: payload}},
		},
	}
	if c.fanout != nil {
		c.resp, c.err = c.fanout.Dispatch(source, c.destSeqs)
		return nil
	}
	c.resp, c.err = c.table.Dispatch(source, c.destSeqs)
	return nil
}

// commandTo returns the dispatched command targeting the given domain.
func (c *SagaContext) commandTo(domain string) *pb.CommandBook {
	if c.resp == nil {
		return nil
	}
	for _, cmd := range c.resp.Commands {
		if cmd.GetCover().GetDomain() == domain {
			return cmd
		}
	}
	return nil
}

// InitSagaSteps registers saga.feature step definitions. Wired into
// features_test.go's InitializeScenario.
func InitSagaSteps(ctx *godog.ScenarioContext) {
	c := newSagaContext()

	// --- Background ---

	ctx.Step(`^the router is built with the OrderFulfillment saga$`, func() error {
		if c.table == nil {
			return fmt.Errorf("no saga configured")
		}
		return nil
	})

	// --- Whens: dispatch ---

	ctx.Step(`^an OrderCreated event is dispatched to the saga router$`, func() error {
		return c.dispatch(fqOrderCreated)
	})
	ctx.Step(`^a StockReserved event is dispatched to the saga router$`, func() error {
		return c.dispatch(fqStockReserved)
	})

	// --- Thens: commands ---

	// Shared with process_manager.feature: count commands from whichever
	// harness dispatched in this scenario.
	commandCount := func() (int, error) {
		if currentPM != nil && currentPM.resp != nil {
			return len(currentPM.resp.GetCommands()), currentPM.err
		}
		if c.err != nil {
			return 0, c.err
		}
		return len(c.resp.GetCommands()), nil
	}
	ctx.Step(`^the response contains exactly one command$`, func() error {
		got, err := commandCount()
		if err != nil {
			return fmt.Errorf("dispatch failed: %v", err)
		}
		if got != 1 {
			return fmt.Errorf("commands = %d, want 1", got)
		}
		return nil
	})
	ctx.Step(`^the response contains no commands$`, func() error {
		got, err := commandCount()
		if err != nil {
			return fmt.Errorf("dispatch failed: %v", err)
		}
		if got != 0 {
			return fmt.Errorf("commands = %d, want 0", got)
		}
		return nil
	})
	ctx.Step(`^the command targets the "([^"]*)" domain$`, func(domain string) error {
		if c.commandTo(domain) == nil {
			return fmt.Errorf("no command targets %q", domain)
		}
		return nil
	})

	// --- Destinations (C-0052, C-0053) ---

	ctx.Step(`^destination sequences inventory=(\d+) and fulfillment=(\d+)$`, func(inv, ful int) error {
		c.destSeqs = map[string]uint32{"inventory": uint32(inv), "fulfillment": uint32(ful)}
		return nil
	})
	ctx.Step(`^the saga observed destination inventory = (\d+)$`, func(seq int) error {
		if got := c.observed["inventory"]; got != uint32(seq) {
			return fmt.Errorf("observed inventory = %d, want %d (C-0052)", got, seq)
		}
		return nil
	})
	ctx.Step(`^the saga observed destination fulfillment = (\d+)$`, func(seq int) error {
		if got := c.observed["fulfillment"]; got != uint32(seq) {
			return fmt.Errorf("observed fulfillment = %d, want %d (C-0052)", got, seq)
		}
		return nil
	})

	// --- Two-target saga (C-0053) ---

	ctx.Step(`^a saga "([^"]*)" translating from "([^"]*)" to "([^"]*)" and "([^"]*)"$`, func(name, from, t1, t2 string) error {
		c.configure(name, from, t1, t2)
		return nil
	})
	ctx.Step(`^the saga handles OrderCreated by emitting a ReserveStock for "([^"]*)" and a CreateShipment for "([^"]*)"$`, func(d1, d2 string) error {
		c.handleOrderCreatedEmit(d1, d2)
		return nil
	})
	ctx.Step(`^the ReserveStock command carries destination sequence (\d+)$`, func(seq int) error {
		cmd := c.commandTo("inventory")
		if cmd == nil {
			return fmt.Errorf("no inventory command")
		}
		if got := cmd.Pages[0].GetHeader().GetSequence(); got != uint32(seq) {
			return fmt.Errorf("stamped sequence = %d, want %d (C-0053)", got, seq)
		}
		return nil
	})
	ctx.Step(`^the CreateShipment command carries destination sequence (\d+)$`, func(seq int) error {
		cmd := c.commandTo("fulfillment")
		if cmd == nil {
			return fmt.Errorf("no fulfillment command")
		}
		if got := cmd.Pages[0].GetHeader().GetSequence(); got != uint32(seq) {
			return fmt.Errorf("stamped sequence = %d, want %d (C-0053)", got, seq)
		}
		return nil
	})
}
