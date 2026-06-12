package angzarr

// Thin gRPC adapters for the engine dispatch tables. These are the ONLY
// gRPC-aware layer over the engine: they bind a table to the generic
// framework service (the envelope stays google.protobuf.Any; the
// coordinator stays domain-blind) and map handler errors through the
// single mapHandlerError. No dispatch logic lives here.

import (
	"context"

	pb "github.com/benjaminabbitt/angzarr/client/go/proto/angzarr_client/proto/angzarr/v1"
	"google.golang.org/grpc"
)

// SagaDispatchHandler adapts a SagaDispatch table to the Saga service.
type SagaDispatchHandler struct {
	pb.UnimplementedSagaServiceServer
	table *SagaDispatch
}

// NewSagaDispatchHandler wraps a saga table for gRPC serving.
func NewSagaDispatchHandler(table *SagaDispatch) *SagaDispatchHandler {
	return &SagaDispatchHandler{table: table}
}

// Handle translates source events into commands via the table.
func (h *SagaDispatchHandler) Handle(_ context.Context, req *pb.SagaHandleRequest) (*pb.SagaResponse, error) {
	resp, err := h.table.Dispatch(req.GetSource(), req.GetDestinationSequences())
	if err != nil {
		return nil, mapHandlerError(err)
	}
	return resp, nil
}

// RegisterSagaDispatch returns a ServiceRegistrar for a saga table.
func RegisterSagaDispatch(table *SagaDispatch) ServiceRegistrar {
	return func(server *grpc.Server) {
		pb.RegisterSagaServiceServer(server, NewSagaDispatchHandler(table))
	}
}

// RunSagaDispatchServer serves a saga table until SIGINT/SIGTERM.
func RunSagaDispatchServer(defaultPort string, table *SagaDispatch) {
	RunServer(RegisterSagaDispatch(table), ServerOptions{
		ServiceName:      "Saga",
		Domain:           table.Name(),
		DefaultPort:      defaultPort,
		EnableReflection: true,
	})
}

// AggregateDispatchHandler adapts an AggregateDispatch table to the
// CommandHandler service.
type AggregateDispatchHandler[S any] struct {
	pb.UnimplementedCommandHandlerServiceServer
	table *AggregateDispatch[S]
}

// NewAggregateDispatchHandler wraps an aggregate table for gRPC serving.
func NewAggregateDispatchHandler[S any](table *AggregateDispatch[S]) *AggregateDispatchHandler[S] {
	return &AggregateDispatchHandler[S]{table: table}
}

// Handle processes a contextual command asynchronously.
func (h *AggregateDispatchHandler[S]) Handle(_ context.Context, req *pb.ContextualCommand) (*pb.BusinessResponse, error) {
	resp, err := h.table.Dispatch(req)
	if err != nil {
		return nil, mapHandlerError(err)
	}
	return resp, nil
}

// HandleSync processes a contextual command synchronously.
func (h *AggregateDispatchHandler[S]) HandleSync(ctx context.Context, req *pb.ContextualCommand) (*pb.BusinessResponse, error) {
	return h.Handle(ctx, req)
}

// RegisterAggregateDispatch returns a ServiceRegistrar for an aggregate table.
func RegisterAggregateDispatch[S any](table *AggregateDispatch[S]) ServiceRegistrar {
	return func(server *grpc.Server) {
		pb.RegisterCommandHandlerServiceServer(server, NewAggregateDispatchHandler(table))
	}
}

// RunAggregateDispatchServer serves an aggregate table until SIGINT/SIGTERM.
func RunAggregateDispatchServer[S any](defaultPort string, table *AggregateDispatch[S]) {
	RunServer(RegisterAggregateDispatch(table), ServerOptions{
		ServiceName:      "CommandHandler",
		Domain:           table.Domain(),
		DefaultPort:      defaultPort,
		EnableReflection: true,
	})
}

// ProcessManagerDispatchHandler adapts a ProcessManagerDispatch table to
// the ProcessManager service.
type ProcessManagerDispatchHandler[S any] struct {
	pb.UnimplementedProcessManagerServiceServer
	table *ProcessManagerDispatch[S]
}

// NewProcessManagerDispatchHandler wraps a PM table for gRPC serving.
func NewProcessManagerDispatchHandler[S any](table *ProcessManagerDispatch[S]) *ProcessManagerDispatchHandler[S] {
	return &ProcessManagerDispatchHandler[S]{table: table}
}

// Handle processes a trigger against rebuilt PM state.
func (h *ProcessManagerDispatchHandler[S]) Handle(_ context.Context, req *pb.ProcessManagerHandleRequest) (*pb.ProcessManagerHandleResponse, error) {
	resp, err := h.table.Dispatch(req.GetTrigger(), req.GetProcessState(), req.GetDestinationSequences())
	if err != nil {
		return nil, mapHandlerError(err)
	}
	return resp, nil
}

// RegisterProcessManagerDispatch returns a ServiceRegistrar for a PM table.
func RegisterProcessManagerDispatch[S any](table *ProcessManagerDispatch[S]) ServiceRegistrar {
	return func(server *grpc.Server) {
		pb.RegisterProcessManagerServiceServer(server, NewProcessManagerDispatchHandler(table))
	}
}

// RunProcessManagerDispatchServer serves a PM table until SIGINT/SIGTERM.
func RunProcessManagerDispatchServer[S any](defaultPort string, table *ProcessManagerDispatch[S]) {
	RunServer(RegisterProcessManagerDispatch(table), ServerOptions{
		ServiceName:      "ProcessManager",
		Domain:           table.Name(),
		DefaultPort:      defaultPort,
		EnableReflection: true,
	})
}

// ProjectorDispatchHandler adapts a ProjectorDispatch table to the
// Projector service.
type ProjectorDispatchHandler[P any] struct {
	pb.UnimplementedProjectorServiceServer
	table *ProjectorDispatch[P]
}

// NewProjectorDispatchHandler wraps a projector table for gRPC serving.
func NewProjectorDispatchHandler[P any](table *ProjectorDispatch[P]) *ProjectorDispatchHandler[P] {
	return &ProjectorDispatchHandler[P]{table: table}
}

// Handle folds the delivered book into a projection.
func (h *ProjectorDispatchHandler[P]) Handle(_ context.Context, events *pb.EventBook) (*pb.Projection, error) {
	proj, err := h.table.Dispatch(events)
	if err != nil {
		return nil, mapHandlerError(err)
	}
	return proj, nil
}

// HandleSpeculative folds without side effects — the table itself is
// side-effect free; speculation discipline is the business code's.
func (h *ProjectorDispatchHandler[P]) HandleSpeculative(ctx context.Context, events *pb.EventBook) (*pb.Projection, error) {
	return h.Handle(ctx, events)
}

// RegisterProjectorDispatch returns a ServiceRegistrar for a projector table.
func RegisterProjectorDispatch[P any](table *ProjectorDispatch[P]) ServiceRegistrar {
	return func(server *grpc.Server) {
		pb.RegisterProjectorServiceServer(server, NewProjectorDispatchHandler(table))
	}
}

// RunProjectorDispatchServer serves a projector table until SIGINT/SIGTERM.
func RunProjectorDispatchServer[P any](defaultPort string, table *ProjectorDispatch[P]) {
	RunServer(RegisterProjectorDispatch(table), ServerOptions{
		ServiceName:      "Projector",
		Domain:           table.Name(),
		DefaultPort:      defaultPort,
		EnableReflection: true,
	})
}
