package query

import (
	"context"
	"sync"
	"time"

	"gocordis-csv-collector/app/model"
)

// ObservationEvent is a UI-friendly "state changed" notification. It carries
// no business state: the UI reacts by invalidating and re-running a Query.
type ObservationEvent struct {
	Type string
	Key  model.CollectionKey
	At   time.Time
}

// Observation answers "when did state change?". It is the UI-friendly stream
// that an Application Observation Adapter publishes after Application events.
type Observation interface {
	// Subscribe registers a handler notified whenever state changes. The
	// returned function unsubscribes; it is idempotent.
	Subscribe(ctx context.Context, handler func(ObservationEvent)) (func() error, error)
	// Latest returns the most recent up to n observation events (Event Feed).
	Latest(n int) []ObservationEvent
}

// ObservationService is the Application-owned in-memory observation bus. The
// UI does not own it; the Application Observation Adapter publishes into it
// and query providers subscribe.
type ObservationService struct {
	mu   sync.Mutex
	subs map[int]func(ObservationEvent)
	next int
	ring []ObservationEvent
	max  int
}

const defaultFeedSize = 64

// NewObservationService returns an empty observation service.
func NewObservationService() *ObservationService {
	return &ObservationService{subs: map[int]func(ObservationEvent){}, max: defaultFeedSize}
}

// Subscribe registers a handler.
func (s *ObservationService) Subscribe(_ context.Context, handler func(ObservationEvent)) (func() error, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := s.next
	s.next++
	s.subs[id] = handler
	return func() error {
		s.mu.Lock()
		defer s.mu.Unlock()
		delete(s.subs, id)
		return nil
	}, nil
}

// Publish notifies subscribers and appends to the latest-events feed. Handler
// invocation happens outside the lock so a subscriber may safely re-enter the
// service (e.g. read Latest or run queries).
func (s *ObservationService) Publish(ev ObservationEvent) {
	if ev.At.IsZero() {
		ev.At = time.Now()
	}
	s.mu.Lock()
	s.ring = append(s.ring, ev)
	if len(s.ring) > s.max {
		s.ring = s.ring[len(s.ring)-s.max:]
	}
	handlers := make([]func(ObservationEvent), 0, len(s.subs))
	for _, h := range s.subs {
		handlers = append(handlers, h)
	}
	s.mu.Unlock()
	for _, h := range handlers {
		h(ev)
	}
}

// Latest returns the newest up to n events (oldest first).
func (s *ObservationService) Latest(n int) []ObservationEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	if n <= 0 || n > len(s.ring) {
		n = len(s.ring)
	}
	out := make([]ObservationEvent, n)
	copy(out, s.ring[len(s.ring)-n:])
	return out
}
