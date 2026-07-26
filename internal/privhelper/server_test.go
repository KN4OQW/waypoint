package privhelper_test

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KN4OQW/waypoint/internal/privhelper"
	"github.com/KN4OQW/waypoint/internal/privhelper/privtest"
)

// serve starts a server backed by a fresh fake on a temporary socket and returns
// a client for it. Everything is torn down when the test ends.
//
// The socket lives in os.MkdirTemp rather than t.TempDir because a Unix socket
// path is capped at 108 bytes and the conformance suite's subtest names — which
// t.TempDir folds into the directory name — blow straight past it.
func serve(t *testing.T) (*privhelper.Client, *privtest.Fake) {
	t.Helper()
	dir, err := os.MkdirTemp("", "wph")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, "s.sock")

	fake := privtest.NewFake()
	srv := &privhelper.Server{
		Prov: fake,
		// Tests run as an ordinary user and the socket is in a private temp dir;
		// the peer check is exercised separately, in TestUnauthorizedPeer.
		AllowedGID: -1,
		Logf:       func(string, ...any) {},
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = srv.Serve(ctx, ln)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	return privhelper.NewClient(path), fake
}

// The contract has to survive the socket. Running the same conformance suite the
// fake passes in-process, but over the wire, is what proves the transport
// preserves it — including the error codes, which are the part most likely to be
// flattened into a generic failure by a serialisation layer.
func TestClientServerConforms(t *testing.T) {
	privtest.Conform(t, func(t *testing.T) privhelper.Provisioner {
		c, _ := serve(t)
		return c
	})
}

// Property: a call actually reaches the backend, rather than the client somehow
// answering from its own state.
func TestCallReachesTheBackend(t *testing.T) {
	c, fake := serve(t)

	got, err := c.SetHostname(context.Background(), privhelper.SetHostnameRequest{
		Hostname: "hs-shack", UpdateHosts: true})
	if err != nil {
		t.Fatal(err)
	}
	if !got.Changed || got.Hostname != "hs-shack" {
		t.Errorf("response = %+v", got)
	}
	if fake.Hostname != "hs-shack" {
		t.Errorf("the backend's hostname is %q", fake.Hostname)
	}
	if !fake.HostsUpdated {
		t.Error("UpdateHosts did not reach the backend")
	}
}

