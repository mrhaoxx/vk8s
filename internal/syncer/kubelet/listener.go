// Package kubelet 实现 apiserver egress 隧道的 HTTP CONNECT 服务端与假 kubelet（设计文档 7.7）。
//
// apiserver 的 egress selector 把 `cluster` 网络上下文的连接以 `CONNECT <ip>:<port>` 发到
// names.KonnectivitySocket。syncer 按端口分流：10250 → 假 kubelet（logs/exec/attach/port-forward），
// 8443 → metrics.k8s.io 服务。两者都是普通的 http.Server，通过 channel 适配的 net.Listener 接收
// 被劫持的连接。
package kubelet

import (
	"context"
	"crypto/tls"
	"net"
	"sync"
)

// chanListener 把外部塞进来的 net.Conn 作为 Accept 结果交给 http.Server.Serve。
type chanListener struct {
	ch        chan net.Conn
	done      chan struct{}
	closeOnce sync.Once
	addr      net.Addr
	tlsConfig *tls.Config
}

func newChanListener(addr net.Addr, tlsConfig *tls.Config) *chanListener {
	return &chanListener{
		ch:        make(chan net.Conn),
		done:      make(chan struct{}),
		addr:      addr,
		tlsConfig: tlsConfig,
	}
}

// Accept 实现 net.Listener。
func (l *chanListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.ch:
		return c, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}

// Close 实现 net.Listener。
func (l *chanListener) Close() error {
	l.closeOnce.Do(func() { close(l.done) })
	return nil
}

// Addr 实现 net.Listener。
func (l *chanListener) Addr() net.Addr { return l.addr }

// push 把连接交给 listener；listener 已关闭或 ctx 结束时返回 false。
func (l *chanListener) push(ctx context.Context, c net.Conn) bool {
	select {
	case l.ch <- c:
		return true
	case <-l.done:
		return false
	case <-ctx.Done():
		return false
	}
}

// socketAddr 隧道内连接的伪地址。
type socketAddr struct{ name string }

func (a socketAddr) Network() string { return "konnectivity" }
func (a socketAddr) String() string  { return a.name }
