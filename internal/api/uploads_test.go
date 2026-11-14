package api

import "testing"

// Only so much of large sends is taken at one time; small requests
// need no room, and room given back is room again.
func TestLargeSendsAreTakenOnlySoFar(t *testing.T) {
	var u uploads
	const small, largest, room = 10, 100, 150
	if _, ok := u.take(5, small, largest, room); !ok || u.taken != 0 {
		t.Fatal("a small request took room")
	}
	first, ok := u.take(90, small, largest, room)
	if !ok || u.taken != 90 {
		t.Fatalf("the first: %v %d", ok, u.taken)
	}
	if _, ok := u.take(-1, small, largest, room); ok {
		t.Fatal("a send of unsaid size was taken past the room")
	}
	second, ok := u.take(60, small, largest, room)
	if !ok {
		t.Fatal("what fits was not taken")
	}
	first()
	second()
	if u.taken != 0 {
		t.Fatalf("room not given back: %d", u.taken)
	}
	// One email is always taken, and no bound takes all.
	if _, ok := u.take(100, small, largest, 50); !ok {
		t.Fatal("the only send was refused")
	}
	var free uploads
	if _, ok := free.take(1<<40, small, largest, 0); !ok {
		t.Fatal("refused with no bound set")
	}
}
