// Package privhelper is the contract between waypointd and the small privileged
// helper that performs the handful of operations waypointd must not perform
// itself: renaming the host, creating the recovery account, locking root, raising
// the setup access point, and joining a network under a rollback checkpoint.
//
// # Why a helper at all
//
// waypointd runs as root today. Every one of the operations above is a reason it
// should not have to: they are rare, they are dangerous, and they are the only
// root-requiring things a dashboard process does. Putting them behind a named,
// argument-validated RPC surface means the blast radius of a bug in the web tier
// is the eleven calls in Provisioner, not "root". The socket is the seam that
// makes dropping waypointd's privileges a later, mechanical change rather than a
// rewrite.
//
// # Shape
//
// This package holds only the contract — the interface, the request/response
// types, their validation, and the wire codec. It has no implementation and no
// dependency on the rest of Waypoint, so both ends and the tests can import it
// without pulling in NetworkManager, the config store, or the daemon. The
// in-memory fake and the conformance suite live in the privtest subpackage, so
// nothing that imports testing ends up in the shipped binary.
//
// # Trust
//
// The socket's filesystem permissions are the authorization boundary: mode 0660,
// owned root:waypoint, in a 0750 directory. Anything that can open it can rename
// the host and create a sudo-capable account, so a server implementation must
// also check SO_PEERCRED and refuse peers outside that group — the mode alone is
// one chmod away from being wrong.
//
// Validation happens on both ends. Callers call Validate to fail fast with a
// useful message; the server calls it again because a request arriving over a
// socket is input, not a promise.
package privhelper

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// DefaultSocketPath is where the helper listens on a real node. /run is tmpfs, so
// the socket cannot survive a reboot as a stale file, and it is not on the SD card.
const DefaultSocketPath = "/run/waypoint/provision.sock"

// SocketGroup is the group that may talk to the helper. The socket is mode 0660
// owned root:waypoint, in a 0750 directory, and the server additionally checks
// SO_PEERCRED — the mode alone is one bad chmod away from being wrong.
const SocketGroup = "waypoint"

// ProtocolVersion is the wire contract's version. It rides on every frame so a
// mismatched waypointd/helper pair — which an A/B rollback (RFC-0017) can produce,
// since the two are updated as separate artifacts — fails with a clear error
// instead of a confusing decode failure deep in a request.
const ProtocolVersion = 1

// MaxFrameSize caps a single frame. It is a DoS bound, not a tuning knob: the
// length prefix is attacker-controlled input, and without a cap checked *before*
// allocation a peer could ask a root process to allocate 4 GiB. The largest real
// payload is an SSH public key plus a comment, three orders of magnitude below
// this.
const MaxFrameSize = 64 << 10

// Method names a call. They are strings rather than integers so a frame captured
// from the socket is readable, and so an unknown method from a newer peer reports
// its own name in the error.
type Method string

// The method set. Every entry has exactly one Provisioner method, one request
// type, and one response type; Methods() below is what keeps a server's switch
// and this list from drifting apart.
const (
	MethodSetHostname           Method = "set_hostname"
	MethodCreateRecoveryUser    Method = "create_recovery_user"
	MethodInstallSSHKey         Method = "install_ssh_key"
	MethodEnableSSH             Method = "enable_ssh"
	MethodLockRoot              Method = "lock_root"
	MethodAPUp                  Method = "ap_up"
	MethodAPDown                Method = "ap_down"
	MethodNetJoin               Method = "net_join"
	MethodNetCheckpointCreate   Method = "net_checkpoint_create"
	MethodNetCheckpointDestroy  Method = "net_checkpoint_destroy"
	MethodNetCheckpointRollback Method = "net_checkpoint_rollback"
	MethodListSudoUsers         Method = "list_sudo_users"
	MethodRemoveUser            Method = "remove_user"
)

// Methods returns every method in the protocol, in a stable order. A server uses
// it to assert its dispatch table is complete; the conformance suite uses it to
// assert an implementation answers to all of them.
func Methods() []Method {
	return []Method{
		MethodSetHostname,
		MethodCreateRecoveryUser,
		MethodInstallSSHKey,
		MethodEnableSSH,
		MethodLockRoot,
		MethodAPUp,
		MethodAPDown,
		MethodNetJoin,
		MethodNetCheckpointCreate,
		MethodNetCheckpointDestroy,
		MethodNetCheckpointRollback,
		MethodListSudoUsers,
		MethodRemoveUser,
	}
}

// Request is one call. ID correlates the response on a connection that may have
// several calls in flight; a server must echo it back unchanged, including on the
// error path, or a client cannot match up a failure.
type Request struct {
	Version int             `json:"v"`
	ID      uint64          `json:"id"`
	Method  Method          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// Response is one reply. Exactly one of Result and Error is set.
type Response struct {
	Version int             `json:"v"`
	ID      uint64          `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *Error          `json:"error,omitempty"`
}

// Code classifies a failure so the caller can react without string-matching. The
// web tier maps these onto HTTP status codes.
type Code string

const (
	// CodeInvalidArgument — the request did not pass validation. Retrying it
	// unchanged will fail again; the operator has to fix the input.
	CodeInvalidArgument Code = "invalid_argument"
	// CodeConflict — the request is well-formed but wrong for the current state
	// (locking root before a recovery user exists, destroying an unknown
	// checkpoint, raising the AP while it is already up).
	CodeConflict Code = "conflict"
	// CodeNotFound — the named user, interface, or checkpoint handle is unknown.
	CodeNotFound Code = "not_found"
	// CodeDenied — the peer is not authorized to call this.
	CodeDenied Code = "denied"
	// CodeUnsupported — this build or this hardware cannot do it (no wireless
	// interface, no NetworkManager, a method from a newer peer).
	CodeUnsupported Code = "unsupported"
	// CodeInternal — the operation was attempted and failed.
	CodeInternal Code = "internal"
	// CodeProtocol — the frame itself was wrong: bad version, unparseable params.
	CodeProtocol Code = "protocol"
)

// Error is a protocol-level error. It crosses the socket as JSON and behaves as a
// Go error on both sides, so a client can errors.As it back out of a call without
// the two ends agreeing on anything but this struct.
type Error struct {
	Code    Code   `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}
	return fmt.Sprintf("privhelper: %s: %s", e.Code, e.Message)
}

