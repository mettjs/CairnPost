package api

import "sync"

// uploads counts the bytes of the large sends being taken now.
type uploads struct {
	mu    sync.Mutex
	taken int64
}

// take makes room for a request body of size bytes and answers how to
// give the room back; false when there is none. A body no larger than
// small needs no room. One whose size is not said (below 0) is counted
// as the largest a send may be.
func (u *uploads) take(size, small, largest, room int64) (release func(), ok bool) {
	if size < 0 {
		size = largest
	}
	if room <= 0 || size <= small {
		return func() {}, true
	}
	size = min(size, largest) // more than that is refused as it is read
	u.mu.Lock()
	defer u.mu.Unlock()
	// The first is always taken: a bound below one email would take none.
	if u.taken > 0 && u.taken+size > room {
		return nil, false
	}
	u.taken += size
	return func() {
		u.mu.Lock()
		u.taken -= size
		u.mu.Unlock()
	}, true
}
