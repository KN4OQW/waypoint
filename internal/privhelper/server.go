package privhelper

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"sync"

	"golang.org/x/sys/unix"
)

// Server exposes a Provisioner over the Unix socket. It is the root side of the
// boundary, and it treats everything arriving on the socket as input: the peer is
// authenticated, the method is looked up in a fixed table, the params are decoded
// with unknown fields refused, and the request is validated again even though the
// client already validated it. A client is a program, not a promise.
type Server struct {
	// Prov performs the work. Everything else here is about deciding whether to
	// call it.
	Prov Provisioner

	// AllowedGID is the group permitted to call. A peer is accepted when it is
	// uid 0, or when this group is one of its groups. -1 disables the check, which
	// is for tests only — on a real node the socket is the whole security boundary.
	AllowedGID int

	// Logf receives one line per accepted call. Requests redact their own secrets
	// via String; this never formats a raw request struct.
	Logf func(format string, args ...any)
}

// NewServer builds a server for p, resolving the waypoint group. A missing group
// is an error rather than a silent fallback to "anyone": a helper that starts with
// its access control disabled is worse than one that refuses to start.
func NewServer(p Provisioner) (*Server, error) {
	g, err := user.LookupGroup(SocketGroup)
	if err != nil {
		return nil, fmt.Errorf("privhelper: group %q must exist before the helper can start: %w", SocketGroup, err)
	}
	gid, err := strconv.Atoi(g.Gid)
	if err != nil {
		return nil, fmt.Errorf("privhelper: group %q has a non-numeric gid %q", SocketGroup, g.Gid)
	}
	return &Server{Prov: p, AllowedGID: gid, Logf: func(string, ...any) {}}, nil
}

// Listen creates the listening socket at path with the right ownership.
//
// The order matters. The socket is created inside a 0750 directory owned
// root:<gid> and is chmod-ed 0600 by an inherited umask before it is widened to
// 0660, so there is no instant at which it is both bound and world-writable.
// Under systemd socket activation none of this runs — see ListenFromSystemd,
// which is the preferred path precisely because systemd creates the socket with
// the final mode already set.
func Listen(path string, gid int) (net.Listener, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, err
	}
	if err := os.Chown(dir, 0, gid); err != nil && !os.IsPermission(err) {
		return nil, fmt.Errorf("privhelper: chown %s: %w", dir, err)
	}
	// A socket left behind by a killed helper would make Listen fail with
	// "address already in use" even though nothing is listening.
	if err := removeStaleSocket(path); err != nil {
		return nil, err
	}

	old := unix.Umask(0o177) // the socket appears as 0600
	ln, err := net.Listen("unix", path)
	unix.Umask(old)
	if err != nil {
		return nil, err
	}
	if err := os.Chown(path, 0, gid); err != nil {
		_ = ln.Close()
		return nil, fmt.Errorf("privhelper: chown %s: %w", path, err)
	}
	if err := os.Chmod(path, 0o660); err != nil {
		_ = ln.Close()
		return nil, fmt.Errorf("privhelper: chmod %s: %w", path, err)
	}
	return ln, nil
}

// removeStaleSocket deletes path if it is a socket nothing is listening on. It
// refuses to delete anything else, so a misconfigured path cannot make the helper
// unlink a real file.
func removeStaleSocket(path string) error {
	fi, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if fi.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("privhelper: %s exists and is not a socket; refusing to remove it", path)
	}
	if c, err := net.Dial("unix", path); err == nil {
		_ = c.Close()
		return fmt.Errorf("privhelper: %s is already served by another process", path)
	}
	return os.Remove(path)
}

// ListenFromSystemd returns the socket systemd passed in, if any. Socket
// activation is the preferred deployment: systemd creates the socket with
// SocketMode=0660 and SocketGroup=waypoint already applied, so the helper never
// has to widen the permissions of a socket it is already accepting on.
//
// It reports ok=false when the process was not socket-activated, and the caller
// falls back to Listen.
func ListenFromSystemd() (ln net.Listener, ok bool, err error) {
	if os.Getenv("LISTEN_PID") != strconv.Itoa(os.Getpid()) {
		return nil, false, nil
	}
	n, err := strconv.Atoi(os.Getenv("LISTEN_FDS"))
	if err != nil || n < 1 {
		return nil, false, nil
	}
	if n > 1 {
		return nil, false, fmt.Errorf("privhelper: systemd passed %d sockets, expected 1", n)
	}
	const firstFD = 3 // SD_LISTEN_FDS_START
	unix.CloseOnExec(firstFD)
	f := os.NewFile(firstFD, "systemd-socket")
	l, err := net.FileListener(f)
	_ = f.Close()
	if err != nil {
		return nil, false, fmt.Errorf("privhelper: adopt systemd socket: %w", err)
	}
	return l, true, nil
}