// Property: a method this helper does not know is refused as out-of-vocabulary,
// not silently ignored. The likeliest cause is a newer waypointd talking to an
// older helper after a rollback, and it must be diagnosable from one log line.
func TestUnknownMethodIsRefused(t *testing.T) {
	c, _ := serve(t)
	conn, err := net.Dial("unix", strings.TrimSpace(c.Path))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close() //nolint:errcheck // test connection

	req, err := privhelper.NewRequest(1, "run_arbitrary_command", map[string]string{"cmd": "rm -rf /"})
	if err != nil {
		t.Fatal(err)
	}
	if err := privhelper.WriteFrame(conn, req); err != nil {
		t.Fatal(err)
	}
	var resp privhelper.Response
	if err := privhelper.ReadFrame(conn, &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Error == nil {
		t.Fatal("an unknown method was accepted")
	}
	if privhelper.CodeOf(resp.Error) != privhelper.CodeUnsupported {
		t.Errorf("code = %q, want %q", privhelper.CodeOf(resp.Error), privhelper.CodeUnsupported)
	}
	if !strings.Contains(resp.Error.Message, "run_arbitrary_command") {
		t.Errorf("the error does not name the method: %v", resp.Error)
	}
	if resp.ID != 1 {
		t.Errorf("ID = %d, want the request's id echoed back", resp.ID)
	}
}

// Property: a params field the helper does not understand is refused rather than
// dropped. A newer client asking for something this helper cannot do must fail
// loudly — quietly creating the account without the part it did not understand is
// the worse outcome.
func TestUnknownParamsFieldIsRefused(t *testing.T) {
	c, fake := serve(t)
	conn, err := net.Dial("unix", c.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close() //nolint:errcheck // test connection

	params := json.RawMessage(`{"username":"rescue","password":"correct horse","sudo":true,"uid":0}`)
	req := privhelper.Request{Version: privhelper.ProtocolVersion, ID: 5,
		Method: privhelper.MethodCreateRecoveryUser, Params: params}
	if err := privhelper.WriteFrame(conn, req); err != nil {
		t.Fatal(err)
	}
	var resp privhelper.Response
	if err := privhelper.ReadFrame(conn, &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Error == nil {
		t.Fatal("an unknown params field was accepted")
	}
	if privhelper.CodeOf(resp.Error) != privhelper.CodeProtocol {
		t.Errorf("code = %q, want %q", privhelper.CodeOf(resp.Error), privhelper.CodeProtocol)
	}
	if len(fake.Users) != 0 {
		t.Error("the rejected request still created an account")
	}
}

// Property: a request that fails validation is refused by the server too, not
// only by the client. The client's check is for a fast, useful message; the
// server's is because a request arriving on a socket is input.
func TestServerRevalidates(t *testing.T) {
	c, fake := serve(t)
	conn, err := net.Dial("unix", c.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close() //nolint:errcheck // test connection

	// Sent as a raw frame, bypassing the client's own validation entirely.
	req := privhelper.Request{Version: privhelper.ProtocolVersion, ID: 9,
		Method: privhelper.MethodSetHostname,
		Params: json.RawMessage(`{"hostname":"raspberrypi","update_hosts":true}`)}
	if err := privhelper.WriteFrame(conn, req); err != nil {
		t.Fatal(err)
	}
	var resp privhelper.Response
	if err := privhelper.ReadFrame(conn, &resp); err != nil {
		t.Fatal(err)
	}
	if privhelper.CodeOf(resp.Error) != privhelper.CodeInvalidArgument {
		t.Fatalf("code = %q (err %v), want %q", privhelper.CodeOf(resp.Error), resp.Error, privhelper.CodeInvalidArgument)
	}
	if fake.Hostname != privtest.DefaultHostname {
		t.Errorf("the rejected rename took effect: %q", fake.Hostname)
	}
}

// Property: a version mismatch is reported as one.
func TestVersionMismatchIsRefused(t *testing.T) {
	c, _ := serve(t)
	conn, err := net.Dial("unix", c.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close() //nolint:errcheck // test connection

	req := privhelper.Request{Version: privhelper.ProtocolVersion + 1, ID: 3, Method: privhelper.MethodLockRoot}
	if err := privhelper.WriteFrame(conn, req); err != nil {
		t.Fatal(err)
	}
	var resp privhelper.Response
	if err := privhelper.ReadFrame(conn, &resp); err != nil {
		t.Fatal(err)
	}
	if privhelper.CodeOf(resp.Error) != privhelper.CodeProtocol {
		t.Errorf("code = %q, want %q", privhelper.CodeOf(resp.Error), privhelper.CodeProtocol)
	}
}

// Property: several calls run over one client without interfering, and each gets
// its own response. The client dials per call, so this is really a check that
// nothing shared between calls carries state it should not.
func TestConcurrentCalls(t *testing.T) {
	c, fake := serve(t)
	ctx := context.Background()

	if _, err := c.CreateRecoveryUser(ctx, privhelper.CreateRecoveryUserRequest{
		Username: "rescue", SSHKey: privtest.SSHPublicKey(1), Sudo: true}); err != nil {
		t.Fatal(err)
	}

	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		go func() {
			_, err := c.LockRoot(ctx, privhelper.LockRootRequest{})
			errs <- err
		}()
	}
	for i := 0; i < 8; i++ {
		if err := <-errs; err != nil {
			t.Errorf("concurrent LockRoot: %v", err)
		}
	}
	if !fake.RootLocked {
		t.Error("root was not locked")
	}
}

// Property: a peer that is not authorized is told so and gets no further, and the
// rejection carries no path or state in its message.
func TestUnauthorizedPeer(t *testing.T) {
	dir, err := os.MkdirTemp("", "wph")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, "s.sock")

	fake := privtest.NewFake()
	// A gid this test process cannot possibly be in.
	srv := &privhelper.Server{Prov: fake, AllowedGID: 0x7ffffffe, Logf: func(string, ...any) {}}
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = srv.Serve(ctx, ln) }()
	t.Cleanup(func() { cancel(); <-done })

	if os.Geteuid() == 0 {
		t.Skip("running as root, which is always authorized")
	}
	_, err = privhelper.NewClient(path).SetHostname(context.Background(),
		privhelper.SetHostnameRequest{Hostname: "hs-shack"})
	if err == nil {
		t.Fatal("an unauthorized peer was served")
	}
	if privhelper.CodeOf(err) != privhelper.CodeDenied {
		t.Errorf("code = %q (err %v), want %q", privhelper.CodeOf(err), err, privhelper.CodeDenied)
	}
	if fake.Hostname != privtest.DefaultHostname {
		t.Error("an unauthorized peer changed the hostname")
	}
}

// Property: the dispatch table covers the protocol. A method added to Provisioner
// but not wired here would otherwise fail only in production, as an
// unsupported-method error on the one call that needed it.
func TestDispatchCoversEveryMethod(t *testing.T) {
	srv := &privhelper.Server{Prov: privtest.NewFake(), AllowedGID: -1}
	got := srv.DispatchedMethods()
	if len(got) != len(privhelper.Methods()) {
		t.Fatalf("the server dispatches %d methods, the protocol declares %d: %v",
			len(got), len(privhelper.Methods()), got)
	}
}

// Property: Listen refuses to unlink something that is not a socket, so a
// misconfigured path cannot make the helper delete a real file.
func TestListenRefusesToRemoveANonSocket(t *testing.T) {
	dir, err := os.MkdirTemp("", "wph")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, "important.conf")
	if err := os.WriteFile(path, []byte("keep me"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := privhelper.Listen(path, os.Getgid()); err == nil {
		t.Fatal("Listen accepted a path holding a regular file")
	}
	if b, err := os.ReadFile(path); err != nil || string(b) != "keep me" {
		t.Errorf("the file was disturbed: %q %v", b, err)
	}
}
