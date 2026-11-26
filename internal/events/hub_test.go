package events

import (
	"testing"
	"time"
)

func receive(t *testing.T, ch <-chan Event) Event {
	t.Helper()
	select {
	case ev, ok := <-ch:
		if !ok {
			t.Fatal("subscription closed")
		}
		return ev
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for event")
		return Event{}
	}
}

func expectNoEvent(t *testing.T, ch <-chan Event) {
	t.Helper()
	select {
	case ev := <-ch:
		t.Fatalf("unexpected event: %+v", ev)
	case <-time.After(30 * time.Millisecond):
	}
}

func TestPublishOrderAndFilter(t *testing.T) {
	hub := New(Options{Buffer: 8, History: 8})
	ch, cancel := hub.Subscribe([]string{"live.*"}, 0)
	defer cancel()

	hub.Publish("live.message", map[string]any{"text": "hello"})
	hub.Publish("account.login", map[string]any{"mid": 1})
	hub.Publish("live.popularity", map[string]any{"value": 2})

	first := receive(t, ch)
	if first.Seq != 1 || first.Topic != "live.message" {
		t.Fatalf("first = %+v", first)
	}
	second := receive(t, ch)
	if second.Seq != 3 || second.Topic != "live.popularity" {
		t.Fatalf("second = %+v", second)
	}
	expectNoEvent(t, ch)
	if got := hub.Stats().Seq; got != 3 {
		t.Fatalf("seq = %d", got)
	}
}

func TestSubscribeReplaySince(t *testing.T) {
	hub := New(Options{Buffer: 8, History: 8})
	hub.Publish("a.one", 1)
	hub.Publish("a.two", 2)
	hub.Publish("a.three", 3)

	ch, cancel := hub.Subscribe(nil, 1)
	defer cancel()

	if ev := receive(t, ch); ev.Seq != 2 || ev.Topic != "a.two" {
		t.Fatalf("replay first = %+v", ev)
	}
	if ev := receive(t, ch); ev.Seq != 3 || ev.Topic != "a.three" {
		t.Fatalf("replay second = %+v", ev)
	}

	hub.Publish("a.four", 4)
	if ev := receive(t, ch); ev.Seq != 4 || ev.Topic != "a.four" {
		t.Fatalf("live after replay = %+v", ev)
	}
}

func TestReplayRingKeepsNewest(t *testing.T) {
	hub := New(Options{Buffer: 4, History: 3})
	for i := 1; i <= 5; i++ {
		hub.Publish("x", i)
	}

	ch, cancel := hub.Subscribe(nil, 1)
	defer cancel()
	if ev := receive(t, ch); ev.Seq != 3 {
		t.Fatalf("oldest retained seq = %d", ev.Seq)
	}
	ch2, cancel2 := hub.Subscribe(nil, 2)
	defer cancel2()
	if ev := receive(t, ch2); ev.Seq != 3 {
		t.Fatalf("since replay = %d", ev.Seq)
	}
}

func TestSlowSubscriberKeepsLatest(t *testing.T) {
	hub := New(Options{Buffer: 1, History: 8})
	ch, cancel := hub.Subscribe(nil, 0)
	defer cancel()

	hub.Publish("x", 1)
	hub.Publish("x", 2)
	hub.Publish("x", 3)

	if ev := receive(t, ch); ev.Seq != 3 {
		t.Fatalf("latest event = %+v", ev)
	}
	if got := hub.Stats().Dropped; got == 0 {
		t.Fatal("expected dropped events")
	}
}

func TestUnsubscribe(t *testing.T) {
	hub := New(Options{Buffer: 2, History: 2})
	ch, cancel := hub.Subscribe(nil, 0)
	cancel()
	cancel()
	if _, ok := <-ch; ok {
		t.Fatal("channel should be closed")
	}

	hub.Publish("x", 1)
	if got := hub.Stats().Subscribers; got != 0 {
		t.Fatalf("subscribers = %d", got)
	}
}

func TestTopicPatterns(t *testing.T) {
	hub := New(Options{Buffer: 8, History: 8})
	ch, cancel := hub.Subscribe([]string{"live.*", "download.completed"}, 0)
	defer cancel()

	hub.Publish("live.message", 1)
	hub.Publish("download.progress", 2)
	hub.Publish("download.completed", 3)

	if ev := receive(t, ch); ev.Topic != "live.message" {
		t.Fatalf("first = %+v", ev)
	}
	if ev := receive(t, ch); ev.Topic != "download.completed" {
		t.Fatalf("second = %+v", ev)
	}
	expectNoEvent(t, ch)
}
