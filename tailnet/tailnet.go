// Package tailnet joins a server to a Tailscale network as a node of its
// own, using tsnet's userspace networking, so the host needs no tailscaled.
// It provides a listener on the node's MagicDNS name, HTTPS by default with
// a certificate Tailscale obtains, and two ways to decide who is let in.
//
// A server with users of its own gives the node's WhoIs to the auth
// package's Tailscale middleware, which finds the user who holds the login
// that a request comes from.  A server with one user, such as one whose
// directory is auth.Solo, wraps its handler in RequireLogin, which admits
// the logins it is told to and no others.
//
// This package is a module of its own, apart from the rest of Gizmo, so
// that a server with no use for Tailscale doesn't inherit what Tailscale
// depends on, or the version of Go it wants.
//
// A node's state belongs to the machine it was made on.  State copied to
// another machine leaves the node stuck in Starting, with the health
// message "node OS changed since last connection".  So each machine is its
// own node, under its own name (juke on the server, juke-dev on a laptop),
// and state is never moved.
package tailnet

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"tailscale.com/client/local"
	"tailscale.com/tsnet"

	"github.com/rjbs/gizmo/auth"
)

// Config says what node to be.
type Config struct {
	// Hostname is the node's name, which becomes the first part of its
	// MagicDNS name: <hostname>.<tailnet>.ts.net.
	Hostname string

	// Dir is where the node keeps its state, so that it needs authorizing
	// only once.  It must not be copied to another machine.
	Dir string

	// Logger is told of the node's progress and of requests refused.  If
	// nil, slog.Default() is.
	Logger *slog.Logger

	// Verbose sends tsnet's own log, which is chatty, to Logger at the
	// Info level.  Otherwise it goes at Debug, where most loggers drop it.
	Verbose bool
}

// Node is this server's node on the tailnet.
type Node struct {
	srv    *tsnet.Server
	lc     *local.Client
	logger *slog.Logger

	// Hostname is the name the node was given.
	Hostname string

	// DNSName is the node's MagicDNS name, like "juke.example.ts.net".
	DNSName string
}

// Start brings the node up and waits until it is connected.  The first time
// a node runs it must be authorized: tsnet uses an auth key from the
// TS_AUTHKEY environment variable if there is one, and otherwise logs a
// login URL to visit.  State is kept in the Config's Dir, so later starts
// need no authorization.
func Start(ctx context.Context, cfg Config) (*Node, error) {
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	chatter := slog.LevelDebug
	if cfg.Verbose {
		chatter = slog.LevelInfo
	}
	srv := &tsnet.Server{
		Hostname: cfg.Hostname,
		Dir:      cfg.Dir,
		UserLogf: func(format string, args ...any) {
			logger.Info("tailscale: " + fmt.Sprintf(format, args...))
		},
		Logf: func(format string, args ...any) {
			logger.Log(ctx, chatter, "tailscale: "+fmt.Sprintf(format, args...))
		},
	}
	lc, err := srv.LocalClient()
	if err != nil {
		srv.Close()
		return nil, err
	}
	stopReporting := reportWhileStarting(ctx, lc, logger)
	status, err := srv.Up(ctx)
	stopReporting()
	if err != nil {
		srv.Close()
		return nil, err
	}
	n := &Node{srv: srv, lc: lc, logger: logger, Hostname: cfg.Hostname}
	if status.Self != nil {
		n.DNSName = strings.TrimSuffix(status.Self.DNSName, ".")
	}
	return n, nil
}

// Listen listens on the node: with https, on port 443 with a certificate
// for the node's MagicDNS name, which needs both MagicDNS and HTTPS
// certificates enabled in the tailnet's admin console; otherwise on port
// 80 in plain HTTP, which the tailnet still encrypts on the wire.  The
// first HTTPS connection waits, perhaps half a minute, while the
// certificate is obtained.
func (n *Node) Listen(https bool) (net.Listener, error) {
	if https {
		return n.srv.ListenTLS("tcp", ":443")
	}
	return n.srv.Listen("tcp", ":80")
}

// URL is the base URL clients reach the node at, with no trailing slash.
func (n *Node) URL(https bool) string {
	if https {
		return "https://" + n.DNSName
	}
	return "http://" + n.DNSName
}

// Names are the names a client on the tailnet may address the node by: its
// hostname alone, and its MagicDNS name.  They are for the HTTP server's
// Guard, which refuses requests addressed to names it doesn't know.
func (n *Node) Names() []string {
	return []string{n.Hostname, n.DNSName}
}

// WhoIs resolves a connection's remote address to the identity behind it.
// It is an auth.WhoIsFunc, for auth.Tailscale and for RequireLogin.
func (n *Node) WhoIs(ctx context.Context, remoteAddr string) (auth.Identity, error) {
	who, err := n.lc.WhoIs(ctx, remoteAddr)
	if err != nil {
		return auth.Identity{}, err
	}
	id := auth.Identity{Tagged: who.Node != nil && who.Node.IsTagged()}
	if who.UserProfile != nil {
		id.Login = who.UserProfile.LoginName
	}
	return id, nil
}

// RequireLogin is RequireLogin with the node's own WhoIs and logger.
func (n *Node) RequireLogin(allowed []string, next http.Handler) http.Handler {
	return RequireLogin(n.WhoIs, allowed, n.logger, next)
}

// Close takes the node off the tailnet.
func (n *Node) Close() error {
	return n.srv.Close()
}

// RequireLogin admits a request only if whois says it comes from an
// untagged node of one of the allowed logins, which are whole login names
// like "rjbs@github".  Anything else gets 401 if it can't be identified,
// or 403, and is logged with its login, so a login that should have been
// allowed can be added.
//
// It doesn't say who the request is from, only that it may come in.  It is
// for a server with one user, where that is the whole question.
func RequireLogin(whois auth.WhoIsFunc, allowed []string, logger *slog.Logger, next http.Handler) http.Handler {
	allow := map[string]bool{}
	for _, login := range allowed {
		allow[login] = true
	}
	refuse := func(w http.ResponseWriter, r *http.Request, status int, why string, more ...any) {
		logger.Warn("refused a request from the tailnet", append([]any{"method", r.Method, "path", r.URL.Path, "from", r.RemoteAddr, "why", why}, more...)...)
		http.Error(w, http.StatusText(status), status)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, err := whois(r.Context(), r.RemoteAddr)
		switch {
		case err != nil:
			refuse(w, r, http.StatusUnauthorized, "can't identify it", "error", err)
		case id.Tagged:
			refuse(w, r, http.StatusForbidden, "a tagged node")
		case !allow[id.Login]:
			refuse(w, r, http.StatusForbidden, "login is not allowed", "login", id.Login)
		default:
			next.ServeHTTP(w, r)
		}
	})
}

// reportWhileStarting logs the node's backend state and health every ten
// seconds until the returned function is called.  tsnet's Up blocks
// silently while the node can't reach the control server, and its own log
// is too chatty to show by default, so without this a node stuck in
// Starting looks exactly like one that's fine.  -- claude, 2026-10-30
func reportWhileStarting(ctx context.Context, lc *local.Client, logger *slog.Logger) (stop func()) {
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			st, err := lc.StatusWithoutPeers(ctx)
			if err != nil {
				if ctx.Err() == nil {
					logger.Warn("tailscale node not yet running", "error", err)
				}
				continue
			}
			if st.BackendState == "Running" {
				continue
			}
			logger.Warn("tailscale node not yet running", "state", st.BackendState, "health", st.Health, "authURL", st.AuthURL)
		}
	}()
	return func() {
		cancel()
		<-done
	}
}
