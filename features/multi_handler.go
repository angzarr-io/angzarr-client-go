package features

// multi_handler.go — step bindings for client/multi_handler.feature
// (C-0010..C-0015, C-0087), driven through the REAL engine composition
// layer: ComposeCommandHandlers rejects duplicate (domain, command)
// claims at build time (audit #18); ComposeSagas/ComposePMs/
// ComposeProjectors fan out in registration order.

import (
	"fmt"

	angzarr "github.com/benjaminabbitt/angzarr/client/go"
	pb "github.com/benjaminabbitt/angzarr/client/go/proto/angzarr_client/proto/angzarr/v1"
	"github.com/cucumber/godog"
	"google.golang.org/protobuf/types/known/anypb"
)

type multiState struct{}

// MultiHandlerContext holds per-scenario state for multi_handler.feature.
type MultiHandlerContext struct {
	chTables []*angzarr.AggregateDispatch[*multiState]
	buildErr error

	sagaHandled map[string]int    // saga name → dispatch count
	sagaTarget  map[string]string // saga name → emit target domain

	pmFanout *angzarr.PMFanout
	pmResp   *pb.ProcessManagerHandleResponse

	projLogs map[string]*int
	projFan  *angzarr.ProjectorFanout
}

func newMultiHandlerContext() *MultiHandlerContext {
	return &MultiHandlerContext{
		sagaHandled: map[string]int{},
		projLogs:    map[string]*int{},
	}
}

func (m *MultiHandlerContext) chTable(name, domain string, cmdTypes ...string) *angzarr.AggregateDispatch[*multiState] {
	table := angzarr.NewAggregateDispatch(name, domain,
		angzarr.NewRebuilder(func() *multiState { return &multiState{} }))
	for _, fqType := range cmdTypes {
		table.OnCommand(fqType, func(*anypb.Any, *multiState, angzarr.CommandContext) (*pb.EventBook, error) {
			return &pb.EventBook{}, nil
		})
	}
	return table
}

// countingSaga emits one command for the target and counts handling.
func (m *MultiHandlerContext) countingSaga(name, source, target string) *angzarr.SagaDispatch {
	return angzarr.NewSagaDispatch(name, source, target).
		OnEvent(fqOrderCreated, func(*anypb.Any, *angzarr.Destinations) ([]*pb.CommandBook, []*pb.EventBook, error) {
			m.sagaHandled[name]++
			if target == "" {
				return nil, nil, nil
			}
			return []*pb.CommandBook{{Cover: &pb.Cover{Domain: target}}}, nil, nil
		})
}

