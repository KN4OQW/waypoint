package privhelper

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sync/atomic"
	"time"
)

// DefaultCallTimeout bounds a call that arrives with no deadline of its own.
// NetJoin waits for an association and APUp restarts a radio, so it is generous;
// it exists to stop a wedged helper from parking a dashboard request forever.
const DefaultCallTimeout = 2 * time.Minute

// Client is a Provisioner backed by the helper's socket.
//
// It dials one connection per call rather than holding one open. That trades a
// few hundred microseconds for the absence of a whole category of bug: no
// reconnect state machine, no half-open connection surviving a helper restart, no
// response demultiplexing. This socket carries a handful of calls during setup and
// a few more when the operator changes the network — nothing where the connection
// cost is visible.
type Client struct {
	// Path is the socket. Empty means DefaultSocketPath.
	Path string
	// Timeout bounds a call whose context has no deadline. Zero means
	// DefaultCallTimeout.
	Timeout time.Duration

	nextID atomic.Uint64
}

// NewClient returns a client for the helper at path. An empty path means
// DefaultSocketPath.
func NewClient(path string) *Client {
	return &Client{Path: path}
}

var _ Provisioner = (*Client)(nil)

func (c *Client) path() string {
	if c.Path == "" {
		return DefaultSocketPath
	}
	return c.Path
}

// call performs one request/response exchange and decodes the result.
func (c *Client) call(ctx context.Context, method Method, params any, result any) error {
	if _, ok := ctx.Deadline(); !ok {
		timeout := c.Timeout
		if timeout <= 0 {
			timeout = DefaultCallTimeout
		}
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", c.path())
	if err != nil {
		// The likeliest causes are "the helper is not running" and "this process
		// is not in the waypoint group", and the operator needs to be able to tell
		// them apart from a bug in the call.
		return Errorf(CodeUnsupported, "cannot reach the provisioning helper at %s: %v", c.path(), err)
	}
	defer conn.Close() //nolint:errcheck // one connection per call; it is finished with either way

	if dl, ok := ctx.Deadline(); ok {
		if err := conn.SetDeadline(dl); err != nil {
			return err
		}
	}
	// Closing the connection on cancellation is what makes an in-flight call
	// abortable: a blocked read on a Unix socket does not otherwise notice a
	// cancelled context.
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()

	id := c.nextID.Add(1)
	req, err := NewRequest(id, method, params)
	if err != nil {
		return err
	}
	if err := WriteFrame(conn, req); err != nil {
		return c.wrap(ctx, method, err)
	}

	var resp Response
	if err := ReadFrame(bufio.NewReader(conn), &resp); err != nil {
		return c.wrap(ctx, method, err)
	}
	if err := CheckVersion(resp.Version); err != nil {
		return err
	}
	// id 0 is the server's pre-authentication rejection, which has no request to
	// correlate with.
	if resp.ID != id && resp.ID != 0 {
		return Errorf(CodeProtocol, "response id %d does not match request id %d", resp.ID, id)
	}
	if resp.Error != nil {
		return resp.Error
	}
	if result != nil && len(resp.Result) > 0 {
		if err := json.Unmarshal(resp.Result, result); err != nil {
			return Errorf(CodeProtocol, "decode %s result: %v", method, err)
		}
	}
	return nil
}

// wrap turns a transport failure into a coded error, distinguishing a cancelled
// call from a helper that died mid-request.
func (c *Client) wrap(ctx context.Context, method Method, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return Errorf(CodeInternal, "%s: %v", method, ctxErr)
	}
	if errors.Is(err, net.ErrClosed) {
		return Errorf(CodeInternal, "%s: connection closed", method)
	}
	return fmt.Errorf("privhelper: %s: %w", method, err)
}

// --- Provisioner ---------------------------------------------------------

func (c *Client) SetHostname(ctx context.Context, req SetHostnameRequest) (SetHostnameResponse, error) {
	var out SetHostnameResponse
	if err := req.Validate(); err != nil {
		return out, err
	}
	err := c.call(ctx, MethodSetHostname, req, &out)
	return out, err
}

