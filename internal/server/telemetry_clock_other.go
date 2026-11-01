//go:build !linux

package server

// hostClock has no shared monotonic clock to offer off Linux; events carry wall
// time only, which a console treats as a different clock domain.
func hostClock() (id string, now func() (int64, bool)) {
	return "golive:local", func() (int64, bool) { return 0, false }
}
