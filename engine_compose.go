package angzarr

// Composition of multiple component tables into one process (the
// successor to the deleted router_aggregator, rebuilt on the engine).
//
// Audit #18 (spec C-0010..C-0015): exactly one CommandHandler owns a
// given (domain, command type) pair — duplicates are rejected at BUILD
// time, never resolved silently at dispatch. Sagas, process managers,
// and projectors legitimately fan out: every matching component runs,
// and outputs merge in registration order so downstream effects are
// deterministic.

import (
	"fmt"

	pb "github.com/benjaminabbitt/angzarr/client/go/proto/angzarr_client/proto/angzarr/v1"
)

// CommandDispatcher is the composition-facing surface of an aggregate
// table (satisfied by *AggregateDispatch[S] for any S).
type CommandDispatcher interface {
	Name() string
	Domain() string
	CommandTypes() []string
	Dispatch(req *pb.ContextualCommand) (*pb.BusinessResponse, error)
}

// CommandHandlerMux routes commands to the single component owning the
// (domain, command type) claim.
type CommandHandlerMux struct {
	byClaim map[string]CommandDispatcher
}

func claimKey(domain, fqType string) string { return domain + "/" + fqType }

// ComposeCommandHandlers builds a mux, rejecting duplicate claims loudly
// at build time (audit #18 / C-0010).
func ComposeCommandHandlers(handlers ...CommandDispatcher) (*CommandHandlerMux, error) {
	mux := &CommandHandlerMux{byClaim: make(map[string]CommandDispatcher)}
	for _, h := range handlers {
		for _, fqType := range h.CommandTypes() {
			key := claimKey(h.Domain(), fqType)
			if prior, taken := mux.byClaim[key]; taken {
				return nil, InvalidArgumentErrorWithCode(
					CodeDuplicateCommandHandler,
					"two command handlers claim the same (domain, command) pair",
					map[string]string{
						ExtraKeyDomain:  h.Domain(),
						ExtraKeyTypeURL: fqType,
						"first":         prior.Name(),
						"second":        h.Name(),
					},
				)
			}
			mux.byClaim[key] = h
		}
	}
	return mux, nil
}

// Dispatch routes to the claiming component.
func (m *CommandHandlerMux) Dispatch(req *pb.ContextualCommand) (*pb.BusinessResponse, error) {
	if req == nil || req.Command == nil || len(req.Command.Pages) == 0 {
		return nil, InvalidArgumentErrorWithCode(CodeMissingCommandBook, ErrMsgNoCommandPages, nil)
	}
	commandAny := req.Command.Pages[0].GetCommand()
	if commandAny == nil {
		return nil, InvalidArgumentErrorWithCode(CodeMissingCommandPayload, ErrMsgNoCommandPages, nil)
	}
	key := claimKey(req.Command.GetCover().GetDomain(), TypeNameFromURL(commandAny.TypeUrl))
	h, ok := m.byClaim[key]
	if !ok {
		return nil, InvalidArgumentErrorWithCode(CodeNoHandlerRegistered,
			ErrMsgUnknownCommand, map[string]string{ExtraKeyTypeURL: commandAny.TypeUrl})
	}
	return h.Dispatch(req)
}

// SagaFanout runs every composed saga against each delivery, merging
// commands and events in registration order.
type SagaFanout struct {
	tables []*SagaDispatch
}

// ComposeSagas builds an ordered saga fan-out.
func ComposeSagas(tables ...*SagaDispatch) *SagaFanout {
	return &SagaFanout{tables: tables}
}

// Dispatch fans the source book out to every saga in registration order.
func (f *SagaFanout) Dispatch(source *pb.EventBook, destinationSequences map[string]uint32) (*pb.SagaResponse, error) {
	merged := &pb.SagaResponse{}
	for _, table := range f.tables {
		resp, err := table.Dispatch(source, destinationSequences)
		if err != nil {
			return nil, err
		}
		merged.Commands = append(merged.Commands, resp.Commands...)
		merged.Events = append(merged.Events, resp.Events...)
	}
	return merged, nil
}

// PMDispatcher is the composition-facing surface of a PM table
// (satisfied by *ProcessManagerDispatch[S] for any S).
type PMDispatcher interface {
	Name() string
	Dispatch(trigger, processState *pb.EventBook, destinationSequences map[string]uint32) (*pb.ProcessManagerHandleResponse, error)
}

// PMFanout runs every composed process manager, merging outputs in
// registration order; the first escalation Notification wins.
type PMFanout struct {
	tables []PMDispatcher
}

// ComposePMs builds an ordered PM fan-out.
func ComposePMs(tables ...PMDispatcher) *PMFanout {
	return &PMFanout{tables: tables}
}