func (c *Client) CreateRecoveryUser(ctx context.Context, req CreateRecoveryUserRequest) (CreateRecoveryUserResponse, error) {
	var out CreateRecoveryUserResponse
	if err := req.Validate(); err != nil {
		return out, err
	}
	err := c.call(ctx, MethodCreateRecoveryUser, req, &out)
	return out, err
}

func (c *Client) InstallSSHKey(ctx context.Context, req InstallSSHKeyRequest) (InstallSSHKeyResponse, error) {
	var out InstallSSHKeyResponse
	if err := req.Validate(); err != nil {
		return out, err
	}
	err := c.call(ctx, MethodInstallSSHKey, req, &out)
	return out, err
}

func (c *Client) EnableSSH(ctx context.Context, req EnableSSHRequest) (EnableSSHResponse, error) {
	var out EnableSSHResponse
	if err := req.Validate(); err != nil {
		return out, err
	}
	err := c.call(ctx, MethodEnableSSH, req, &out)
	return out, err
}

func (c *Client) LockRoot(ctx context.Context, req LockRootRequest) (LockRootResponse, error) {
	var out LockRootResponse
	if err := req.Validate(); err != nil {
		return out, err
	}
	err := c.call(ctx, MethodLockRoot, req, &out)
	return out, err
}

func (c *Client) APUp(ctx context.Context, req APUpRequest) (APUpResponse, error) {
	var out APUpResponse
	if err := req.Validate(); err != nil {
		return out, err
	}
	err := c.call(ctx, MethodAPUp, req, &out)
	return out, err
}

func (c *Client) APDown(ctx context.Context, req APDownRequest) (APDownResponse, error) {
	var out APDownResponse
	if err := req.Validate(); err != nil {
		return out, err
	}
	err := c.call(ctx, MethodAPDown, req, &out)
	return out, err
}

func (c *Client) NetScan(ctx context.Context, req NetScanRequest) (NetScanResponse, error) {
	var out NetScanResponse
	if err := req.Validate(); err != nil {
		return out, err
	}
	err := c.call(ctx, MethodNetScan, req, &out)
	return out, err
}

func (c *Client) NetJoin(ctx context.Context, req NetJoinRequest) (NetJoinResponse, error) {
	var out NetJoinResponse
	if err := req.Validate(); err != nil {
		return out, err
	}
	err := c.call(ctx, MethodNetJoin, req, &out)
	return out, err
}

func (c *Client) NetCheckpointCreate(ctx context.Context, req NetCheckpointCreateRequest) (NetCheckpointCreateResponse, error) {
	var out NetCheckpointCreateResponse
	if err := req.Validate(); err != nil {
		return out, err
	}
	err := c.call(ctx, MethodNetCheckpointCreate, req, &out)
	return out, err
}

func (c *Client) NetCheckpointDestroy(ctx context.Context, req NetCheckpointDestroyRequest) (NetCheckpointDestroyResponse, error) {
	var out NetCheckpointDestroyResponse
	if err := req.Validate(); err != nil {
		return out, err
	}
	err := c.call(ctx, MethodNetCheckpointDestroy, req, &out)
	return out, err
}

func (c *Client) NetCheckpointRollback(ctx context.Context, req NetCheckpointRollbackRequest) (NetCheckpointRollbackResponse, error) {
	var out NetCheckpointRollbackResponse
	if err := req.Validate(); err != nil {
		return out, err
	}
	err := c.call(ctx, MethodNetCheckpointRollback, req, &out)
	return out, err
}

func (c *Client) ListSudoUsers(ctx context.Context, req ListSudoUsersRequest) (ListSudoUsersResponse, error) {
	var out ListSudoUsersResponse
	if err := req.Validate(); err != nil {
		return out, err
	}
	err := c.call(ctx, MethodListSudoUsers, req, &out)
	return out, err
}

func (c *Client) RemoveUser(ctx context.Context, req RemoveUserRequest) (RemoveUserResponse, error) {
	var out RemoveUserResponse
	if err := req.Validate(); err != nil {
		return out, err
	}
	err := c.call(ctx, MethodRemoveUser, req, &out)
	return out, err
}
