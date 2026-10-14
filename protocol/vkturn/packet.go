package vkturn

import (
	"net"
	"os"
	"sync"
	"time"
)

type packetTransport interface {
	SendPacketUntil(<-chan struct{}, []byte) error
	ReceivePacketUntil(<-chan struct{}, []byte) (int, error)
}

type receivedPacket struct {
	data []byte
	err  error
}

type packetConn struct {
	transport       packetTransport
	done            chan struct{}
	closeOnce       sync.Once
	incoming        chan receivedPacket
	mu              sync.Mutex
	readDeadline    time.Time
	writeDeadline   time.Time
	deadlineChanged chan struct{}
}

func newPacketConn(p packetTransport) *packetConn {
	c := &packetConn{transport: p, done: make(chan struct{}), incoming: make(chan receivedPacket, 1), deadlineChanged: make(chan struct{})}
	go c.receive()
	return c
}

func (c *packetConn) receive() {
	b := make([]byte, 65535)
	for {
		n, err := c.transport.ReceivePacketUntil(c.done, b)
		item := receivedPacket{err: err}
		if n > 0 {
			item.data = append([]byte(nil), b[:n]...)
		}
		select {
		case c.incoming <- item:
		case <-c.done:
			return
		}
		if err != nil {
			return
		}
	}
}

func (c *packetConn) Read(b []byte) (int, error) { n, _, err := c.ReadFrom(b); return n, err }
func (c *packetConn) ReadFrom(b []byte) (int, net.Addr, error) {
	for {
		c.mu.Lock()
		deadline, changed := c.readDeadline, c.deadlineChanged
		c.mu.Unlock()
		var timer *time.Timer
		var timeout <-chan time.Time
		if !deadline.IsZero() {
			if !time.Now().Before(deadline) {
				return 0, nil, os.ErrDeadlineExceeded
			}
			timer = time.NewTimer(time.Until(deadline))
			timeout = timer.C
		}
		select {
		case <-c.done:
			if timer != nil {
				timer.Stop()
			}
			return 0, nil, net.ErrClosed
		case <-changed:
			if timer != nil {
				timer.Stop()
			}
			continue
		case <-timeout:
			return 0, nil, os.ErrDeadlineExceeded
		case item := <-c.incoming:
			if timer != nil {
				timer.Stop()
			}
			return copy(b, item.data), c.RemoteAddr(), item.err
		}
	}
}
func (c *packetConn) Write(b []byte) (int, error) {
	select {
	case <-c.done:
		return 0, net.ErrClosed
	default:
	}
	c.mu.Lock()
	deadline := c.writeDeadline
	c.mu.Unlock()
	if !deadline.IsZero() && !time.Now().Before(deadline) {
		return 0, os.ErrDeadlineExceeded
	}
	// Native WireGuard does not set transport write deadlines. Closing this
	// connection also unblocks a send waiting for room in the TURN queue.
	if err := c.transport.SendPacketUntil(c.done, b); err != nil {
		return 0, err
	}
	return len(b), nil
}
func (c *packetConn) WriteTo(b []byte, _ net.Addr) (int, error) { return c.Write(b) }
func (c *packetConn) Close() error                              { c.closeOnce.Do(func() { close(c.done) }); return nil }
func (c *packetConn) LocalAddr() net.Addr                       { return &net.UDPAddr{IP: net.IPv4zero} }
func (c *packetConn) RemoteAddr() net.Addr {
	return &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 51820}
}
func (c *packetConn) SetDeadline(t time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.readDeadline = t
	c.writeDeadline = t
	close(c.deadlineChanged)
	c.deadlineChanged = make(chan struct{})
	return nil
}
func (c *packetConn) SetReadDeadline(t time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.readDeadline = t
	close(c.deadlineChanged)
	c.deadlineChanged = make(chan struct{})
	return nil
}
func (c *packetConn) SetWriteDeadline(t time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.writeDeadline = t
	return nil
}
