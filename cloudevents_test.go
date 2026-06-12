package angzarr

import (
	"testing"

	pb "github.com/benjaminabbitt/angzarr/client/go/proto/angzarr_client/proto/angzarr/v1"
	"google.golang.org/protobuf/types/known/anypb"
)

func cloudEventBookWith(t *testing.T, msgs ...*pb.Cover) *pb.EventBook {
	t.Helper()
	pages := make([]*pb.EventPage, 0, len(msgs))
	for _, m := range msgs {
		packed, err := anypb.New(m)
		if err != nil {
			t.Fatalf("anypb.New: %v", err)
		}
		pages = append(pages, &pb.EventPage{Payload: &pb.EventPage_Event{Event: packed}})
	}
	return &pb.EventBook{Pages: pages}
}

// Audit #25 / MED-4.5: dispatch is exact type-URL match only. The
// OnSuffix registration path was removed: a type registered both
// exactly and by suffix published TWO CloudEvents per event (exact hit
// broke the inner loop, then fell through into the suffix block on the
// shared handlers map), and suffix matching is string-based flow
// control. This pins single-publish for an exact registration.
func TestCloudEventsRouter_ExactMatch_PublishesExactlyOnce(t *testing.T) {
	router := NewCloudEventsRouter("test", "hand").
		On(func(c *pb.Cover) *pb.CloudEvent {
			return &pb.CloudEvent{Type: "io.angzarr.cover"}
		})

	resp := router.Project(cloudEventBookWith(t, &pb.Cover{Domain: "hand"}))
	if got := len(resp.Events); got != 1 {
		t.Fatalf("published %d CloudEvents for one event, want exactly 1", got)
	}
}

func TestCloudEventsRouter_UnregisteredType_PublishesNothing(t *testing.T) {
	router := NewCloudEventsRouter("test", "hand").
		On(func(n *pb.Notification) *pb.CloudEvent {
			return &pb.CloudEvent{Type: "io.angzarr.notification"}
		})

	resp := router.Project(cloudEventBookWith(t, &pb.Cover{Domain: "hand"}))
	if got := len(resp.Events); got != 0 {
		t.Fatalf("published %d CloudEvents for an unregistered type, want 0", got)
	}
}
