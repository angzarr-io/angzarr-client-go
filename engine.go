// Package angzarr — the shared dispatch/rebuild engine.
//
// This is the single home of dispatch mechanics (architecture of record:
// taskloom third-heap). Generated per-component tables (buf plugin,
// reading (angzarr.v1.component)/(angzarr.v1.rejected) options) register
// type-keyed thunks here; the engine owns page walking, Notification
// detection, fully-qualified rejection routing, and corrupt-payload
// semantics. Framework rules live here exactly once — do not duplicate
// them into generated output or component adapters.
//
// gRPC routes on method path; the angzarr envelope erases payload types
// into google.protobuf.Any so the coordinator stays domain-blind. The
// dispatch tables below are the type_url-level complement to gRPC's
// method routing — do not replace them with per-command rpcs without
// redesigning the wire contract.
package angzarr

import (
	"log/slog"

	pb "github.com/benjaminabbitt/angzarr/client/go/proto/angzarr_client/proto/angzarr/v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
)

// Dispatch error message constants (cross-language stable).
const (
	ErrMsgUnknownCommand = "Unknown command type"
	ErrMsgNoCommandPages = "No command pages"
)

// extractRejectionKey extracts the source domain and FULLY-QUALIFIED
// command type name from a RejectionNotification (deep-clerk: FQ keys).
func extractRejectionKey(rejection *pb.RejectionNotification) (string, string) {
	if rejection.RejectedCommand == nil {
		return "", ""
	}
	domain := ""
	if rejection.RejectedCommand.Cover != nil {
		domain = rejection.RejectedCommand.Cover.Domain
	}
	cmdTypeName := ""
	if len(rejection.RejectedCommand.Pages) > 0 {
		if cmd := rejection.RejectedCommand.Pages[0].GetCommand(); cmd != nil {
			cmdTypeName = TypeNameFromURL(cmd.TypeUrl)
		}
	}
	return domain, cmdTypeName
}

// ----------------------------------------------------------------------------
// Rebuilder — state reconstruction from an EventBook
// ----------------------------------------------------------------------------

// ApplierThunk folds one event payload into state. Generated thunks
// unmarshal to the typed event and call the pure typed applier; decode
// errors surface here so the engine can classify them.
type ApplierThunk[S any] func(state S, payload *anypb.Any) error

// RebuildInfo reports what the rebuild consumed.
type RebuildInfo struct {
	// HadPriorEvents is true when the book carried any history (pages or
	// a snapshot) — the "does this aggregate exist" signal. It reflects
	// page presence, not applier hits.
	HadPriorEvents bool
	// AppliedCount is the number of pages whose type had a registered
	// applier.
	AppliedCount int
}

// Rebuilder reconstructs state of type S from an EventBook: snapshot
// first (when configured), then every event page in order.
//
// Error semantics (decided: taskloom cheap-hull): a persisted payload
// that fails to decode FAILS the rebuild with PERSISTED_EVENT_CORRUPT —
// commands must never validate against silently-truncated state. The
// adapter maps it to DATA_LOSS. Event types with no registered applier
// are skipped: not every event folds into state.
type Rebuilder[S any] struct {
	factory  func() S
	snapshot ApplierThunk[S]
	appliers map[string]ApplierThunk[S]
}

// NewRebuilder builds a Rebuilder whose fresh state comes from factory.
func NewRebuilder[S any](factory func() S) *Rebuilder[S] {
	return &Rebuilder[S]{
		factory:  factory,
		appliers: make(map[string]ApplierThunk[S]),
	}
}

// Apply registers the applier thunk for a fully-qualified event type name.
func (r *Rebuilder[S]) Apply(fullName string, thunk ApplierThunk[S]) *Rebuilder[S] {
	r.appliers[fullName] = thunk
	return r
}

// WithSnapshot registers the snapshot loader, applied before any pages.
func (r *Rebuilder[S]) WithSnapshot(thunk ApplierThunk[S]) *Rebuilder[S] {
	r.snapshot = thunk
	return r
}

