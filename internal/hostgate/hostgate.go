// Package hostgate 是多 UNC 规模的宿主机健康闸。
//
// 几十台机台 = 几十个独立 SMB 宿主。Windows SMB 对不响应的 IP 的访问
// 要等 ~21s TCP 重试超时——同宿主的每个格式源各撞一遍：一台关机的机台
// （22 个格式源）让并发槽被死等占用、整轮 pass 拖长数分钟；整线故障则
// 每轮产生上万条逐日期失败。这里用 2s 的 445 端口探测代替，结果按宿主
// 缓存（TTL 内同宿主其余源立即返回）——每轮每宿主至多一次探测。
//
// 只对 UNC 形状的路径生效（HostOf 返回空 = 直接放行），本地路径零影响。
package hostgate

import (
	"context"
	"net"
	"strings"
	"sync"
	"time"
)

const (
	probeTimeout = 2 * time.Second
	deadTTL      = 10 * time.Minute // 死判定的缓存期：一轮 pass 内有效，下轮重探
	aliveTTL     = 10 * time.Minute
)

var (
	mu    sync.Mutex
	cache = map[string]health{}
	port  = "445"
	dials int
)

type health struct {
	ok      bool
	expires time.Time
}

// HostOf 从路径提取 UNC 宿主机名（大小写折叠）。非 UNC 形状返回空。
func HostOf(path string) string {
	p := strings.ReplaceAll(path, "/", "\\")
	if !strings.HasPrefix(p, `\\`) {
		return ""
	}
	rest := p[2:]
	host := rest
	if end := strings.Index(rest, `\`); end >= 0 {
		host = rest[:end]
	}
	host = strings.TrimSpace(host)
	if host == "" {
		return ""
	}
	return strings.ToLower(host)
}

// Reachable 返回宿主当前是否可达（带 TTL 缓存；每 TTL 至多一次探测）。
// 空宿主（非 UNC 路径）恒为可达。
func Reachable(ctx context.Context, host string) bool {
	if host == "" {
		return true
	}
	mu.Lock()
	c, ok := cache[host]
	mu.Unlock()
	if ok && time.Now().Before(c.expires) {
		return c.ok
	}
	reachable := probe(ctx, host)
	mu.Lock()
	dials++
	exp := aliveTTL
	if !reachable {
		exp = deadTTL
	}
	cache[host] = health{ok: reachable, expires: time.Now().Add(exp)}
	mu.Unlock()
	return reachable
}

func probe(ctx context.Context, host string) bool {
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, port), probeTimeout)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// 测试钩子：探测端口与拨号计数（校验缓存确实挡住了重复探测）。
func SetProbePort(p string) { mu.Lock(); port = p; mu.Unlock() }
func DialCount() int        { mu.Lock(); defer mu.Unlock(); return dials }
func Reset() {
	mu.Lock()
	cache = map[string]health{}
	dials = 0
	mu.Unlock()
}
