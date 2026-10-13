// Package finger serves nowfile documents over the finger protocol
// (RFC 1288), following the transport section of docs/format.md: one query
// line in, one document out, close.
package finger

import (
	"bufio"
	"context"
	"errors"
	"log"
	"net"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/mglass/nowfile/internal/format"
	"github.com/mglass/nowfile/internal/store"
	"github.com/mglass/nowfile/internal/username"
)

const (
	// MaxLine is the longest query line accepted, including the line ending.
	MaxLine = 512
	// DefaultTimeout is the read and write deadline when a Server leaves
	// its timeouts zero.
	DefaultTimeout = 10 * time.Second
	// DefaultMaxConns is the connection cap when a Server leaves MaxConns
	// zero.
	DefaultMaxConns = 64
)

// Replies that are not documents. Each is one line. NoSuchUser is the only
// answer for unknown, suspended, deleted, reserved, and malformed names, so
// a query cannot tell them apart.
const (
	NoSuchUser   = "finger: no such user."
	NoForwarding = "finger: forwarding refused."
	NoListing    = "finger: user listing is not available."
	QueryTooLong = "finger: query too long."
	OneUser      = "finger: one user per query."
)

// Server answers finger queries. Lookup is required; the rest are optional.
type Server struct {
	// Host is this server's own host name, for example "nowfile.org". A
	// query "name@Host" is answered as "name"; any other "@" is refused.
	Host string
	// Lookup returns the composed document for a canonical username at
	// now, or false for any reason the user should not be shown.
	Lookup func(ctx context.Context, name string, now time.Time) (format.Document, bool)
	// List returns the names for an empty query. Nil disables listing.
	List func(ctx context.Context) ([]string, error)
	// Now defaults to time.Now.
	Now func() time.Time
	// ReadTimeout and WriteTimeout default to DefaultTimeout.
	ReadTimeout  time.Duration
	WriteTimeout time.Duration
	// MaxConns caps concurrent connections, default DefaultMaxConns.
	// Connections past the cap are closed without a reply.
	MaxConns int
	// Logf defaults to log.Printf.
	Logf func(format string, args ...any)
}

// FromStore returns a Server backed by st. The host is taken from baseURL
// (for example "https://nowfile.org"). Listing returns the users who opted
// into the directory.
func FromStore(st *store.Store, baseURL string) *Server {
	var host string
	if u, err := url.Parse(baseURL); err == nil {
		host = u.Hostname()
	}
	s := &Server{Host: host}
	s.Lookup = func(ctx context.Context, name string, now time.Time) (format.Document, bool) {
		doc, err := st.DocumentByName(ctx, name, now)
		if err != nil {
			if !errors.Is(err, store.ErrNotFound) && !errors.Is(err, store.ErrSuspended) {
				s.logf("finger: lookup %q: %v", name, err)
			}
			return format.Document{}, false
		}
		return doc, true
	}
	s.List = st.DirectoryNames
	return s
}

// ListenAndServe listens on addr and serves until ctx is cancelled.
func ListenAndServe(ctx context.Context, addr string, s *Server) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	return s.Serve(ctx, ln)
}

// Serve accepts connections on ln until ctx is cancelled, then closes ln and
// waits for open connections to finish. It returns nil on cancellation.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	stop := context.AfterFunc(ctx, func() { ln.Close() })
	defer stop()
	s.logf("finger: listening on %s", ln.Addr())

	max := s.MaxConns
	if max <= 0 {
		max = DefaultMaxConns
	}
	sem := make(chan struct{}, max)
	var wg sync.WaitGroup
	defer wg.Wait()

	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			if errors.Is(err, net.ErrClosed) {
				return err
			}
			// Transient (out of file descriptors and the like): back off
			// rather than take the whole binary down.
			s.logf("finger: accept: %v", err)
			select {
			case <-time.After(100 * time.Millisecond):
			case <-ctx.Done():
				return nil
			}
			continue
		}
		select {
		case sem <- struct{}{}:
		default:
			conn.Close()
			continue
		}
		wg.Add(1)
		go func() {
			defer func() { <-sem; wg.Done() }()
			s.ServeConn(ctx, conn)
		}()
	}
}

// ServeConn answers one query on conn and closes it.
func (s *Server) ServeConn(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	conn.SetReadDeadline(time.Now().Add(timeout(s.ReadTimeout)))
	line, err := bufio.NewReaderSize(conn, MaxLine).ReadSlice('\n')
	var reply string
	switch {
	case errors.Is(err, bufio.ErrBufferFull):
		reply = QueryTooLong + "\n"
	case err != nil && len(line) == 0:
		// Timed out or hung up before sending anything.
		return
	default:
		// A query cut off by EOF without a line ending is answered as is.
		reply = s.answer(ctx, string(line))
	}
	conn.SetWriteDeadline(time.Now().Add(timeout(s.WriteTimeout)))
	conn.Write([]byte(crlf(reply)))
}

// answer returns the reply text, with LF line endings, for one query line.
func (s *Server) answer(ctx context.Context, line string) string {
	q := strings.TrimSpace(line)
	long := false
	if f, rest, ok := cutSwitch(q); ok {
		long, q = f, rest
	}
	if q == "" {
		return s.list(ctx)
	}
	if strings.ContainsAny(q, " \t") {
		return OneUser + "\n"
	}
	if i := strings.IndexByte(q, '@'); i >= 0 {
		host := q[i+1:]
		if s.Host == "" || !strings.EqualFold(host, s.Host) {
			return NoForwarding + "\n"
		}
		q = q[:i]
	}
	// ValidateHeld, not Validate: reserved names such as "mike" can be
	// issued by the admin, and the store misses any that were not.
	name, err := username.ValidateHeld(q)
	if err != nil {
		return NoSuchUser + "\n"
	}
	doc, ok := s.Lookup(ctx, name, s.now())
	if !ok {
		return NoSuchUser + "\n"
	}
	return format.Render(doc, long)
}

// cutSwitch strips a leading "/W" (RFC 1288) or "-l" (what some clients
// send for "finger -l") from q. ok reports whether one was present.
func cutSwitch(q string) (long bool, rest string, ok bool) {
	for _, sw := range []string{"/W", "-l"} {
		if len(q) >= len(sw) && strings.EqualFold(q[:len(sw)], sw) {
			rest := q[len(sw):]
			if rest == "" || rest[0] == ' ' || rest[0] == '\t' {
				return true, strings.TrimSpace(rest), true
			}
		}
	}
	return false, q, false
}

func (s *Server) list(ctx context.Context) string {
	if s.List == nil {
		return NoListing + "\n"
	}
	names, err := s.List(ctx)
	if err != nil {
		s.logf("finger: list: %v", err)
		return NoListing + "\n"
	}
	var b strings.Builder
	for _, n := range names {
		b.WriteString(n + "\n")
	}
	return b.String()
}

// crlf converts LF line endings to the CRLF classic finger clients expect,
// without doubling any CR already present.
func crlf(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\n", "\r\n")
}

func timeout(d time.Duration) time.Duration {
	if d <= 0 {
		return DefaultTimeout
	}
	return d
}

func (s *Server) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Server) logf(f string, args ...any) {
	if s.Logf != nil {
		s.Logf(f, args...)
		return
	}
	log.Printf(f, args...)
}