// Rebuild folds the book into fresh state. A nil book normalizes to an
// empty one (fresh state, no prior events) — callers never need their
// own nil guard.
func (r *Rebuilder[S]) Rebuild(book *pb.EventBook) (S, RebuildInfo, error) {
	state := r.factory()
	info := RebuildInfo{}
	if book == nil {
		return state, info, nil
	}
	info.HadPriorEvents = len(book.Pages) > 0 || book.Snapshot != nil

	if book.Snapshot != nil && book.Snapshot.State != nil && r.snapshot != nil {
		if err := r.snapshot(state, book.Snapshot.State); err != nil {
			return state, info, persistedCorruptError(book.Snapshot.State.TypeUrl, err)
		}
	}

	// Pages already folded into the snapshot must not re-apply — their
	// effects are in the snapshot state; double-folding corrupts it.
	coveredThrough := uint32(0)
	if book.Snapshot != nil {
		coveredThrough = book.Snapshot.Sequence
	}

	for _, page := range book.Pages {
		event := page.GetEvent()
		if event == nil {
			continue
		}
		if coveredThrough > 0 && page.GetHeader().GetSequence() <= coveredThrough {
			continue
		}
		thunk, ok := r.appliers[TypeNameFromURL(event.TypeUrl)]
		if !ok {
			continue
		}
		if err := thunk(state, event); err != nil {
			return state, info, persistedCorruptError(event.TypeUrl, err)
		}
		info.AppliedCount++
	}
	return state, info, nil
}

// persistedCorruptError classifies a store-sourced decode failure.
// mapHandlerError turns it into DATA_LOSS on the wire.
func persistedCorruptError(typeURL string, cause error) error {
	return &ClientError{
		Kind:    ErrInvalidArgument,
		Message: MessagePersistedEventCorrupt,
		Code:    CodePersistedEventCorrupt,
		Extras:  map[string]string{ExtraKeyTypeURL: typeURL},
		Cause:   cause,
	}
}

// ----------------------------------------------------------------------------
// SagaDispatch — event → command translation table
// ----------------------------------------------------------------------------

// SagaEventThunk translates one source event into commands and/or fact
// events. Generated thunks unmarshal to the typed event and call the
// typed business method with the coordinator-supplied Destinations
// (sagas always receive next sequences — taskloom oval-query).
type SagaEventThunk func(eventAny *anypb.Any, dests *Destinations) ([]*pb.CommandBook, []*pb.EventBook, error)

// SagaRejectionThunk compensates a rejected command. Sagas return
// compensation events only (no commands on rejection).
type SagaRejectionThunk func(n *pb.Notification, rejection *pb.RejectionNotification) ([]*pb.EventBook, error)

// SagaDispatch is the dispatch table for one saga component. Generated
// code populates it from the proto declaration; hand-written code may
// populate it directly.
type SagaDispatch struct {
	name        string
	inputDomain string
	targets     []string
	handlers    map[string]SagaEventThunk       // FQ event type name
	rejections  map[string][]SagaRejectionThunk // FQ command type name (deep-clerk); ordered (C-0042)
}

// NewSagaDispatch builds an empty saga table.
func NewSagaDispatch(name, inputDomain string, targetDomains ...string) *SagaDispatch {
	return &SagaDispatch{
		name:        name,
		inputDomain: inputDomain,
		targets:     targetDomains,
		handlers:    make(map[string]SagaEventThunk),
		rejections:  make(map[string][]SagaRejectionThunk),
	}
}

// OnEvent registers the thunk for a fully-qualified event type name.
func (d *SagaDispatch) OnEvent(fullName string, thunk SagaEventThunk) *SagaDispatch {
	d.handlers[fullName] = thunk
	return d
}

// OnRejected registers a compensation thunk keyed by the FULLY-QUALIFIED
// rejected-command type name. Short names never match (decided:
// deep-clerk); the buf plugin validates declarations at generation time.
// Multiple registrations for the same command all run, in registration
// order (spec C-0042 — distinct undoings are independent).
func (d *SagaDispatch) OnRejected(fqCommandType string, thunk SagaRejectionThunk) *SagaDispatch {
	d.rejections[fqCommandType] = append(d.rejections[fqCommandType], thunk)
	return d
}

// Name returns the component name.
func (d *SagaDispatch) Name() string { return d.name }

// InputDomain returns the domain whose events this saga consumes.
func (d *SagaDispatch) InputDomain() string { return d.inputDomain }