// Serve accepts connections until ctx is cancelled or the listener fails.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	var wg sync.WaitGroup
	// Unblocks a blocked Accept on shutdown; the accept loop below reads ctx.Err
	// to tell a cancellation apart from a real listener failure.
	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			wg.Wait()
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer conn.Close() //nolint:errcheck // the connection is finished with either way
			s.serveConn(ctx, conn)
		}()
	}
}

// serveConn authenticates the peer once, then serves calls until the peer closes.
func (s *Server) serveConn(ctx context.Context, conn net.Conn) {
	if err := s.authorize(conn); err != nil {
		s.Logf("privhelper: rejected peer: %v", err)
		// Reported on the wire so a misconfigured client sees why rather than an
		// unexplained disconnect. The message names no path and leaks no state.
		_ = WriteFrame(conn, NewErrorResponse(0, Errorf(CodeDenied, "not authorized to use the provisioning helper")))
		return
	}

	r := bufio.NewReader(conn)
	for {
		var req Request
		if err := ReadFrame(r, &req); err != nil {
			if !errors.Is(err, io.EOF) {
				s.Logf("privhelper: read: %v", err)
			}
			return
		}
		resp := s.handle(ctx, req)
		if err := WriteFrame(conn, resp); err != nil {
			s.Logf("privhelper: write: %v", err)
			return
		}
	}
}

// authorize checks the peer's credentials with SO_PEERCRED. This is the check the
// socket mode cannot make: the kernel reports who is on the other end, and no
// amount of tampering with the socket's permissions changes that answer.
func (s *Server) authorize(conn net.Conn) error {
	if s.AllowedGID < 0 {
		return nil // tests only
	}
	uc, ok := conn.(*net.UnixConn)
	if !ok {
		return fmt.Errorf("connection is %T, not a Unix socket", conn)
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return err
	}
	var cred *unix.Ucred
	var credErr error
	if err := raw.Control(func(fd uintptr) {
		cred, credErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	}); err != nil {
		return err
	}
	if credErr != nil {
		return credErr
	}
	if cred.Uid == 0 {
		return nil
	}
	if int(cred.Gid) == s.AllowedGID {
		return nil
	}
	// The primary gid is all SO_PEERCRED reports, so a peer whose waypoint
	// membership is supplementary needs the group list looked up by uid.
	u, err := user.LookupId(strconv.FormatUint(uint64(cred.Uid), 10))
	if err != nil {
		return fmt.Errorf("uid %d is not in group %s", cred.Uid, SocketGroup)
	}
	gids, err := u.GroupIds()
	if err != nil {
		return err
	}
	want := strconv.Itoa(s.AllowedGID)
	for _, g := range gids {
		if g == want {
			return nil
		}
	}
	return fmt.Errorf("uid %d (%s) is not in group %s", cred.Uid, u.Username, SocketGroup)
}

// handle dispatches one request. Every failure path returns a coded error with
// the request's own id echoed back, so a client can always match a failure to the
// call that caused it.
func (s *Server) handle(ctx context.Context, req Request) Response {
	if err := CheckVersion(req.Version); err != nil {
		return NewErrorResponse(req.ID, err)
	}
	h, ok := s.dispatch()[req.Method]
	if !ok {
		// Out-of-vocabulary method. Named in the error because the likeliest cause
		// is a newer waypointd talking to an older helper after a rollback.
		return NewErrorResponse(req.ID, Errorf(CodeUnsupported,
			"method %q is not in this helper's vocabulary", req.Method))
	}
	result, err := h(ctx, req.Params)
	if err != nil {
		s.Logf("privhelper: %s failed: %v", req.Method, err)
		return NewErrorResponse(req.ID, err)
	}
	resp, err := NewResponse(req.ID, result)
	if err != nil {
		return NewErrorResponse(req.ID, err)
	}
	return resp
}

type handler func(context.Context, json.RawMessage) (any, error)

// decodeParams unmarshals params into a request and validates it.
//
// Unknown fields are refused rather than ignored. A field this helper does not
// understand means the caller believes it is asking for something this helper
// will not do — a newer client asking an older helper to create a user with, say,
// a shell it does not know about should fail loudly, not quietly create the user
// with the wrong shell.
func decodeParams[T Validator](params json.RawMessage) (T, error) {
	var req T
	if len(params) > 0 {
		dec := json.NewDecoder(bytes.NewReader(params))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&req); err != nil {
			return req, Errorf(CodeProtocol, "params: %v", err)
		}
	}
	if err := req.Validate(); err != nil {
		return req, err
	}
	return req, nil
}

