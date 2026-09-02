// Package classify defines the bounded traffic classes used by Bondify schedulers.
package classify

// Class identifies the traffic priority class used for scheduling and budgeting.
type Class uint8

const (
	Latency Class = iota
	Realtime
	Interactive
	Bulk
)

func (c Class) String() string {
	switch c {
	case Latency:
		return "LATENCY"
	case Realtime:
		return "REALTIME"
	case Interactive:
		return "INTERACTIVE"
	case Bulk:
		return "BULK"
	default:
		return "UNKNOWN"
	}
}
