package pool

import (
	"context"
	"net"
	"sync"
	"time"
)

// selectionSetupContext bounds setup without imposing that deadline on the
// lifetime of a returned transport. Only the immutable parent deadline is
// advertised: a downstream WithTimeout must not capture a setup-only deadline.
// Successful setup disarms the cancellation timer; parent
// cancellation still propagates until the transport is closed.
type selectionSetupContext struct {
	context.Context
	cancel context.CancelCauseFunc
	mu     sync.Mutex
	timer  *time.Timer
	active bool
}

func newSelectionSetupContext(parent context.Context, timeout time.Duration) (*selectionSetupContext, context.CancelFunc) {
	base, cancel := context.WithCancelCause(parent)
	c := &selectionSetupContext{Context: base, cancel: cancel, active: true}
	c.timer = time.AfterFunc(timeout, func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.active {
			c.active = false
			c.cancel(context.DeadlineExceeded)
		}
	})
	return c, func() { c.mu.Lock(); c.active = false; c.timer.Stop(); c.cancel(context.Canceled); c.mu.Unlock() }
}
func (c *selectionSetupContext) Err() error {
	if context.Cause(c.Context) == context.DeadlineExceeded {
		return context.DeadlineExceeded
	}
	return c.Context.Err()
}
func (c *selectionSetupContext) disarm() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.active = false
	c.timer.Stop()
	return c.Err()
}
func disarmSelectionSetup(ctx context.Context) error {
	if c, ok := ctx.(*selectionSetupContext); ok {
		return c.disarm()
	}
	return ctx.Err()
}

type selectionOwnedConn struct {
	net.Conn
	once   sync.Once
	cancel context.CancelFunc
}

func (c *selectionOwnedConn) Close() error { err := c.Conn.Close(); c.once.Do(c.cancel); return err }

type selectionOwnedPacketConn struct {
	net.PacketConn
	once   sync.Once
	cancel context.CancelFunc
}

func (c *selectionOwnedPacketConn) Close() error {
	err := c.PacketConn.Close()
	c.once.Do(c.cancel)
	return err
}