// TargetDomains returns the domains this saga issues commands to.
func (d *SagaDispatch) TargetDomains() []string { return d.targets }

// EventTypes returns the registered fully-qualified event type names.
func (d *SagaDispatch) EventTypes() []string {
	types := make([]string, 0, len(d.handlers))
	for fullName := range d.handlers {
		types = append(types, fullName)
	}
	return types
}

// Subscriptions derives the subscription map from the table.
func (d *SagaDispatch) Subscriptions() map[string][]string {
	return map[string][]string{d.inputDomain: d.EventTypes()}
}

// Dispatch walks EVERY page of the source book (saga.proto: source =
// the events that triggered the saga — not full aggregate state, so
// each page is a fresh trigger; pinned by projector.feature C-0011's
// sibling semantics). Notifications route to the FQ-keyed rejection
// entry; an undeclared rejection is the framework's to handle
// (DelegateToFramework) and yields an empty response, by declaration
// rather than by accident.
func (d *SagaDispatch) Dispatch(source *pb.EventBook, destinationSequences map[string]uint32) (*pb.SagaResponse, error) {
	if source == nil {
		return nil, InvalidArgumentErrorWithCode(CodeMissingSagaSource, MessageMissingSagaSource, nil)
	}
	if len(source.Pages) == 0 {
		return nil, InvalidArgumentErrorWithCode(CodeEmptySagaSource, MessageEmptySagaSource, nil)
	}

	dests := NewDestinations(destinationSequences)
	resp := &pb.SagaResponse{}

	for _, page := range source.Pages {
		eventAny := page.GetEvent()
		if eventAny == nil {
			continue
		}

		// MED-4.5 / audit #25: exact type-URL match only.
		if IsNotificationTypeURL(eventAny.TypeUrl) {
			events, err := d.dispatchRejection(eventAny)
			if err != nil {
				return nil, err
			}
			resp.Events = append(resp.Events, events...)
			continue
		}

		thunk, ok := d.handlers[TypeNameFromURL(eventAny.TypeUrl)]
		if !ok {
			continue // saga only reacts to declared types (spec C-0051)
		}
		commands, events, err := thunk(eventAny, dests)
		if err != nil {
			return nil, err
		}
		resp.Commands = append(resp.Commands, commands...)
		resp.Events = append(resp.Events, events...)
	}

	// FILL-ONLY correlation propagation (Cover contract: the correlation
	// ID flows through all commands/events): emitted commands inherit the
	// source book's correlation unless the handler stamped one itself —
	// the saga sibling of the aggregate's stampEmittedBook.
	if corr := source.GetCover().GetCorrelationId(); corr != "" {
		for _, cmd := range resp.Commands {
			if cmd.Cover == nil {
				cmd.Cover = &pb.Cover{}
			}
			if cmd.Cover.CorrelationId == "" {
				cmd.Cover.CorrelationId = corr
			}
		}
	}
	return resp, nil
}

// dispatchRejection decodes a Notification page and routes it to the
// FQ-keyed compensation entry.
func (d *SagaDispatch) dispatchRejection(eventAny *anypb.Any) ([]*pb.EventBook, error) {
	notification := &pb.Notification{}
	if err := proto.Unmarshal(eventAny.Value, notification); err != nil {
		return nil, InvalidArgumentErrorWithCode(CodeNotificationDecodeFailed,
			MessageNotificationDecodeFailed, map[string]string{ExtraKeyTypeURL: eventAny.TypeUrl})
	}

	rejection := &pb.RejectionNotification{}
	if notification.Payload != nil {
		if err := proto.Unmarshal(notification.Payload.Value, rejection); err != nil {
			return nil, InvalidArgumentErrorWithCode(CodeRejectionNotificationDecodeFailed,
				MessageRejectionNotificationDecodeFailed, nil)
		}
	}

	_, fqCommand := extractRejectionKey(rejection)
	thunks, ok := d.rejections[fqCommand]
	if !ok {
		return nil, nil // undeclared: DelegateToFramework
	}
	var events []*pb.EventBook
	for _, thunk := range thunks {
		out, err := thunk(notification, rejection)
		if err != nil {
			return nil, err
		}
		events = append(events, out...)
	}
	return events, nil
}

