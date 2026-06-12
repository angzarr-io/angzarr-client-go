// Package angzarr provides event version transformation via UpcasterRouter.
package angzarr

import (
	pb "github.com/benjaminabbitt/angzarr/client/go/proto/angzarr_client/proto/angzarr/v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
)

// UpcasterHandler transforms an old event Any to a new event Any.
type UpcasterHandler func(old *anypb.Any) *anypb.Any

// UpcasterRouter transforms old event versions to current versions.
//
// Events matching registered handlers are transformed.
// Events without matching handlers pass through unchanged.
//
// Example:
//
//	router := NewUpcasterRouter("order").
//	    On("OrderCreatedV1", upcastCreatedV1).
//	    On("OrderShippedV1", upcastShippedV1)
//
//	newEvents := router.Upcast(oldEvents)
type UpcasterRouter struct {
	domain   string
	handlers []upcasterEntry
}

type upcasterEntry struct {
	fullName string
	handler  UpcasterHandler
}

// NewUpcasterRouter creates a new upcaster router for a domain.
func NewUpcasterRouter(domain string) *UpcasterRouter {
	return &UpcasterRouter{
		domain:   domain,
		handlers: make([]upcasterEntry, 0),
	}
}

// On registers a handler for an old event type.
//
// The fullName is the fully-qualified proto type name (e.g., "examples.OrderCreatedV1").
// This matches against type_url = "type.googleapis.com/" + fullName.
func (r *UpcasterRouter) On(fullName string, handler UpcasterHandler) *UpcasterRouter {
	r.handlers = append(r.handlers, upcasterEntry{fullName: fullName, handler: handler})
	return r
}

// Upcast transforms a list of events to current versions.
//
// Dispatch is chained (C-0136/C-0137): every matching upcaster applies in
// registration order, the output of one feeding the next, so schema
// evolution composes across versions (V1 → V2 → V3). The chain stops when
// no later registration matches the current event type. Events with no
// matching handlers pass through unchanged.
func (r *UpcasterRouter) Upcast(events []*pb.EventPage) []*pb.EventPage {
	result := make([]*pb.EventPage, 0, len(events))

	for _, page := range events {
		event := page.GetEvent()
		if event == nil {
			result = append(result, page)
			continue
		}

		transformed := false
		for _, entry := range r.handlers {
			if event.TypeUrl == TypeURLPrefix+entry.fullName {
				event = entry.handler(event)
				transformed = true
			}
		}

		if !transformed {
			result = append(result, page)
			continue
		}
		newPage := proto.Clone(page).(*pb.EventPage)
		newPage.Payload = &pb.EventPage_Event{Event: event}
		result = append(result, newPage)
	}

	return result
}

// Domain returns the domain this upcaster handles.
func (r *UpcasterRouter) Domain() string {
	return r.domain
}