// Errorf builds a coded error.
func Errorf(code Code, format string, a ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, a...)}
}

// CodeOf extracts the code from err, or CodeInternal for an error that carries
// none. A nil error reports the empty code, so callers can use it unconditionally.
func CodeOf(err error) Code {
	if err == nil {
		return ""
	}
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return CodeInternal
}

// --- codec ---------------------------------------------------------------

// Frames are a 4-byte big-endian length followed by that many bytes of JSON.
//
// Length-prefixing rather than newline-delimiting because it bounds the read
// before any allocation: the reader knows the size up front, checks it against
// MaxFrameSize, and refuses oversized frames without having consumed them. A
// delimiter-scanning reader has to buffer until it finds the delimiter, which
// means a peer that never sends one decides how much memory a root process uses.

var (
	// ErrFrameTooLarge means a frame exceeded MaxFrameSize. On read it is
	// returned before allocating, and the connection must be closed — the stream
	// is no longer synchronised because the oversized body was not consumed.
	ErrFrameTooLarge = errors.New("privhelper: frame exceeds the maximum size")
	// ErrEmptyFrame means a zero-length frame arrived, which is never valid.
	ErrEmptyFrame = errors.New("privhelper: zero-length frame")
)

const headerSize = 4

// WriteFrame encodes v as JSON and writes one length-prefixed frame to w.
//
// The header and body go out in a single Write. Two writes would let a
// concurrent writer on the same connection interleave between them and corrupt
// the stream — the kind of bug that only shows up under load on the one call
// that matters.
func WriteFrame(w io.Writer, v any) error {
	body, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("privhelper: encode frame: %w", err)
	}
	if len(body) > MaxFrameSize {
		return fmt.Errorf("%w: %d bytes (max %d)", ErrFrameTooLarge, len(body), MaxFrameSize)
	}
	buf := make([]byte, headerSize+len(body))
	binary.BigEndian.PutUint32(buf[:headerSize], uint32(len(body)))
	copy(buf[headerSize:], body)
	_, err = w.Write(buf)
	return err
}

// ReadFrame reads one length-prefixed frame from r and decodes it into v.
//
// A clean close between frames surfaces as io.EOF, which is how a server's accept
// loop tells "the client is done" apart from "the client died mid-frame"
// (io.ErrUnexpectedEOF).
func ReadFrame(r io.Reader, v any) error {
	var hdr [headerSize]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return err // io.EOF between frames is a clean close, not a failure
	}
	n := binary.BigEndian.Uint32(hdr[:])
	if n == 0 {
		return ErrEmptyFrame
	}
	// Checked before the make below — this is the whole point of the length prefix.
	if n > MaxFrameSize {
		return fmt.Errorf("%w: %d bytes (max %d)", ErrFrameTooLarge, n, MaxFrameSize)
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(r, body); err != nil {
		if errors.Is(err, io.EOF) {
			return io.ErrUnexpectedEOF // the peer died mid-frame
		}
		return err
	}
	if err := json.Unmarshal(body, v); err != nil {
		return fmt.Errorf("privhelper: decode frame: %w", err)
	}
	return nil
}

// NewRequest builds a request frame, marshalling params and stamping the version.
func NewRequest(id uint64, method Method, params any) (Request, error) {
	req := Request{Version: ProtocolVersion, ID: id, Method: method}
	if params != nil {
		raw, err := json.Marshal(params)
		if err != nil {
			return Request{}, fmt.Errorf("privhelper: encode %s params: %w", method, err)
		}
		req.Params = raw
	}
	return req, nil
}

// NewResponse builds a success response frame.
func NewResponse(id uint64, result any) (Response, error) {
	resp := Response{Version: ProtocolVersion, ID: id}
	if result != nil {
		raw, err := json.Marshal(result)
		if err != nil {
			return Response{}, fmt.Errorf("privhelper: encode result: %w", err)
		}
		resp.Result = raw
	}
	return resp, nil
}

// NewErrorResponse builds a failure response frame, coding an uncoded error as
// CodeInternal so nothing crosses the wire without a classification.
func NewErrorResponse(id uint64, err error) Response {
	var e *Error
	if !errors.As(err, &e) {
		e = &Error{Code: CodeInternal, Message: err.Error()}
	}
	return Response{Version: ProtocolVersion, ID: id, Error: e}
}

// CheckVersion reports whether a received frame's version is one this build
// speaks. Frames carry the version so a mismatched pair says so plainly.
func CheckVersion(v int) error {
	if v != ProtocolVersion {
		return Errorf(CodeProtocol, "protocol version %d, this build speaks %d", v, ProtocolVersion)
	}
	return nil
}
