// Package angzarr provides CloudEvents support for projectors.
//
// CloudEvents projectors transform internal domain events into CloudEvents 1.0
// format for external consumption via HTTP webhooks or Kafka.
//
// Functional Pattern (CloudEventsRouter):
//
//	func handleRegistered(event *examples.PlayerRegistered) *pb.CloudEvent {
//	    public := &examples.PublicPlayerRegistered{DisplayName: event.DisplayName}
//	    data, _ := anypb.New(public)
//	    return &pb.CloudEvent{Type: "com.poker.player.registered", Data: data}
//	}
//
//	router := NewCloudEventsRouter("prj-player-cloudevents", "player").
//	    On(handleRegistered)
//
// OO Pattern (CloudEventsProjectorBase):
//
//	type PlayerCloudEventsProjector struct {
//	    angzarr.CloudEventsProjectorBase
//	}
//
//	func NewPlayerCloudEventsProjector() *PlayerCloudEventsProjector {
//	    p := &PlayerCloudEventsProjector{}
//	    p.Init("prj-player-cloudevents", "player")
//	    p.On(p.handleRegistered)
//	    return p
//	}
//
//	func (p *PlayerCloudEventsProjector) handleRegistered(event *examples.PlayerRegistered) *pb.CloudEvent {
//	    // Transform and return CloudEvent
//	}
package angzarr

import (
	"context"
	"reflect"

	pb "github.com/benjaminabbitt/angzarr/client/go/proto/angzarr_client/proto/angzarr/v1"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
)

// CloudEventsHandler is a function that transforms an event into a CloudEvent.
// The function should return nil to skip publishing for this event.
type CloudEventsHandler[E proto.Message] func(event E) *pb.CloudEvent

// cloudEventsFunc is the internal handler type that works with raw bytes.
type cloudEventsFunc func(data []byte) *pb.CloudEvent

// CloudEventsRouter provides fluent registration for CloudEvents projector handlers.
//
// Example:
//
//	func handleRegistered(event *examples.PlayerRegistered) *pb.CloudEvent {
//	    public := &examples.PublicPlayerRegistered{DisplayName: event.DisplayName}
//	    data, _ := anypb.New(public)
//	    return &pb.CloudEvent{Type: "com.poker.player.registered", Data: data}
//	}
//
//	router := NewCloudEventsRouter("prj-player-cloudevents", "player").
//	    On(handleRegistered)
type CloudEventsRouter struct {
	name        string
	inputDomain string
	handlers    map[string]cloudEventsFunc
}

// NewCloudEventsRouter creates a new CloudEvents router for the given projector name and domain.
func NewCloudEventsRouter(name, inputDomain string) *CloudEventsRouter {
	return &CloudEventsRouter{
		name:        name,
		inputDomain: inputDomain,
		handlers:    make(map[string]cloudEventsFunc),
	}
}

// Name returns the projector name.
func (r *CloudEventsRouter) Name() string {
	return r.name
}

// InputDomain returns the domain this projector subscribes to.
func (r *CloudEventsRouter) InputDomain() string {
	return r.inputDomain
}

// On registers a CloudEvents handler for an event type.
//
// The handler function must have signature: func(*EventType) *pb.CloudEvent
// where EventType is a protobuf message type. Return nil to skip this event.
//
// Example:
//
//	router.On(handlePlayerRegistered)
//
//	func handlePlayerRegistered(event *examples.PlayerRegistered) *pb.CloudEvent {
//	    public := &examples.PublicPlayerRegistered{DisplayName: event.DisplayName}
//	    data, _ := anypb.New(public)
//	    return &pb.CloudEvent{Type: "com.poker.player.registered", Data: data}
//	}
func (r *CloudEventsRouter) On(handler any) *CloudEventsRouter {
	handlerValue := reflect.ValueOf(handler)
	handlerType := handlerValue.Type()

	if handlerType.Kind() != reflect.Func {
		panic("CloudEventsRouter.On: handler must be a function")
	}
	if handlerType.NumIn() != 1 {
		panic("CloudEventsRouter.On: handler must have exactly 1 parameter (event *EventType)")
	}
	if handlerType.NumOut() != 1 {
		panic("CloudEventsRouter.On: handler must return *pb.CloudEvent")
	}

	// Get the event type (first parameter)
	eventPtrType := handlerType.In(0)
	if eventPtrType.Kind() != reflect.Ptr {
		panic("CloudEventsRouter.On: event parameter must be a pointer")
	}
	eventType := eventPtrType.Elem()

	// Extract fully-qualified type name via proto reflection
	eventPtr := reflect.New(eventType)
	protoMsg := eventPtr.Interface().(proto.Message)
	fullName := string(protoMsg.ProtoReflect().Descriptor().FullName())

	// Create the wrapper function
	wrapper := func(data []byte) *pb.CloudEvent {
		// Create a new instance of the event type
		eventPtr := reflect.New(eventType)
		event := eventPtr.Interface().(proto.Message)

		// Unmarshal the event
		if err := proto.Unmarshal(data, event); err != nil {
			return nil
		}

		// Call the handler
		results := handlerValue.Call([]reflect.Value{eventPtr})

		// Extract result
		if results[0].IsNil() {
			return nil
		}
		return results[0].Interface().(*pb.CloudEvent)
	}

	r.handlers[fullName] = wrapper
	return r
}

