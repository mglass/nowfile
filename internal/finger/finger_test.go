package finger

import (
	"context"
	"io"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mglass/nowfile/internal/format"
	"github.com/mglass/nowfile/internal/store"
)

var now = time.Date(2026, 10, 13, 12, 30, 0, 0, time.UTC)

var mike = format.Document{
	Version:  format.CurrentVersion,
	Name:     "Mike Glass",
	Headline: "Builds internal tools.",
	Slots: []format.Slot{{
		Key: "Status", Value: "Lunch, back at 1",
		Until: time.Date(2026, 10, 13, 13, 0, 0, 0, time.UTC),
	}},
	Plan: "Reply within a day.",
}

func testServer() *Server {
	return &Server{
		Host: "nowfile.org",
		Lookup: func(_ context.Context, name string, _ time.Time) (format.Document, bool) {
			if name == "mike" {
				return mike, true
			}
			return format.Document{}, false
		},
		List: func(context.Context) ([]string, error) { return []string{"ann", "mike"}, nil },
		Now:  func() time.Time { return now },
		Logf: func(string, ...any) {},
	}
}

// query sends q on a net.Pipe to s and returns everything s writes back.
func query(t *testing.T, s *Server, q string) string {
	t.Helper()
	client, server := net.Pipe()
	done := make(chan struct{})
	go func() { s.ServeConn(context.Background(), server); close(done) }()
	// net.Pipe writes block until read; the server may stop reading early.
	go client.Write([]byte(q))
	client.SetReadDeadline(time.Now().Add(5 * time.Second))
	out, err := io.ReadAll(client)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	client.Close()
	<-done
	return string(out)
}

func wire(s string) string { return strings.ReplaceAll(s, "\n", "\r\n") }

func TestQueries(t *testing.T) {
	short := wire(format.Render(mike, false))
	long := wire(format.Render(mike, true))
	none := NoSuchUser + "\r\n"
	tests := []struct {
		name, q, want string
	}{
		{"short", "mike\r\n", short},
		{"short LF only", "mike\n", short},
		{"uppercase", "MIKE\r\n", short},
		{"long /W", "/W mike\r\n", long},
		{"long /w lowercase", "/w mike\r\n", long},
		{"long -l", "-l mike\r\n", long},
		{"own host", "mike@nowfile.org\r\n", short},
		{"own host uppercase", "/W Mike@NowFile.org\r\n", long},
		{"empty lists", "\r\n", "ann\r\nmike\r\n"},
		{"/W alone lists", "/W\r\n", "ann\r\nmike\r\n"},
		{"forward", "mike@example.com\r\n", NoForwarding + "\r\n"},
		{"forward chain", "mike@nowfile.org@example.com\r\n", NoForwarding + "\r\n"},
		{"bare host", "@example.com\r\n", NoForwarding + "\r\n"},
		{"unknown", "nobodyhere\r\n", none},
		{"reserved", "admin\r\n", none},
		{"malformed", "x!\r\n", none},
		{"own host empty name", "@nowfile.org\r\n", none},
		{"two names", "mike ann\r\n", OneUser + "\r\n"},
		{"/Wmike is a name", "/Wmike\r\n", none},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := query(t, testServer(), tt.q); got != tt.want {
				t.Errorf("query %q:\n got %q\nwant %q", tt.q, got, tt.want)
			}
		})
	}
}

func TestShortOmitsPlanLongHasIt(t *testing.T) {
	s := testServer()
	if got := query(t, s, "mike\r\n"); strings.Contains(got, "Plan:") {
		t.Errorf("short form has Plan block: %q", got)
	}
	if got := query(t, s, "/W mike\r\n"); !strings.Contains(got, "\r\nPlan:\r\nReply within a day.\r\n") {
		t.Errorf("long form missing Plan block: %q", got)
	}
}

func TestEveryLineCRLF(t *testing.T) {
	d := mike
	d.Plan = "one\r\ntwo\nthree"
	s := testServer()
	s.Lookup = func(context.Context, string, time.Time) (format.Document, bool) { return d, true }
	got := query(t, s, "/W mike\r\n")
	if strings.Contains(strings.ReplaceAll(got, "\r\n", ""), "\n") || strings.Contains(got, "\r\r") {
		t.Errorf("bad line endings: %q", got)
	}
	if !strings.HasSuffix(got, "three\r\n") {
		t.Errorf("missing final CRLF: %q", got)
	}
}

