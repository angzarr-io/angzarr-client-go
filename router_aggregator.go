// Package angzarr — top-level Router aggregator.
//
// Spec HIGH-4.1 / `core/main/.plan/cross-language-spec.md` §4:
//
// Go's per-kind typed routers (CommandHandlerRouter[S], SagaRouter,
// ProcessManagerRouter[S], ProjectorRouter, UpcasterRouter) preserve
// compile-time type safety, but they don't enforce the cross-cutting
// invariants that Py / Rs / Ja `Router.build()` enforces:
//
//   - ROUTER_NO_HANDLERS — Build() called with no typed routers attached.
//   - MIXED_HANDLER_KINDS — Build() called with handlers from more than
//     one kind (e.g. a CommandHandler and a Saga in the same Router).
//   - DUPLICATE_COMMAND_HANDLER — two CommandHandlerRouters registered
//     for the same (domain, command_type_url) pair.
//
// The Router aggregator layers these checks on top of the typed routers
// without erasing type safety: typed routers are still constructed
// directly via NewCommandHandlerRouter / NewSagaRouter / etc. and their
// Dispatch() entry points are unchanged. The aggregator is only the
// composition / validation point.
//
// User-confirmed design from the cross-language-interpretation doc:
//
//	router := angzarr.NewRouter("orders").
//	    AddCommandHandler(orderCH).
//	    Build()
//
// Returns a Built that exposes HandlerCount / Kind / OutputDomains /
// SyncOutputDomains / HasAsyncOutputs per audit #74.
package angzarr

// Kind is the discriminator for handler kinds participating in a Router
// aggregate. Mirrors Py `Kind`/`handler_kind` enums and Rs HandlerConfig
// variants.
type Kind int

const (
	// KindUnknown is the zero value; never assigned by a real router.
	KindUnknown Kind = iota
	// KindCommandHandler — CommandHandlerRouter[S]
	KindCommandHandler
	// KindSaga — SagaRouter
	KindSaga
	// KindProcessManager — ProcessManagerRouter[S]
	KindProcessManager
	// KindProjector — ProjectorRouter
	KindProjector
	// KindUpcaster — UpcasterRouter
	KindUpcaster
)

// String returns the lowercase canonical name.
func (k Kind) String() string {
	switch k {
	case KindCommandHandler:
		return "command_handler"
	case KindSaga:
		return "saga"
	case KindProcessManager:
		return "process_manager"
	case KindProjector:
		return "projector"
	case KindUpcaster:
		return "upcaster"
	default:
		return "unknown"
	}
}

// commandHandlerEntry adapts a CommandHandlerRouter[S] to a kind-agnostic
// shape via the existing commandHandlerRouterLike interface (`Domain() +
// CommandTypes()`).
type commandHandlerEntry struct {
	router commandHandlerRouterLike
}

type sagaEntry struct {
	router *SagaRouter
}

type processManagerEntry[S any] struct {
	router *ProcessManagerRouter[S]
}

// processManagerInfo is the kind-agnostic view of a PM router used by the
// aggregator. The aggregator stores PMs as *processManagerInfo so it
// doesn't need to be parameterized over S itself.
type processManagerInfo struct {
	name          string
	pmDomain      string
	subscriptions map[string][]string
}

type projectorEntry struct {
	router *ProjectorRouter
}

type upcasterEntry2 struct {
	router *UpcasterRouter
}

// Router aggregates typed kind-routers and validates cross-cutting
// invariants at Build time. Spec HIGH-4.1.
type Router struct {
	name      string
	chs       []commandHandlerEntry
	sagas     []*SagaRouter
	pms       []processManagerInfo
	projs     []*ProjectorRouter
	upcasters []*UpcasterRouter
}

// NewRouter returns an empty aggregator with the given name. The name
// is reported back in `BuildError.details[router_name]` so cross-language
// cucumber assertions key off the same identifier.
func NewRouter(name string) *Router {
	return &Router{name: name}
}

// AddCommandHandler attaches a CommandHandlerRouter[S]. Generic helper
// since the typed router carries its state type parameter.
func (r *Router) AddCommandHandler(router commandHandlerRouterLike) *Router {
	r.chs = append(r.chs, commandHandlerEntry{router: router})
	return r
}

// AddSaga attaches a SagaRouter.
func (r *Router) AddSaga(router *SagaRouter) *Router {
	r.sagas = append(r.sagas, router)
	return r
}

// AddProcessManager attaches a ProcessManagerRouter[S]. Generic free
// function (not a method) because Router can't be parameterized over S.
func AddProcessManager[S any](r *Router, router *ProcessManagerRouter[S]) *Router {
	r.pms = append(r.pms, processManagerInfo{
		name:          router.Name(),
		pmDomain:      router.PMDomain(),
		subscriptions: router.Subscriptions(),
	})
	return r
}

// AddProjector attaches a ProjectorRouter.
func (r *Router) AddProjector(router *ProjectorRouter) *Router {
	r.projs = append(r.projs, router)
	return r
}

// AddUpcaster attaches an UpcasterRouter. Spec HIGH-4.4.
func (r *Router) AddUpcaster(router *UpcasterRouter) *Router {
	r.upcasters = append(r.upcasters, router)
	return r
}