// ----------------------------------------------------------------------------
// AggregateDispatch — command → events table
// ----------------------------------------------------------------------------

// CommandContext carries per-dispatch facts the business method may need
// beyond its typed command and rebuilt state.
type CommandContext struct {
	// NextSequence is the aggregate's next event sequence, derived from
	// the prior-events book the coordinator supplied.
	NextSequence uint32
	// HadPriorEvents is the "does this aggregate exist" signal — true
	// when the prior-events book carried any history (pages or
	// snapshot). Exposed here because state factories produce non-nil
	// zero states, so business code cannot infer existence from state.
	HadPriorEvents bool
}

// AggregateCommandThunk handles one command against rebuilt state.
// Generated thunks unmarshal to the typed command and call the typed
// business method.
type AggregateCommandThunk[S any] func(cmdAny *anypb.Any, state S, cctx CommandContext) (*pb.EventBook, error)

// AggregateRejectionThunk compensates a rejected command against
// rebuilt state, returning a full BusinessResponse (events, or an
// escalation Notification). CommandContext supplies NextSequence so
// compensation events append after prior history (spec C-0083).
type AggregateRejectionThunk[S any] func(n *pb.Notification, rejection *pb.RejectionNotification, state S, cctx CommandContext) (*pb.BusinessResponse, error)

// AggregateDispatch is the dispatch table for one aggregate component.
type AggregateDispatch[S any] struct {
	name       string
	domain     string
	rebuilder  *Rebuilder[S]
	handlers   map[string]AggregateCommandThunk[S]     // FQ command type name
	rejections map[string][]AggregateRejectionThunk[S] // FQ command type name (deep-clerk); ordered (C-0042)
}

// NewAggregateDispatch builds an empty aggregate table over a Rebuilder.
func NewAggregateDispatch[S any](name, domain string, rebuilder *Rebuilder[S]) *AggregateDispatch[S] {
	return &AggregateDispatch[S]{
		name:       name,
		domain:     domain,
		rebuilder:  rebuilder,
		handlers:   make(map[string]AggregateCommandThunk[S]),
		rejections: make(map[string][]AggregateRejectionThunk[S]),
	}
}

// OnCommand registers the thunk for a fully-qualified command type name.
func (d *AggregateDispatch[S]) OnCommand(fullName string, thunk AggregateCommandThunk[S]) *AggregateDispatch[S] {
	d.handlers[fullName] = thunk
	return d
}

// OnRejected registers a compensation thunk keyed by the FULLY-QUALIFIED
// rejected-command type name (decided: deep-clerk).
// Multiple registrations for the same command all run, in registration
// order (spec C-0042); their compensation events merge into one response.
func (d *AggregateDispatch[S]) OnRejected(fqCommandType string, thunk AggregateRejectionThunk[S]) *AggregateDispatch[S] {
	d.rejections[fqCommandType] = append(d.rejections[fqCommandType], thunk)
	return d
}

// Name returns the component name.
func (d *AggregateDispatch[S]) Name() string { return d.name }

// Domain returns the aggregate's domain.
func (d *AggregateDispatch[S]) Domain() string { return d.domain }

// CommandTypes returns the registered fully-qualified command type names.
func (d *AggregateDispatch[S]) CommandTypes() []string {
	types := make([]string, 0, len(d.handlers))
	for fullName := range d.handlers {
		types = append(types, fullName)
	}
	return types
}