// Dispatch fans the trigger out to every PM in registration order.
func (f *PMFanout) Dispatch(trigger, processState *pb.EventBook, destinationSequences map[string]uint32) (*pb.ProcessManagerHandleResponse, error) {
	merged := &pb.ProcessManagerHandleResponse{}
	for _, table := range f.tables {
		resp, err := table.Dispatch(trigger, processState, destinationSequences)
		if err != nil {
			return nil, err
		}
		merged.Commands = append(merged.Commands, resp.Commands...)
		merged.ProcessEvents = append(merged.ProcessEvents, resp.ProcessEvents...)
		merged.Facts = append(merged.Facts, resp.Facts...)
		if merged.Notification == nil {
			merged.Notification = resp.Notification
		}
	}
	return merged, nil
}

// ProjectorDispatcher is the composition-facing surface of a projector
// table (satisfied by *ProjectorDispatch[P] for any P).
type ProjectorDispatcher interface {
	Name() string
	Dispatch(events *pb.EventBook) (*pb.Projection, error)
}

// ProjectorFanout runs every composed projector for its side effects,
// returning each projector's projection in registration order.
type ProjectorFanout struct {
	tables []ProjectorDispatcher
}

// ComposeProjectors builds an ordered projector fan-out.
func ComposeProjectors(tables ...ProjectorDispatcher) *ProjectorFanout {
	return &ProjectorFanout{tables: tables}
}

// Dispatch fans the book out to every projector in registration order.
func (f *ProjectorFanout) Dispatch(events *pb.EventBook) ([]*pb.Projection, error) {
	projections := make([]*pb.Projection, 0, len(f.tables))
	for _, table := range f.tables {
		projection, err := table.Dispatch(events)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", table.Name(), err)
		}
		projections = append(projections, projection)
	}
	return projections, nil
}

// ----------------------------------------------------------------------------
// RouterBuilder — build-time validation for one process's components
// ----------------------------------------------------------------------------

// Router is the built routing configuration: exactly one kind populated.
type Router struct {
	CommandMux      *CommandHandlerMux
	SagaFanout      *SagaFanout
	PMFanout        *PMFanout
	ProjectorFanout *ProjectorFanout
}

// RouterBuilder collects components and validates the configuration at
// Build (spec C-0060..C-0065): empty configs, unrecognised components,
// mixed kinds, and duplicate command claims all fail loudly at build —
// never at dispatch. Each handler is introspected exactly once.
type RouterBuilder struct {
	chs        []CommandDispatcher
	sagas      []*SagaDispatch
	pms        []PMDispatcher
	projectors []ProjectorDispatcher
	err        error
}

// NewRouterBuilder builds an empty configuration.
func NewRouterBuilder() *RouterBuilder {
	return &RouterBuilder{}
}

// Register adds a component of any recognised kind. Unrecognised
// components poison the build (C-0061).
func (b *RouterBuilder) Register(component any) *RouterBuilder {
	switch c := component.(type) {
	case CommandDispatcher:
		b.chs = append(b.chs, c)
	case *SagaDispatch:
		b.sagas = append(b.sagas, c)
	case PMDispatcher:
		b.pms = append(b.pms, c)
	case ProjectorDispatcher:
		b.projectors = append(b.projectors, c)
	default:
		if b.err == nil {
			b.err = InvalidArgumentErrorWithCode(CodeHandlerUnknownKind,
				"component is not a recognised handler kind",
				map[string]string{ExtraKeyInput: fmt.Sprintf("%T", component)})
		}
	}
	return b
}

// Build validates and assembles the router.
func (b *RouterBuilder) Build() (*Router, error) {
	if b.err != nil {
		return nil, b.err
	}
	kinds := 0
	for _, populated := range []bool{
		len(b.chs) > 0, len(b.sagas) > 0, len(b.pms) > 0, len(b.projectors) > 0,
	} {
		if populated {
			kinds++
		}
	}
	if kinds == 0 {
		return nil, InvalidArgumentErrorWithCode(CodeRouterNoHandlers,
			"no handlers are registered", nil)
	}
	if kinds > 1 {
		return nil, InvalidArgumentErrorWithCode(CodeMixedHandlerKinds,
			"a router hosts exactly one handler kind", nil)
	}

	router := &Router{}
	switch {
	case len(b.chs) > 0:
		mux, err := ComposeCommandHandlers(b.chs...)
		if err != nil {
			return nil, err
		}
		router.CommandMux = mux
	case len(b.sagas) > 0:
		router.SagaFanout = ComposeSagas(b.sagas...)
	case len(b.pms) > 0:
		router.PMFanout = ComposePMs(b.pms...)
	case len(b.projectors) > 0:
		router.ProjectorFanout = ComposeProjectors(b.projectors...)
	}
	return router, nil
}

// NewBuildError stamps a build-time configuration error with a
// cross-language code and structured extras (spec HIGH-4.3).
func NewBuildError(code, message string, extras map[string]string) error {
	return InvalidArgumentErrorWithCode(code, message, extras)
}
