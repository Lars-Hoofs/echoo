package netguard

import (
	"errors"
	"net"
	"net/netip"
	"testing"
	"time"
)

func TestIsInternal(t *testing.T) {
	cases := []struct {
		addr string
		want bool
	}{
		{"127.0.0.1", true},
		{"10.1.2.3", true},
		{"172.16.0.1", true},
		{"192.168.1.1", true},
		{"169.254.169.254", true},
		{"100.64.0.1", true},
		{"100.127.255.254", true},
		{"0.0.0.0", true},
		{"0.1.2.3", true},
		{"224.0.0.1", true},
		{"192.0.2.10", true},
		{"198.51.100.10", true},
		{"203.0.113.10", true},
		{"::1", true},
		{"::", true},
		{"fe80::1", true},
		{"fc00::1", true},
		{"fd12:3456::1", true},
		{"ff02::1", true},
		{"2001:db8::1", true},
		{"::ffff:127.0.0.1", true},
		{"::ffff:10.0.0.1", true},
		{"::ffff:169.254.169.254", true},
		{"::ffff:100.64.0.1", true},
		{"64:ff9b::808:808", true},
		{"64:ff9b:1::1", true},
		{"2002:7f00:1::1", true},
		{"2002:0a00:1::", true},
		{"2002:0808:0808::1", false},
		{"::7f00:1", true},    // IPv4-compatible 127.0.0.1
		{"::a00:1", true},     // IPv4-compatible 10.0.0.1
		{"::a9fe:a9fe", true}, // IPv4-compatible 169.254.169.254
		{"::808:808", false},  // IPv4-compatible 8.8.8.8
		{"fec0::1", true},
		{"feff::1", true},
		{"fe00::1", false},
		{"2001:0:4136:e378:8000:63bf:f7f7:f7f7", false},
		{"2001:0:7f00:1:8000:63bf:80ff:fffe", true},
		{"2001:0:4136:e378:8000:63bf:f5ff:fffe", true},
		{"198.18.0.1", true},
		{"198.19.255.255", true},
		{"198.20.0.1", false},
		{"240.0.0.1", true},
		{"255.255.255.255", true},
		{"192.0.0.1", true},
		{"192.0.1.1", false},
		{"8.8.8.8", false},
		{"1.1.1.1", false},
		{"100.63.255.255", false},
		{"100.128.0.1", false},
		{"172.32.0.1", false},
		{"2606:4700:4700::1111", false},
		{"::ffff:8.8.8.8", false},
	}
	for _, tc := range cases {
		if got := IsInternal(netip.MustParseAddr(tc.addr)); got != tc.want {
			t.Errorf("IsInternal(%s) = %v, want %v", tc.addr, got, tc.want)
		}
	}
}

func TestDialerControl(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	accepted := make(chan struct{}, 1)
	go func() {
		if c, err := ln.Accept(); err == nil {
			accepted <- struct{}{}
			_ = c.Close()
		}
	}()

	t.Run("blocks loopback", func(t *testing.T) {
		_, err := Dialer(false, time.Second).DialContext(t.Context(), "tcp", ln.Addr().String())
		if !errors.Is(err, ErrInternal) {
			t.Fatalf("err = %v, want ErrInternal", err)
		}
		select {
		case <-accepted:
			t.Fatal("blocked dial reached the listener")
		case <-time.After(100 * time.Millisecond):
		}
	})

	t.Run("blocks a name that resolves to loopback", func(t *testing.T) {
		_, port, _ := net.SplitHostPort(ln.Addr().String())
		_, err := Dialer(false, time.Second).DialContext(t.Context(), "tcp", net.JoinHostPort("localhost", port))
		if !errors.Is(err, ErrInternal) {
			t.Fatalf("err = %v, want ErrInternal", err)
		}
	})

	t.Run("allows loopback when allowed", func(t *testing.T) {
		c, err := Dialer(true, time.Second).DialContext(t.Context(), "tcp", ln.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		_ = c.Close()
		select {
		case <-accepted:
		case <-time.After(time.Second):
			t.Fatal("listener saw no connection")
		}
	})
}