// Project processes an EventBook and returns CloudEvents.
func (r *CloudEventsRouter) Project(source *pb.EventBook) *pb.CloudEventsResponse {
	if source == nil {
		return &pb.CloudEventsResponse{}
	}

	var events []*pb.CloudEvent

	for _, page := range source.Pages {
		event := page.GetEvent()
		if event == nil {
			continue
		}

		typeURL := event.TypeUrl

		// MED-4.5 / audit #25: exact type-URL match only. Suffix
		// matching was removed — event-type lists derive exactly from
		// proto declarations, so a "family of types" need is an explicit
		// list of exact registrations, not a runtime suffix probe.
		for fullName, handler := range r.handlers {
			if typeURL == TypeURLPrefix+fullName {
				if ce := handler(event.Value); ce != nil {
					events = append(events, ce)
				}
				break
			}
		}
	}

	return &pb.CloudEventsResponse{Events: events}
}

// EventTypes returns the event type names this router handles.
func (r *CloudEventsRouter) EventTypes() []string {
	types := make([]string, 0, len(r.handlers))
	for fullName := range r.handlers {
		types = append(types, fullName)
	}
	return types
}

// Subscriptions returns the subscription configuration for framework registration.
func (r *CloudEventsRouter) Subscriptions() map[string][]string {
	return map[string][]string{
		r.inputDomain: r.EventTypes(),
	}
}

// CloudEventsProjectorBase provides OO-style CloudEvents projector infrastructure.
//
// Embed this in your projector struct and call Init() to set up the base.
// Then register handlers with On().
//
// Example:
//
//	type PlayerCloudEventsProjector struct {
//	    angzarr.CloudEventsProjectorBase
//	}
//
//	func NewPlayerCloudEventsProjector() *PlayerCloudEventsProjector {
//	    p := &PlayerCloudEventsProjector{}
//	    p.Init("prj-player-cloudevents", "player")
//	    p.On(p.handleRegistered)
//	    return p
//	}
//
//	func (p *PlayerCloudEventsProjector) handleRegistered(event *examples.PlayerRegistered) *pb.CloudEvent {
//	    public := &examples.PublicPlayerRegistered{DisplayName: event.DisplayName}
//	    data, _ := anypb.New(public)
//	    return &pb.CloudEvent{Type: "com.poker.player.registered", Data: data}
//	}
type CloudEventsProjectorBase struct {
	router *CloudEventsRouter
}

// Init initializes the CloudEvents projector base.
func (p *CloudEventsProjectorBase) Init(name, inputDomain string) {
	p.router = NewCloudEventsRouter(name, inputDomain)
}

// Name returns the projector name.
func (p *CloudEventsProjectorBase) Name() string {
	return p.router.Name()
}

// InputDomain returns the domain this projector subscribes to.
func (p *CloudEventsProjectorBase) InputDomain() string {
	return p.router.InputDomain()
}

// On registers a CloudEvents handler. See CloudEventsRouter.On for details.
func (p *CloudEventsProjectorBase) On(handler any) {
	p.router.On(handler)
}

// Project processes an EventBook and returns CloudEvents.
func (p *CloudEventsProjectorBase) Project(source *pb.EventBook) *pb.CloudEventsResponse {
	return p.router.Project(source)
}

// Handle implements the projector service interface.
// Returns a Projection containing the CloudEventsResponse packed as Any.
func (p *CloudEventsProjectorBase) Handle(source *pb.EventBook) (*pb.Projection, error) {
	response := p.router.Project(source)

	// Pack CloudEventsResponse into Any
	projectionAny, err := anypb.New(response)
	if err != nil {
		return nil, err
	}

	return &pb.Projection{
		Projector:  p.router.Name(),
		Projection: projectionAny,
	}, nil
}

// cloudEventsGrpcHandler adapts a project function to the Projector service.
type cloudEventsGrpcHandler struct {
	pb.UnimplementedProjectorServiceServer
	project func(*pb.EventBook) (*pb.Projection, error)
}

func (h *cloudEventsGrpcHandler) Handle(_ context.Context, events *pb.EventBook) (*pb.Projection, error) {
	return h.project(events)
}

func (h *cloudEventsGrpcHandler) HandleSpeculative(ctx context.Context, events *pb.EventBook) (*pb.Projection, error) {
	return h.Handle(ctx, events)
}

// RunCloudEventsProjectorServer runs a gRPC projector server using the CloudEvents router.
func RunCloudEventsProjectorServer(name, port string, router *CloudEventsRouter) {
	handler := &cloudEventsGrpcHandler{project: func(source *pb.EventBook) (*pb.Projection, error) {
		response := router.Project(source)
		projectionAny, err := anypb.New(response)
		if err != nil {
			return nil, err
		}
		return &pb.Projection{
			Projector:  router.Name(),
			Projection: projectionAny,
		}, nil
	}}
	RunServer(func(server *grpc.Server) {
		pb.RegisterProjectorServiceServer(server, handler)
	}, ServerOptions{ServiceName: "Projector", Domain: name, DefaultPort: port, EnableReflection: true})
}

// RunOOCloudEventsProjectorServer runs a gRPC projector server using the OO CloudEvents projector.
func RunOOCloudEventsProjectorServer(name, port string, projector *CloudEventsProjectorBase) {
	handler := &cloudEventsGrpcHandler{project: projector.Handle}
	RunServer(func(server *grpc.Server) {
		pb.RegisterProjectorServiceServer(server, handler)
	}, ServerOptions{ServiceName: "Projector", Domain: name, DefaultPort: port, EnableReflection: true})
}
