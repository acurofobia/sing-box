package vkturn

import (
	"errors"
	"net"
	"os"
	"testing"
	"time"
)

type testPackets struct{ packets chan []byte }

func (p *testPackets) SendPacketUntil(done <-chan struct{}, b []byte) error {
	select {
	case p.packets <- append([]byte(nil), b...):
		return nil
	case <-done:
		return net.ErrClosed
	}
}
func (p *testPackets) ReceivePacketUntil(done <-chan struct{}, b []byte) (int, error) {
	select {
	case v := <-p.packets:
		return copy(b, v), nil
	case <-done:
		return 0, net.ErrClosed
	}
}

func TestPacketRoundTripAndClose(t *testing.T) {
	p := &testPackets{make(chan []byte, 1)}
	c := newPacketConn(p)
	if _, err := c.Write([]byte("wireguard-packet")); err != nil {
		t.Fatal(err)
	}
	b := make([]byte, 128)
	n, addr, err := c.ReadFrom(b)
	if err != nil || string(b[:n]) != "wireguard-packet" || addr.String() != "127.0.0.1:51820" {
		t.Fatalf("n=%d addr=%v err=%v", n, addr, err)
	}
	done := make(chan error, 1)
	go func() { _, err := c.Read(b); done <- err }()
	c.Close()
	select {
	case err := <-done:
		if !errors.Is(err, net.ErrClosed) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Close did not unblock read")
	}
}

func TestReadDeadlineAndRecovery(t *testing.T) {
	p := &testPackets{make(chan []byte, 1)}
	c := newPacketConn(p)
	defer c.Close()
	c.SetReadDeadline(time.Now().Add(10 * time.Millisecond))
	if _, err := c.Read(make([]byte, 64)); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("expected deadline: %v", err)
	}
	c.SetReadDeadline(time.Time{})
	c.Write([]byte("after-timeout"))
	if _, err := c.Read(make([]byte, 64)); err != nil {
		t.Fatal(err)
	}
}