// Dispatch routes a ContextualCommand: envelope and command type are
// validated BEFORE state is rebuilt (an unknown command must report
// NO_HANDLER_REGISTERED, not whatever the rebuild would surface), then
// state rebuilds — a corrupt persisted event fails the command
// (PERSISTED_EVENT_CORRUPT → DATA_LOSS; decided: cheap-hull) — and the
// thunk runs with the rebuilt state and CommandContext.
func (d *AggregateDispatch[S]) Dispatch(req *pb.ContextualCommand) (*pb.BusinessResponse, error) {
	if req == nil || req.Command == nil {
		return nil, InvalidArgumentErrorWithCode(CodeMissingCommandBook, ErrMsgNoCommandPages, nil)
	}
	if len(req.Command.Pages) == 0 {
		return nil, InvalidArgumentErrorWithCode(CodeMissingCommandPage, ErrMsgNoCommandPages, nil)
	}
	commandAny := req.Command.Pages[0].GetCommand()
	if commandAny == nil || commandAny.TypeUrl == "" {
		return nil, InvalidArgumentErrorWithCode(CodeMissingCommandPayload, ErrMsgNoCommandPages, nil)
	}

	// MED-4.5 / audit #25: exact type-URL match only.
	if IsNotificationTypeURL(commandAny.TypeUrl) {
		return d.dispatchRejection(commandAny, req.Events)
	}

	thunk, ok := d.handlers[TypeNameFromURL(commandAny.TypeUrl)]
	if !ok {
		return nil, InvalidArgumentErrorWithCode(CodeNoHandlerRegistered,
			ErrMsgUnknownCommand, map[string]string{ExtraKeyTypeURL: commandAny.TypeUrl})
	}

	state, info, err := d.rebuilder.Rebuild(req.Events)
	if err != nil {
		return nil, err
	}
	nextSeq := NextSequence(req.Events)
	events, err := thunk(commandAny, state, CommandContext{
		NextSequence:   nextSeq,
		HadPriorEvents: info.HadPriorEvents,
	})
	if err != nil {
		return nil, err
	}
	stampEmittedBook(events, req.Command.GetCover(), nextSeq)
	return &pb.BusinessResponse{Result: &pb.BusinessResponse_Events{Events: events}}, nil
}

// stampEmittedBook applies the dispatch path's FILL-ONLY stamps to an
// emitted EventBook (spec C-0001, C-0146..C-0148):
//   - the command cover's ext propagates onto the book's cover so child
//     aggregates carry their parent linkage — never overriding an ext
//     the handler set itself;
//   - pages without headers receive consecutive sequences from the
//     aggregate's next sequence — explicit headers are preserved.
func stampEmittedBook(events *pb.EventBook, cmdCover *pb.Cover, nextSeq uint32) {
	if events == nil {
		return
	}
	if ext := cmdCover.GetExt(); ext != nil {
		if events.Cover == nil {
			events.Cover = &pb.Cover{}
		}
		if events.Cover.Ext == nil {
			events.Cover.Ext = ext
		}
	}
	seq := nextSeq
	for _, page := range events.Pages {
		if page.Header == nil {
			page.Header = &pb.PageHeader{SequenceType: &pb.PageHeader_Sequence{Sequence: seq}}
		}
		seq++
	}
}

// dispatchRejection decodes a Notification command page and routes it to
// the FQ-keyed compensation entry with rebuilt state.
func (d *AggregateDispatch[S]) dispatchRejection(commandAny *anypb.Any, events *pb.EventBook) (*pb.BusinessResponse, error) {
	notification := &pb.Notification{}
	if err := proto.Unmarshal(commandAny.Value, notification); err != nil {
		return nil, InvalidArgumentErrorWithCode(CodeNotificationDecodeFailed,
			MessageNotificationDecodeFailed, map[string]string{ExtraKeyTypeURL: commandAny.TypeUrl})
	}
	rejection := &pb.RejectionNotification{}
	if notification.Payload != nil {
		if err := proto.Unmarshal(notification.Payload.Value, rejection); err != nil {
			return nil, InvalidArgumentErrorWithCode(CodeRejectionNotificationDecodeFailed,
				MessageRejectionNotificationDecodeFailed, nil)
		}
	}

	_, fqCommand := extractRejectionKey(rejection)
	thunks, ok := d.rejections[fqCommand]
	if !ok {
		// Undeclared: DelegateToFramework (by declaration, not accident).
		return &pb.BusinessResponse{}, nil
	}
	state, info, err := d.rebuilder.Rebuild(events)
	if err != nil {
		return nil, err
	}
	cctx := CommandContext{
		NextSequence:   NextSequence(events),
		HadPriorEvents: info.HadPriorEvents,
	}
	if len(thunks) == 1 {
		return thunks[0](notification, rejection, state, cctx)
	}
	// Fan-out: run every compensator in registration order, merging their
	// compensation events into one response.
	merged := &pb.EventBook{}
	for _, thunk := range thunks {
		resp, err := thunk(notification, rejection, state, cctx)
		if err != nil {
			return nil, err
		}
		if out := resp.GetEvents(); out != nil {
			merged.Pages = append(merged.Pages, out.Pages...)
		}
	}
	return &pb.BusinessResponse{Result: &pb.BusinessResponse_Events{Events: merged}}, nil
}

