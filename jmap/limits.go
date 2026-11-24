package jmap

// Limits are the limits a server keeps to.  All but MaxQueryLimit are
// advertised in its core capability (RFC 8620 §2).  A zero field takes its
// default.
//
// One value serves both for the HTTP server, which advertises the limits and
// keeps those on requests and uploads, and for the standard methods, which
// keep those on objects.
type Limits struct {
	MaxSizeUpload         int64
	MaxConcurrentUpload   int
	MaxSizeRequest        int64
	MaxConcurrentRequests int
	MaxCallsInRequest     int
	MaxObjectsInGet       int
	MaxObjectsInSet       int

	// MaxQueryLimit is the most ids one /query will return.  A query that
	// asks for more, or names no limit, gets this many.  There is nowhere
	// in the session to advertise it.
	MaxQueryLimit int
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
	if l.MaxQueryLimit == 0 {
		l.MaxQueryLimit = 1000
	}
	return l
}