// InitMultiHandlerSteps registers multi_handler.feature step definitions.
func InitMultiHandlerSteps(ctx *godog.ScenarioContext) {
	m := newMultiHandlerContext()

	// --- CommandHandler uniqueness (C-0010..C-0012) ---
	ctx.Step(`^two command handlers Alpha and Beta for domain "([^"]*)"$`, func(domain string) error {
		m.chTables = []*angzarr.AggregateDispatch[*multiState]{
			m.chTable("Alpha", domain), m.chTable("Beta", domain),
		}
		return nil
	})
	ctx.Step(`^both handle CreateOrder$`, func() error {
		for _, t := range m.chTables {
			t.OnCommand(fqCreateOrder, func(*anypb.Any, *multiState, angzarr.CommandContext) (*pb.EventBook, error) {
				return &pb.EventBook{}, nil
			})
		}
		return nil
	})
	ctx.Step(`^a command handler Alpha for domain "([^"]*)" handling CreateOrder$`, func(domain string) error {
		m.chTables = append(m.chTables, m.chTable("Alpha", domain, fqCreateOrder))
		return nil
	})
	ctx.Step(`^a command handler Beta for domain "([^"]*)" handling CreateOrder$`, func(domain string) error {
		m.chTables = append(m.chTables, m.chTable("Beta", domain, fqCreateOrder))
		return nil
	})
	ctx.Step(`^a command handler Player for domain "([^"]*)" handling RegisterPlayer and DepositFunds$`, func(domain string) error {
		m.chTables = append(m.chTables, m.chTable("Player", domain, "test.RegisterPlayer", "test.DepositFunds"))
		return nil
	})
	buildMux := func() error {
		dispatchers := make([]angzarr.CommandDispatcher, len(m.chTables))
		for i, t := range m.chTables {
			dispatchers[i] = t
		}
		_, m.buildErr = angzarr.ComposeCommandHandlers(dispatchers...)
		return nil
	}
	ctx.Step(`^the router is built with Alpha then Beta$`, buildMux)
	ctx.Step(`^the router is built with Alpha then Beta across domains$`, buildMux)
	ctx.Step(`^the router is built with Player$`, buildMux)
	ctx.Step(`^registration is rejected because two command handlers claim CreateOrder in "([^"]*)"$`, func(domain string) error {
		clientErr := angzarr.AsClientError(m.buildErr)
		if clientErr == nil || clientErr.Code != angzarr.CodeDuplicateCommandHandler {
			return fmt.Errorf("want coded %s at build, got %v", angzarr.CodeDuplicateCommandHandler, m.buildErr)
		}
		if clientErr.Extras[angzarr.ExtraKeyDomain] != domain {
			return fmt.Errorf("claim domain = %v, want %s", clientErr.Extras, domain)
		}
		return nil
	})
	ctx.Step(`^the configuration is accepted$`, func() error {
		if m.buildErr != nil {
			return fmt.Errorf("build failed: %v", m.buildErr)
		}
		return nil
	})

	// --- Saga fan-out (C-0013, C-0087) ---
	ctx.Step(`^two sagas SagaA and SagaB both listening to source "([^"]*)" for OrderCreated$`, func(source string) error {
		currentSaga.from = source
		currentSaga.fanout = angzarr.ComposeSagas(
			m.countingSaga("SagaA", source, ""),
			m.countingSaga("SagaB", source, ""),
		)
		return nil
	})
	ctx.Step(`^SagaA emits a ReserveStock command for "([^"]*)"$`, func(target string) error {
		currentSaga.fanout = nil // rebuilt below with targets
		m.sagaTargets("SagaA", target)
		return nil
	})
	ctx.Step(`^SagaB emits a CreateShipment command for "([^"]*)"$`, func(target string) error {
		m.sagaTargets("SagaB", target)
		return nil
	})
	ctx.Step(`^the saga router is built with SagaA then SagaB$`, func() error {
		if currentSaga.fanout == nil {
			currentSaga.fanout = angzarr.ComposeSagas(
				m.countingSaga("SagaA", currentSaga.from, m.targetOf("SagaA")),
				m.countingSaga("SagaB", currentSaga.from, m.targetOf("SagaB")),
			)
		}
		return nil
	})
	ctx.Step(`^the response contains two commands in registration order$`, func() error {
		resp := currentSaga.resp
		if currentPM != nil && currentPM.resp != nil {
			if got := len(currentPM.resp.GetCommands()); got != 2 {
				return fmt.Errorf("PM commands = %d, want 2", got)
			}
			return nil
		}
		if got := len(resp.GetCommands()); got != 2 {
			return fmt.Errorf("commands = %d, want 2", got)
		}
		return nil
	})
	ctx.Step(`^the first command targets the "([^"]*)" domain$`, func(domain string) error {
		if got := currentSaga.resp.GetCommands()[0].GetCover().GetDomain(); got != domain {
			return fmt.Errorf("first command targets %q, want %q (registration order)", got, domain)
		}
		return nil
	})
	ctx.Step(`^the second command targets the "([^"]*)" domain$`, func(domain string) error {
		if got := currentSaga.resp.GetCommands()[1].GetCover().GetDomain(); got != domain {
			return fmt.Errorf("second command targets %q, want %q (registration order)", got, domain)
		}
		return nil
	})
	ctx.Step(`^each saga handles the event exactly once$`, func() error {
		for _, name := range []string{"SagaA", "SagaB"} {
			if got := m.sagaHandled[name]; got != 1 {
				return fmt.Errorf("%s handled %d times, want exactly 1 (C-0087)", name, got)
			}
		}
		return nil
	})

	// --- PM fan-out (C-0014) ---
	ctx.Step(`^two process managers PMA and PMB both sourcing from "([^"]*)" and handling OrderCreated$`, func(source string) error {
		pm := func(name, target string) *angzarr.ProcessManagerDispatch[*multiState] {
			table := angzarr.NewProcessManagerDispatch(name, name,
				angzarr.NewRebuilder(func() *multiState { return &multiState{} }))
			table.OnEvent(source, fqOrderCreated, func(*anypb.Any, *multiState, *angzarr.Destinations) (*pb.ProcessManagerHandleResponse, error) {
				return &pb.ProcessManagerHandleResponse{
					Commands: []*pb.CommandBook{{Cover: &pb.Cover{Domain: target}}},
				}, nil
			})
			return table
		}
		m.pmFanout = angzarr.ComposePMs(pm("PMA", "inventory"), pm("PMB", "fulfillment"))
		currentPM.sources = []string{source}
		return nil
	})
	ctx.Step(`^PMA emits a ReserveStock command$`, func() error { return nil })
	ctx.Step(`^PMB emits a CreateShipment command$`, func() error { return nil })
	ctx.Step(`^the PM router is built with PMA then PMB$`, func() error {
		if m.pmFanout == nil {
			return fmt.Errorf("no PM fanout configured")
		}
		// Route the shared PM trigger phrase through the fanout.
		currentPM.table = nil
		currentPM.dispatchOverride = func(domain, eventType string) error {
			trigger := &pb.EventBook{
				Cover: &pb.Cover{Domain: domain},
				Pages: []*pb.EventPage{
					{Payload: &pb.EventPage_Event{Event: &anypb.Any{TypeUrl: angzarr.TypeURLPrefix + eventType}}},
				},
			}
			currentPM.resp, currentPM.err = m.pmFanout.Dispatch(trigger, nil, nil)
			return currentPM.err
		}
		return nil
	})

	// --- Projector fan-out (C-0015) ---
	ctx.Step(`^two projectors ProjA and ProjB both consuming domain "([^"]*)"$`, func(domain string) error {
		m.projLogs["ProjA"], m.projLogs["ProjB"] = new(int), new(int)
		proj := func(name string) *angzarr.ProjectorDispatch[*multiState] {
			return angzarr.NewProjectorDispatch(name, func() *multiState { return &multiState{} }).
				ForDomains(domain)
		}
		pa, pb_ := proj("ProjA"), proj("ProjB")
		pa.OnEvent(fqOrderCreated, func(*multiState, *anypb.Any) error { *m.projLogs["ProjA"]++; return nil })
		pb_.OnEvent(fqOrderCreated, func(*multiState, *anypb.Any) error { *m.projLogs["ProjB"]++; return nil })
		m.projFan = angzarr.ComposeProjectors(pa, pb_)
		return nil
	})
	ctx.Step(`^ProjA appends to a log on OrderCreated$`, func() error { return nil })
	ctx.Step(`^ProjB appends to a different log on OrderCreated$`, func() error { return nil })
	ctx.Step(`^the projector router is built with ProjA then ProjB$`, func() error {
		if m.projFan == nil {
			return fmt.Errorf("no projector fanout")
		}
		return nil
	})
	ctx.Step(`^an EventBook with one OrderCreated event is dispatched$`, func() error {
		book := &pb.EventBook{
			Cover: &pb.Cover{Domain: "order"},
			Pages: []*pb.EventPage{
				{Payload: &pb.EventPage_Event{Event: &anypb.Any{TypeUrl: angzarr.TypeURLPrefix + fqOrderCreated}}},
			},
		}
		_, err := m.projFan.Dispatch(book)
		return err
	})
	ctx.Step(`^ProjA's log has (\d+) entry$`, func(n int) error {
		if *m.projLogs["ProjA"] != n {
			return fmt.Errorf("ProjA log = %d, want %d", *m.projLogs["ProjA"], n)
		}
		return nil
	})
	ctx.Step(`^ProjB's log has (\d+) entry$`, func(n int) error {
		if *m.projLogs["ProjB"] != n {
			return fmt.Errorf("ProjB log = %d, want %d", *m.projLogs["ProjB"], n)
		}
		return nil
	})
}

// sagaTargets records the emit target for a named saga (C-0013 setup).
func (m *MultiHandlerContext) sagaTargets(name, target string) {
	if m.projLogs == nil {
		m.projLogs = map[string]*int{}
	}
	if m.sagaTarget == nil {
		m.sagaTarget = map[string]string{}
	}
	m.sagaTarget[name] = target
}

func (m *MultiHandlerContext) targetOf(name string) string {
	return m.sagaTarget[name]
}