// ----------------------------------------------------------------------------
// ProcessManagerDispatch — trigger + own state → commands/process events
// ----------------------------------------------------------------------------

// PMEventThunk handles the newest trigger event against rebuilt PM
// state. Generated thunks unmarshal to the typed event, call the typed
// business method, and pack its results into the response.
type PMEventThunk[S any] func(eventAny *anypb.Any, state S, dests *Destinations) (*pb.ProcessManagerHandleResponse, error)

// PMRejectionThunk compensates a rejected PM-issued command, returning
// process events and an optional escalation Notification (carried on
// ProcessManagerHandleResponse.notification — field 4).
type PMRejectionThunk[S any] func(n *pb.Notification, rejection *pb.RejectionNotification, state S) ([]*pb.EventBook, *pb.Notification, error)

// ProcessManagerDispatch is the dispatch table for one process manager.
// PMs source events from multiple domains; handlers are keyed by
// (input domain, FQ event type).
type ProcessManagerDispatch[S any] struct {
	name       string
	pmDomain   string
	rebuilder  *Rebuilder[S]
	handlers   map[string]map[string]PMEventThunk[S]
	rejections map[string][]PMRejectionThunk[S] // FQ command type name (deep-clerk); ordered (C-0042)
}

// NewProcessManagerDispatch builds an empty PM table over a Rebuilder
// for the PM's own event-sourced state.
func NewProcessManagerDispatch[S any](name, pmDomain string, rebuilder *Rebuilder[S]) *ProcessManagerDispatch[S] {
	return &ProcessManagerDispatch[S]{
		name:       name,
		pmDomain:   pmDomain,
		rebuilder:  rebuilder,
		handlers:   make(map[string]map[string]PMEventThunk[S]),
		rejections: make(map[string][]PMRejectionThunk[S]),
	}
}

// OnEvent registers the thunk for (input domain, FQ event type).
func (d *ProcessManagerDispatch[S]) OnEvent(inputDomain, fullName string, thunk PMEventThunk[S]) *ProcessManagerDispatch[S] {
	if d.handlers[inputDomain] == nil {
		d.handlers[inputDomain] = make(map[string]PMEventThunk[S])
	}
	d.handlers[inputDomain][fullName] = thunk
	return d
}

// OnRejected registers a compensation thunk keyed by the FULLY-QUALIFIED
// rejected-command type name (decided: deep-clerk).
// Multiple registrations for the same command all run, in registration
// order (spec C-0042); process events merge; the first escalation wins.
func (d *ProcessManagerDispatch[S]) OnRejected(fqCommandType string, thunk PMRejectionThunk[S]) *ProcessManagerDispatch[S] {
	d.rejections[fqCommandType] = append(d.rejections[fqCommandType], thunk)
	return d
}

// Name returns the component name.
func (d *ProcessManagerDispatch[S]) Name() string { return d.name }

// PMDomain returns the PM's own domain.
func (d *ProcessManagerDispatch[S]) PMDomain() string { return d.pmDomain }

// Sources returns the input domains this PM listens to.
func (d *ProcessManagerDispatch[S]) Sources() []string {
	domains := make([]string, 0, len(d.handlers))
	for domain := range d.handlers {
		domains = append(domains, domain)
	}
	return domains
}

// Subscriptions derives the subscription map from the table.
func (d *ProcessManagerDispatch[S]) Subscriptions() map[string][]string {
	subs := make(map[string][]string, len(d.handlers))
	for domain, byType := range d.handlers {
		types := make([]string, 0, len(byType))
		for fullName := range byType {
			types = append(types, fullName)
		}
		subs[domain] = types
	}
	return subs
}

