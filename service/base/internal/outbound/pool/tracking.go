package pool

import (
	"net"
	"sync"
	"sync/atomic"
)

type trackedConn struct {
	net.Conn
	once                 sync.Once
	successOnce          sync.Once
	failureOnce          sync.Once
	readMu               sync.Mutex
	closing              atomic.Bool
	wrote                atomic.Bool
	confirmed            atomic.Bool
	release              func()
	onTraffic            func(upload, download int64)
	onConfirmedSuccess   func()
	onUnconfirmedFailure func(error)
}

func (c *trackedConn) Read(b []byte) (int, error) {
	c.readMu.Lock()
	defer c.readMu.Unlock()

	n, err := c.Conn.Read(b)
	if n > 0 && c.onTraffic != nil {
		c.onTraffic(0, int64(n))
	}
	if n > 0 {
		c.confirmed.Store(true)
		if c.onConfirmedSuccess != nil {
			c.successOnce.Do(c.onConfirmedSuccess)
		}
	} else if err != nil && c.wrote.Load() {
		c.reportUnconfirmedFailure(err)
	}
	return n, err
}

func (c *trackedConn) Write(b []byte) (int, error) {
	n, err := c.Conn.Write(b)
	if n > 0 {
		c.wrote.Store(true)
		if c.onTraffic != nil {
			c.onTraffic(int64(n), 0)
		}
	}
	return n, err
}

func (c *trackedConn) Close() error {
	c.closing.Store(true)
	err := c.Conn.Close()
	c.once.Do(c.release)
	return err
}

func (c *trackedConn) reportUnconfirmedFailure(err error) {
	if err == nil || c.onUnconfirmedFailure == nil || c.closing.Load() || c.confirmed.Load() {
		return
	}
	c.failureOnce.Do(func() {
		if !c.closing.Load() && !c.confirmed.Load() {
			c.onUnconfirmedFailure(err)
		}
	})
}

type trackedPacketConn struct {
	net.PacketConn
	once               sync.Once
	successOnce        sync.Once
	release            func()
	onTraffic          func(upload, download int64)
	onConfirmedSuccess func()
}

func (c *trackedPacketConn) ReadFrom(b []byte) (int, net.Addr, error) {
	n, addr, err := c.PacketConn.ReadFrom(b)
	if n > 0 && c.onTraffic != nil {
		c.onTraffic(0, int64(n))
	}
	if n > 0 && c.onConfirmedSuccess != nil {
		c.successOnce.Do(c.onConfirmedSuccess)
	}
	return n, addr, err
}

func (c *trackedPacketConn) WriteTo(b []byte, addr net.Addr) (int, error) {
	n, err := c.PacketConn.WriteTo(b, addr)
	if n > 0 && c.onTraffic != nil {
		c.onTraffic(int64(n), 0)
	}
	return n, err
}

func (c *trackedPacketConn) Close() error {
	err := c.PacketConn.Close()
	c.once.Do(c.release)
	return err
}

func (p *poolOutbound) incActive(member *memberState) {
	if member.shared != nil {
		member.shared.incActive()
	}
}

func (p *poolOutbound) decActive(member *memberState) {
	if member.shared != nil {
		member.shared.decActive()
	}
}

func (p *poolOutbound) getCandidateBuffer() []*memberState {
	if buf := p.candidatesPool.Get(); buf != nil {
		return buf.([]*memberState)
	}
	return make([]*memberState, 0, len(p.options.Members))
}

func (p *poolOutbound) putCandidateBuffer(buf []*memberState) {
	if buf == nil {
		return
	}
	const maxCached = 4096
	if cap(buf) > maxCached {
		return
	}
	p.candidatesPool.Put(buf[:0])
}