// Built is the result of Router.Build. Mirrors Py `_BuiltRouterBase` /
// Rs `Built` enum / Ja `Built` sealed interface.
type Built interface {
	// HandlerCount returns the number of typed routers in the aggregate.
	HandlerCount() int
	// Kind returns the shared kind of every handler in the aggregate.
	Kind() Kind
	// OutputDomains returns the set of destination domains the handlers
	// emit commands / facts to. Empty for CommandHandler / Projector /
	// Upcaster (per audit #74).
	OutputDomains() []string
	// SyncOutputDomains is the subset of OutputDomains that must be
	// readiness-probed (audit #74). Default: all OutputDomains.
	SyncOutputDomains() []string
	// HasAsyncOutputs is true when at least one output domain is reached
	// via an async bus (audit #74). For Sagas / PMs this is the default.
	HasAsyncOutputs() bool
	// RouterName returns the aggregate's name.
	RouterName() string
}

type builtImpl struct {
	name            string
	kind            Kind
	handlerCount    int
	outputDomains   []string
	syncOutputs     []string
	hasAsyncOutputs bool
}

func (b *builtImpl) HandlerCount() int       { return b.handlerCount }
func (b *builtImpl) Kind() Kind              { return b.kind }
func (b *builtImpl) OutputDomains() []string { return append([]string(nil), b.outputDomains...) }
func (b *builtImpl) SyncOutputDomains() []string {
	return append([]string(nil), b.syncOutputs...)
}
func (b *builtImpl) HasAsyncOutputs() bool { return b.hasAsyncOutputs }
func (b *builtImpl) RouterName() string    { return b.name }

// Build validates and returns the aggregate.
//
// Errors raised (all carry SCREAMING_SNAKE Code + static Message +
// details map; spec HIGH-4.3):
//
//   - CodeRouterNoHandlers — no typed routers attached.
//   - CodeMixedHandlerKinds — more than one kind attached.
//   - CodeDuplicateCommandHandler — two CHs collide on (domain, type_url).
func (r *Router) Build() (Built, error) {
	kindsPresent := 0
	var kind Kind
	if len(r.chs) > 0 {
		kindsPresent++
		kind = KindCommandHandler
	}
	if len(r.sagas) > 0 {
		kindsPresent++
		kind = KindSaga
	}
	if len(r.pms) > 0 {
		kindsPresent++
		kind = KindProcessManager
	}
	if len(r.projs) > 0 {
		kindsPresent++
		kind = KindProjector
	}
	if len(r.upcasters) > 0 {
		kindsPresent++
		kind = KindUpcaster
	}

	if kindsPresent == 0 {
		return nil, NewBuildError(
			CodeRouterNoHandlers,
			MessageRouterNoHandlers,
			map[string]string{ExtraKeyRouterName: r.name},
		)
	}
	if kindsPresent > 1 {
		return nil, NewBuildError(
			CodeMixedHandlerKinds,
			MessageMixedHandlerKinds,
			map[string]string{ExtraKeyRouterName: r.name},
		)
	}

	// Per-kind validation + output-domain derivation.
	out := &builtImpl{name: r.name, kind: kind}
	switch kind {
	case KindCommandHandler:
		// DUPLICATE_COMMAND_HANDLER: (domain, type_url) uniqueness.
		seen := make(map[string]map[string]bool)
		for _, entry := range r.chs {
			domain := entry.router.Domain()
			if _, ok := seen[domain]; !ok {
				seen[domain] = make(map[string]bool)
			}
			for _, ct := range entry.router.CommandTypes() {
				if seen[domain][ct] {
					return nil, NewBuildError(
						CodeDuplicateCommandHandler,
						MessageDuplicateCommandHandler,
						map[string]string{
							ExtraKeyRouterName: r.name,
							ExtraKeyDomain:     domain,
							ExtraKeyTypeURL:    ct,
						},
					)
				}
				seen[domain][ct] = true
			}
		}
		out.handlerCount = len(r.chs)
		// CH has no output domains.
	case KindSaga:
		out.handlerCount = len(r.sagas)
		domains := make([]string, 0, len(r.sagas))
		seen := make(map[string]bool)
		for _, s := range r.sagas {
			if seen[s.TargetDomain()] {
				continue
			}
			seen[s.TargetDomain()] = true
			domains = append(domains, s.TargetDomain())
		}
		out.outputDomains = domains
		out.syncOutputs = append([]string(nil), domains...)
		out.hasAsyncOutputs = true
	case KindProcessManager:
		out.handlerCount = len(r.pms)
		// PMs report destinations through their handlers; we surface the
		// subscription source domains as a conservative output set.
		domSet := make(map[string]bool)
		for _, pm := range r.pms {
			for src := range pm.subscriptions {
				domSet[src] = true
			}
		}
		domains := make([]string, 0, len(domSet))
		for d := range domSet {
			domains = append(domains, d)
		}
		out.outputDomains = domains
		out.syncOutputs = append([]string(nil), domains...)
		out.hasAsyncOutputs = true
	case KindProjector:
		out.handlerCount = len(r.projs)
	case KindUpcaster:
		out.handlerCount = len(r.upcasters)
	}
	return out, nil
}

// NewBuildError creates a structured BuildError (a *ClientError with kind
// = ErrInvalidArgument). Spec HIGH-4.3: mirrors Py `BuildError`, Rs
// `BuildError`, Ja `BuildException`.
func NewBuildError(code, message string, details map[string]string) error {
	return InvalidArgumentErrorWithCode(code, message, details)
}