// Dispatch handles a trigger book against rebuilt PM state. The trigger
// is the FULL STATE of the triggering domain (process_manager.proto) —
// only the NEWEST page is the trigger; history must not re-fire. A
// trigger from a domain outside the PM's sources, or an undeclared
// event type, yields an empty response (spec C-0022 — not an error).
func (d *ProcessManagerDispatch[S]) Dispatch(
	trigger, processState *pb.EventBook,
	destinationSequences map[string]uint32,
) (*pb.ProcessManagerHandleResponse, error) {
	if trigger == nil {
		return nil, InvalidArgumentErrorWithCode(CodeMissingPMTrigger, "missing PM trigger", nil)
	}
	if len(trigger.Pages) == 0 {
		return nil, InvalidArgumentErrorWithCode(CodeEmptyPMTrigger, "empty PM trigger", nil)
	}
	eventAny := trigger.Pages[len(trigger.Pages)-1].GetEvent()
	if eventAny == nil {
		return nil, InvalidArgumentErrorWithCode(CodeMissingPMEventPayload, "missing PM event payload", nil)
	}

	// MED-4.5 / audit #25: exact type-URL match only.
	if IsNotificationTypeURL(eventAny.TypeUrl) {
		return d.dispatchRejection(eventAny, processState)
	}

	triggerDomain := ""
	if trigger.Cover != nil {
		triggerDomain = trigger.Cover.Domain
	}
	byType, ok := d.handlers[triggerDomain]
	if !ok {
		return &pb.ProcessManagerHandleResponse{}, nil // outside sources (C-0022)
	}
	thunk, ok := byType[TypeNameFromURL(eventAny.TypeUrl)]
	if !ok {
		return &pb.ProcessManagerHandleResponse{}, nil // undeclared type
	}

	state, _, err := d.rebuilder.Rebuild(processState)
	if err != nil {
		return nil, err
	}
	return thunk(eventAny, state, NewDestinations(destinationSequences))
}

// dispatchRejection routes a Notification trigger to the FQ-keyed
// compensation entry; the escalation Notification rides field 4 of the
// response.
func (d *ProcessManagerDispatch[S]) dispatchRejection(eventAny *anypb.Any, processState *pb.EventBook) (*pb.ProcessManagerHandleResponse, error) {
	notification := &pb.Notification{}
	if err := proto.Unmarshal(eventAny.Value, notification); err != nil {
		return nil, InvalidArgumentErrorWithCode(CodeNotificationDecodeFailed,
			MessageNotificationDecodeFailed, map[string]string{ExtraKeyTypeURL: eventAny.TypeUrl})
	}
	rejection := &pb.RejectionNotification{}
	if notification.Payload != nil {
		if err := proto.Unmarshal(notification.Payload.Value, rejection); err != nil {
			return nil, InvalidArgumentErrorWithCode(CodeRejectionNotificationDecodeFailed,
				MessageRejectionNotificationDecodeFailed, nil)
		}
	}

	_, fqCommand := extractRejectionKey(rejection)
	thunks, ok := d.rejections[fqCommand]
	if !ok {
		return &pb.ProcessManagerHandleResponse{}, nil // undeclared: DelegateToFramework
	}
	state, _, err := d.rebuilder.Rebuild(processState)
	if err != nil {
		return nil, err
	}
	out := &pb.ProcessManagerHandleResponse{}
	for _, thunk := range thunks {
		processEvents, escalation, err := thunk(notification, rejection, state)
		if err != nil {
			return nil, err
		}
		out.ProcessEvents = append(out.ProcessEvents, processEvents...)
		if out.Notification == nil {
			out.Notification = escalation
		}
	}
	return out, nil
}

// ----------------------------------------------------------------------------
// ProjectorDispatch — event book → projection
// ----------------------------------------------------------------------------

// ProjectorEventThunk folds one event into the per-delivery projection
// instance P.
type ProjectorEventThunk[P any] func(projection P, eventAny *anypb.Any) error

// ProjectorFinish packs the folded instance into the wire Projection.
type ProjectorFinish[P any] func(projection P, events *pb.EventBook) (*pb.Projection, error)

// projectorLogger emits the cross-language WARN when a projector
// receives an event with no matching handler (Rust parity:
// #[handles_unknown] / unconditional tracing::warn!).
var projectorLogger = slog.Default()

