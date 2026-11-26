// Package events provides the in-process event hub shared by the stdio and HTTP
// transports. Events carry a monotonic sequence number so reconnecting clients
// can resume from the last event they saw.
package events

import (
	"strings"
	"sync"
	"time"
)

const (
	defaultBuffer  = 256
	defaultHistory = 1024
)

// Event is one server-initiated event.
type Event struct {
	Seq   uint64 `json:"seq"`
	Topic string `json:"topic"`
	TS    int64  `json:"ts"`
	Data  any    `json:"data,omitempty"`
}

// Options configures a Hub. Zero values use the defaults.
type Options struct {
	Buffer  int
	History int
}

// Stats is a point-in-time view of the hub.
type Stats struct {
	Seq         uint64 `json:"seq"`
	Subscribers int    `json:"subscribers"`
	Dropped     uint64 `json:"dropped"`
}

// Hub fans events out to subscribers and retains a bounded replay window.
type Hub struct {
	mu      sync.Mutex
	seq     uint64
	nextID  uint64
	buffer  int
	dropped uint64
	subs    map[uint64]*subscriber

	history  []Event
	histSize int
	histNext int
	histLen  int
}

type subscriber struct {
	id     uint64
	ch     chan Event
	topics []string
}

// New creates a Hub.
func New(opts Options) *Hub {
	if opts.Buffer <= 0 {
		opts.Buffer = defaultBuffer
	}
	if opts.History <= 0 {
		opts.History = defaultHistory
	}
	return &Hub{
		buffer:   opts.Buffer,
		histSize: opts.History,
		subs:     make(map[uint64]*subscriber),
	}
}

// Publish records an event and fans it out to matching subscribers. A slow
// subscriber loses its oldest queued event rather than blocking other
// subscribers or the producer.
func (h *Hub) Publish(topic string, data any) {
	topic = strings.TrimPrefix(strings.TrimSpace(topic), "event.")
	if topic == "" {
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	h.seq++
	ev := Event{
		Seq:   h.seq,
		Topic: topic,
		TS:    time.Now().UnixMilli(),
		Data:  data,
	}
	h.appendHistoryLocked(ev)

	for _, sub := range h.subs {
		if !matches(sub.topics, topic) {
			continue
		}
		select {
		case sub.ch <- ev:
		default:
			// Keep the stream moving: discard the oldest queued event.
			select {
			case <-sub.ch:
			default:
			}
			select {
			case sub.ch <- ev:
			default:
				h.dropped++
			}
			h.dropped++
		}
	}
}

// Subscribe returns a stream of matching events. If since is non-zero, events
// retained in the replay window with Seq > since are delivered first. The
// cancel function is safe to call more than once.
func (h *Hub) Subscribe(topics []string, since uint64) (<-chan Event, func()) {
	topics = normalizeTopics(topics)

	h.mu.Lock()
	replay := h.replayLocked(topics, since)
	ch := make(chan Event, h.buffer+len(replay))
	for _, ev := range replay {
		ch <- ev
	}

	id := h.nextID
	h.nextID++
	h.subs[id] = &subscriber{id: id, ch: ch, topics: topics}
	h.mu.Unlock()

	var once sync.Once
	cancel := func() {
		once.Do(func() {
			h.mu.Lock()
			defer h.mu.Unlock()
			if _, ok := h.subs[id]; !ok {
				return
			}
			delete(h.subs, id)
			close(ch)
		})
	}
	return ch, cancel
}

// Stats returns a snapshot for health checks.
func (h *Hub) Stats() Stats {
	h.mu.Lock()
	defer h.mu.Unlock()
	return Stats{Seq: h.seq, Subscribers: len(h.subs), Dropped: h.dropped}
}

func (h *Hub) appendHistoryLocked(ev Event) {
	if h.histSize <= 0 {
		return
	}
	if len(h.history) < h.histSize {
		h.history = append(h.history, ev)
		h.histLen = len(h.history)
		return
	}
	h.history[h.histNext] = ev
	h.histNext = (h.histNext + 1) % h.histSize
	h.histLen = h.histSize
}

func (h *Hub) replayLocked(topics []string, since uint64) []Event {
	if since == 0 || h.histLen == 0 {
		return nil
	}
	start := 0
	if h.histLen == h.histSize {
		start = h.histNext
	}
	out := make([]Event, 0, h.histLen)
	for i := 0; i < h.histLen; i++ {
		ev := h.history[(start+i)%h.histSize]
		if ev.Seq <= since || !matches(topics, ev.Topic) {
			continue
		}
		out = append(out, ev)
	}
	return out
}

func normalizeTopics(topics []string) []string {
	out := make([]string, 0, len(topics))
	for _, group := range topics {
		for _, topic := range strings.Split(group, ",") {
			topic = strings.TrimPrefix(strings.TrimSpace(topic), "event.")
			if topic != "" {
				out = append(out, topic)
			}
		}
	}
	return out
}

func matches(patterns []string, topic string) bool {
	if len(patterns) == 0 {
		return true
	}
	for _, pattern := range patterns {
		if pattern == "*" || pattern == topic {
			return true
		}
		if strings.HasSuffix(pattern, ".*") {
			prefix := strings.TrimSuffix(pattern, "*")
			if strings.HasPrefix(topic, prefix) {
				return true
			}
		}
	}
	return false
}