// call is the shared shape of every handler: decode, validate, invoke.
func call[T Validator, R any](params json.RawMessage, fn func(T) (R, error)) (any, error) {
	req, err := decodeParams[T](params)
	if err != nil {
		var zero R
		return zero, err
	}
	return fn(req)
}

// dispatch is the method table. It is built from the Provisioner rather than
// declared as a package var so a server always dispatches to its own backend, and
// so TestDispatchCoversEveryMethod can assert it is complete.
func (s *Server) dispatch() map[Method]handler {
	p := s.Prov
	return map[Method]handler{
		MethodSetHostname: func(ctx context.Context, raw json.RawMessage) (any, error) {
			return call(raw, func(r SetHostnameRequest) (SetHostnameResponse, error) {
				s.Logf("privhelper: set_hostname %q", r.Hostname)
				return p.SetHostname(ctx, r)
			})
		},
		MethodCreateRecoveryUser: func(ctx context.Context, raw json.RawMessage) (any, error) {
			return call(raw, func(r CreateRecoveryUserRequest) (CreateRecoveryUserResponse, error) {
				s.Logf("privhelper: %s", r) // String redacts the password
				return p.CreateRecoveryUser(ctx, r)
			})
		},
		MethodInstallSSHKey: func(ctx context.Context, raw json.RawMessage) (any, error) {
			return call(raw, func(r InstallSSHKeyRequest) (InstallSSHKeyResponse, error) {
				s.Logf("privhelper: install_ssh_key for %q", r.Username)
				return p.InstallSSHKey(ctx, r)
			})
		},
		MethodEnableSSH: func(ctx context.Context, raw json.RawMessage) (any, error) {
			return call(raw, func(r EnableSSHRequest) (EnableSSHResponse, error) {
				s.Logf("privhelper: enable_ssh enabled=%v", r.Enabled)
				return p.EnableSSH(ctx, r)
			})
		},
		MethodLockRoot: func(ctx context.Context, raw json.RawMessage) (any, error) {
			return call(raw, func(r LockRootRequest) (LockRootResponse, error) {
				s.Logf("privhelper: lock_root")
				return p.LockRoot(ctx, r)
			})
		},
		MethodAPUp: func(ctx context.Context, raw json.RawMessage) (any, error) {
			return call(raw, func(r APUpRequest) (APUpResponse, error) {
				s.Logf("privhelper: %s", r) // String redacts the passphrase
				return p.APUp(ctx, r)
			})
		},
		MethodAPDown: func(ctx context.Context, raw json.RawMessage) (any, error) {
			return call(raw, func(r APDownRequest) (APDownResponse, error) {
				s.Logf("privhelper: ap_down")
				return p.APDown(ctx, r)
			})
		},
		MethodNetJoin: func(ctx context.Context, raw json.RawMessage) (any, error) {
			return call(raw, func(r NetJoinRequest) (NetJoinResponse, error) {
				s.Logf("privhelper: %s", r) // String redacts the PSK
				return p.NetJoin(ctx, r)
			})
		},
		MethodNetCheckpointCreate: func(ctx context.Context, raw json.RawMessage) (any, error) {
			return call(raw, func(r NetCheckpointCreateRequest) (NetCheckpointCreateResponse, error) {
				s.Logf("privhelper: net_checkpoint_create %ds", r.TimeoutSeconds)
				return p.NetCheckpointCreate(ctx, r)
			})
		},
		MethodNetCheckpointDestroy: func(ctx context.Context, raw json.RawMessage) (any, error) {
			return call(raw, func(r NetCheckpointDestroyRequest) (NetCheckpointDestroyResponse, error) {
				s.Logf("privhelper: net_checkpoint_destroy %q", r.Handle)
				return p.NetCheckpointDestroy(ctx, r)
			})
		},
		MethodNetCheckpointRollback: func(ctx context.Context, raw json.RawMessage) (any, error) {
			return call(raw, func(r NetCheckpointRollbackRequest) (NetCheckpointRollbackResponse, error) {
				s.Logf("privhelper: net_checkpoint_rollback %q", r.Handle)
				return p.NetCheckpointRollback(ctx, r)
			})
		},
	}
}

// DispatchedMethods returns the methods this server answers to. It exists so a
// test can assert the table above covers Methods() — a method added to the
// protocol but not to the table would otherwise fail only in production, as an
// unsupported-method error on the one call that needed it.
func (s *Server) DispatchedMethods() []Method {
	d := s.dispatch()
	out := make([]Method, 0, len(d))
	for _, m := range Methods() {
		if _, ok := d[m]; ok {
			out = append(out, m)
		}
	}
	return out
}
