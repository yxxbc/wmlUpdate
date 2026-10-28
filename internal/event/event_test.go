package event

import "testing"

func TestBufferStableSequence(t *testing.T) {
	b := NewBuffer(2)
	for i := 0; i < 5; i++ {
		b.Emit(Event{Type: TypeLog, Message: string(rune('a' + i))})
	}
	events, base := b.Snapshot()
	if len(events) != 2 || base != 3 {
		t.Fatalf("len=%d base=%d", len(events), base)
	}
	if events[0].Message != "d" || events[1].Message != "e" {
		t.Fatalf("events = %+v", events)
	}
}