func TestListingDisabled(t *testing.T) {
	s := testServer()
	s.List = nil
	if got, want := query(t, s, "\r\n"), NoListing+"\r\n"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestOwnHostUnset(t *testing.T) {
	s := testServer()
	s.Host = ""
	if got, want := query(t, s, "mike@nowfile.org\r\n"), NoForwarding+"\r\n"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestOversizedLine(t *testing.T) {
	s := testServer()
	lookups := 0
	s.Lookup = func(context.Context, string, time.Time) (format.Document, bool) { lookups++; return mike, true }
	got := query(t, s, strings.Repeat("m", 600)+"\r\n")
	if want := QueryTooLong + "\r\n"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if lookups != 0 {
		t.Errorf("oversized query reached Lookup")
	}
	// Exactly MaxLine bytes including CRLF is accepted.
	if got := query(t, testServer(), strings.Repeat(" ", MaxLine-6)+"mike\r\n"); !strings.HasPrefix(got, "nowfile/0\r\n") {
		t.Errorf("query of MaxLine bytes refused: %q", got)
	}
}

func TestSlowClientTimesOut(t *testing.T) {
	s := testServer()
	s.ReadTimeout = 50 * time.Millisecond
	client, server := net.Pipe()
	defer client.Close()
	done := make(chan struct{})
	go func() { s.ServeConn(context.Background(), server); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("server did not time out a silent client")
	}
	client.SetReadDeadline(time.Now().Add(time.Second))
	if out, _ := io.ReadAll(client); len(out) != 0 {
		t.Errorf("silent client got %q", out)
	}
}

func TestSlowReaderTimesOut(t *testing.T) {
	s := testServer()
	s.WriteTimeout = 50 * time.Millisecond
	client, server := net.Pipe()
	defer client.Close()
	done := make(chan struct{})
	go func() { s.ServeConn(context.Background(), server); close(done) }()
	client.Write([]byte("mike\r\n"))
	// Never read the reply.
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("server did not time out a client that never reads")
	}
}

func TestServeLoopback(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { errc <- testServer().Serve(ctx, ln) }()

	// The last query has no line ending; the client half-closes instead.
	for _, q := range []string{"mike@nowfile.org\r\n", "mike\r\n", "mike"} {
		conn, err := net.Dial("tcp", ln.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		conn.SetDeadline(time.Now().Add(5 * time.Second))
		conn.Write([]byte(q))
		conn.(*net.TCPConn).CloseWrite()
		out, err := io.ReadAll(conn)
		conn.Close()
		if err != nil {
			t.Fatal(err)
		}
		if want := wire(format.Render(mike, false)); string(out) != want {
			t.Errorf("got %q, want %q", out, want)
		}
	}

	cancel()
	select {
	case err := <-errc:
		if err != nil {
			t.Errorf("Serve after cancel: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Serve did not return after cancel")
	}
}

// TestFromStore checks the store adapter: a live user is served, and
// unknown, suspended, and deleted users all get the same line.
func TestFromStore(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	mk := func(name string) store.User {
		u, err := st.CreateUser(ctx, name, name+"@example.com")
		if err != nil {
			t.Fatal(err)
		}
		if err := st.PutStatic(ctx, u.ID, store.Static{Name: strings.ToUpper(name), Plan: "plan of " + name}); err != nil {
			t.Fatal(err)
		}
		return u
	}
	held, err := st.CreateUserHeld(ctx, "mike", "mike@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.PutStatic(ctx, held.ID, store.Static{Name: "Mike Glass"}); err != nil {
		t.Fatal(err)
	}
	ann := mk("ann")
	bob := mk("bob")
	cat := mk("cat")
	if err := st.SetDirectory(ctx, ann.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := st.SetDirectory(ctx, bob.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := st.Suspend(ctx, "bob"); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteUser(ctx, cat.ID); err != nil {
		t.Fatal(err)
	}

	s := FromStore(st, "https://nowfile.org")
	s.Logf = func(f string, args ...any) { t.Errorf("unexpected log: "+f, args...) }
	if s.Host != "nowfile.org" {
		t.Errorf("Host = %q", s.Host)
	}
	if got := query(t, s, "/W ann@nowfile.org\r\n"); !strings.Contains(got, "Name: ANN\r\n") || !strings.Contains(got, "plan of ann") {
		t.Errorf("ann: %q", got)
	}
	if got := query(t, s, "Mike\r\n"); !strings.Contains(got, "Name: Mike Glass\r\n") {
		t.Errorf("held name mike: %q", got)
	}
	want := NoSuchUser + "\r\n"
	for _, q := range []string{"bob", "cat", "dan", "admin"} {
		if got := query(t, s, q+"\r\n"); got != want {
			t.Errorf("%s: got %q, want %q", q, got, want)
		}
	}
	if got := query(t, s, "\r\n"); got != "ann\r\n" {
		t.Errorf("listing: got %q, want only ann", got)
	}
}
