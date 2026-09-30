package hostgate

import (
	"context"
	"fmt"
	"net"
	"testing"
	"time"
)

func TestHostOfUncShapes(t *testing.T) {
	cases := map[string]string{
		`\\192.168.1.150\log\202609`: "192.168.1.150",
		`//SANLING3/Log/202609`:      "sanling3",
		`\\host\share\deep\file.log`: "host",
		`\\host`:                     "host",
		`\\  \share`:                 "",
		`/Users/x/data`:              "",
		`data/production/2026-09-01`: "",
		`C:\logs\202609`:             "",
	}
	for path, want := range cases {
		if got := HostOf(path); got != want {
			t.Errorf("HostOf(%q) = %q, want %q", path, got, want)
		}
	}
}

// Reachable 缓存生效：TTL 内同宿主只拨号一次；死宿主同样只拨一次。
func TestReachableCachesPerHost(t *testing.T) {
	Reset()
	defer Reset()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().(*net.TCPAddr).String()
	defer ln.Close()
	SetProbePort(fmt.Sprintf("%d", portOf(ln)))

	ctx := context.Background()
	if !Reachable(ctx, "127.0.0.1") {
		t.Fatal("listening host must be reachable")
	}
	if !Reachable(ctx, "127.0.0.1") {
		t.Fatal("cached entry must stay reachable")
	}
	if dials != 1 {
		t.Fatalf("dials = %d, want 1 (cache must suppress re-probes)", dials)
	}

	// 死宿主：监听器关闭后，TTL 内仍缓存为可达——强制过期再探。
	mu.Lock()
	cache["127.0.0.1"] = health{ok: true, expires: time.Now().Add(-time.Second)}
	mu.Unlock()
	ln.Close()
	// 关闭监听器后端口可能短暂仍能完成三次握手（回环 linger，WSL/Windows
	// 实测都会），先轮询到真正拒绝再断言——否则测到的是内核残留，不是闸。
	if err := waitRefused(addr, 3*time.Second); err != nil {
		t.Fatalf("%s must stop accepting: %v", addr, err)
	}
	if Reachable(ctx, "127.0.0.1") {
		t.Fatal("closed port must be unreachable")
	}
	if Reachable(ctx, "127.0.0.1") {
		t.Fatal("dead cache must suppress re-probe within TTL")
	}
	if dials != 2 {
		t.Fatalf("dials = %d, want 2", dials)
	}
}

func portOf(ln net.Listener) int {
	return ln.Addr().(*net.TCPAddr).Port
}

// waitRefused 阻塞直到 addr 开始拒绝连接（或超时返回最后一次错误）。
func waitRefused(addr string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err != nil {
			return nil
		}
		_ = conn.Close()
		if time.Now().After(deadline) {
			return fmt.Errorf("still accepting after %s", timeout)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
