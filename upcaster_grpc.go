package angzarr

// Upcaster gRPC adapter — serves UpcasterHandleFunc pipelines. The
// upcaster component kind is outside the dispatch-table consolidation.

import (
	"context"

	pb "github.com/benjaminabbitt/angzarr/client/go/proto/angzarr_client/proto/angzarr/v1"
	"google.golang.org/grpc"
)

// UpcasterHandleFunc transforms a slice of EventPages to their current versions.
type UpcasterHandleFunc func(events []*pb.EventPage) []*pb.EventPage

// UpcasterGrpcHandler wraps a handle function for the gRPC Upcaster service.
//
// Example:
//
//	router := NewUpcasterRouter("player").
//	    On("examples.PlayerRegisteredV1", upcastPlayerRegistered)
//
//	handler := NewUpcasterGrpcHandler("upcaster-player", "player").
//	    WithHandle(func(events []*pb.EventPage) []*pb.EventPage {
//	        return router.Upcast(events)
//	    })
//
//	RunUpcasterServer("upcaster-player", "50401", handler)
type UpcasterGrpcHandler struct {
	pb.UnimplementedUpcasterServiceServer
	name     string
	domain   string
	handleFn UpcasterHandleFunc
}

// NewUpcasterGrpcHandler creates a new upcaster handler.
func NewUpcasterGrpcHandler(name, domain string) *UpcasterGrpcHandler {
	return &UpcasterGrpcHandler{
		name:   name,
		domain: domain,
	}
}

// WithHandle sets the event transformation callback.
func (h *UpcasterGrpcHandler) WithHandle(fn UpcasterHandleFunc) *UpcasterGrpcHandler {
	h.handleFn = fn
	return h
}

// Upcast transforms events to current versions.
func (h *UpcasterGrpcHandler) Upcast(ctx context.Context, req *pb.UpcastRequest) (*pb.UpcastResponse, error) {
	events := req.Events
	if h.handleFn != nil {
		events = h.handleFn(events)
	}
	return &pb.UpcastResponse{Events: events}, nil
}

// RegisterUpcasterGrpcHandler returns a ServiceRegistrar that registers an upcaster handler.
func RegisterUpcasterGrpcHandler(handler *UpcasterGrpcHandler) ServiceRegistrar {
	return func(server *grpc.Server) {
		pb.RegisterUpcasterServiceServer(server, handler)
	}
}

// RunUpcasterServer starts a gRPC server for an upcaster.
//
// Parameters:
//   - name: The upcaster's name (e.g., "upcaster-player")
//   - defaultPort: Default TCP port if PORT env not set
//   - handler: UpcasterGrpcHandler with configured handle function
func RunUpcasterServer(name, defaultPort string, handler *UpcasterGrpcHandler) {
	RunServer(RegisterUpcasterGrpcHandler(handler), ServerOptions{
		ServiceName:      "Upcaster",
		Domain:           name,
		DefaultPort:      defaultPort,
		EnableReflection: true,
	})
}
