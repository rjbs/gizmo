package jmap

// Limits are the limits a server advertises in its core capability (RFC 8620
// §2).  A zero field takes its default.
type Limits struct {
	MaxSizeUpload         int64
	MaxConcurrentUpload   int
	MaxSizeRequest        int64
	MaxConcurrentRequests int
	MaxCallsInRequest     int
	MaxObjectsInGet       int
	MaxObjectsInSet       int
}

// WithDefaults returns the limits with every zero field set to its default.
// MaxSizeUpload has none: a server that takes no uploads advertises zero.
func (l Limits) WithDefaults() Limits {
	if l.MaxConcurrentUpload == 0 {
		l.MaxConcurrentUpload = 4
	}
	if l.MaxSizeRequest == 0 {
		l.MaxSizeRequest = 10_000_000
	}
	if l.MaxConcurrentRequests == 0 {
		l.MaxConcurrentRequests = 4
	}
	if l.MaxCallsInRequest == 0 {
		l.MaxCallsInRequest = 64
	}
	if l.MaxObjectsInGet == 0 {
		l.MaxObjectsInGet = 1000
	}
	if l.MaxObjectsInSet == 0 {
		l.MaxObjectsInSet = 1000
	}
	return l
}