// SetProjectorLogger swaps the projector WARN logger, returning the
// previous one (test hook; mirrors Rust's tracing subscriber swap).
func SetProjectorLogger(logger *slog.Logger) *slog.Logger {
	prev := projectorLogger
	projectorLogger = logger
	return prev
}

// ProjectorDispatch is the dispatch table for one projector. One
// instance of P is created per delivery and reused across every page
// (spec C-0012); each declared event type folds into it (spec C-0011);
// Finish packs the result.
type ProjectorDispatch[P any] struct {
	name     string
	factory  func() P
	domains  map[string]bool                   // consumed domains; empty = all
	handlers map[string]ProjectorEventThunk[P] // FQ event type name
	unknown  func(typeURL string)
	finish   ProjectorFinish[P]
}

// NewProjectorDispatch builds an empty projector table.
func NewProjectorDispatch[P any](name string, factory func() P) *ProjectorDispatch[P] {
	return &ProjectorDispatch[P]{
		name:     name,
		factory:  factory,
		handlers: make(map[string]ProjectorEventThunk[P]),
	}
}

// ForDomains restricts the projector to books from the given domains —
// events from undeclared domains never fire handlers (spec C-0032).
// Without it, every domain is consumed.
func (d *ProjectorDispatch[P]) ForDomains(domains ...string) *ProjectorDispatch[P] {
	if d.domains == nil {
		d.domains = make(map[string]bool, len(domains))
	}
	for _, domain := range domains {
		d.domains[domain] = true
	}
	return d
}

// OnEvent registers the thunk for a fully-qualified event type name.
func (d *ProjectorDispatch[P]) OnEvent(fullName string, thunk ProjectorEventThunk[P]) *ProjectorDispatch[P] {
	d.handlers[fullName] = thunk
	return d
}

// OnUnknown registers a catch-all invoked for event types with no
// registered handler (Rust parity: #[handles_unknown]). With a
// catch-all, unknown types are intentionally handled and no WARN is
// emitted; without one, each unknown type logs at WARN.
func (d *ProjectorDispatch[P]) OnUnknown(fn func(typeURL string)) *ProjectorDispatch[P] {
	d.unknown = fn
	return d
}

// Finish registers the packer that turns the folded instance into the
// wire Projection.
func (d *ProjectorDispatch[P]) Finish(fn ProjectorFinish[P]) *ProjectorDispatch[P] {
	d.finish = fn
	return d
}

// Name returns the component name.
func (d *ProjectorDispatch[P]) Name() string { return d.name }

// EventTypes returns the registered fully-qualified event type names.
func (d *ProjectorDispatch[P]) EventTypes() []string {
	types := make([]string, 0, len(d.handlers))
	for fullName := range d.handlers {
		types = append(types, fullName)
	}
	return types
}

// Dispatch folds every page of the book into one projection instance.
func (d *ProjectorDispatch[P]) Dispatch(events *pb.EventBook) (*pb.Projection, error) {
	if events == nil || events.Cover == nil {
		return nil, InvalidArgumentErrorWithCode(CodeMissingEventBookCover, "missing event book cover", nil)
	}
	projection := d.factory()
	consumed := d.domains == nil || d.domains[events.Cover.GetDomain()]
	if !consumed {
		// Undeclared domain: nothing folds (spec C-0032).
		if d.finish == nil {
			return &pb.Projection{Cover: events.Cover, Projector: d.name}, nil
		}
		return d.finish(projection, events)
	}
	for _, page := range events.Pages {
		eventAny := page.GetEvent()
		if eventAny == nil {
			continue
		}
		thunk, ok := d.handlers[TypeNameFromURL(eventAny.TypeUrl)]
		if !ok {
			if d.unknown != nil {
				d.unknown(eventAny.TypeUrl)
			} else {
				projectorLogger.Warn(
					"projector received event with no matching handler",
					slog.String("projector", d.name),
					slog.String("type_url", eventAny.TypeUrl),
				)
			}
			continue
		}
		if err := thunk(projection, eventAny); err != nil {
			return nil, err
		}
	}
	if d.finish == nil {
		return &pb.Projection{Cover: events.Cover, Projector: d.name}, nil
	}
	return d.finish(projection, events)
}
