package gateway

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"k8s.io/klog/v2"
)

// ProxyConfig TCP 代理参数。
type ProxyConfig struct {
	Listen           string
	HandshakeTimeout time.Duration
	DialTimeout      time.Duration
	IdleTimeout      time.Duration
	ShutdownGrace    time.Duration
}

// Proxy 接受 TCP 连接、按 SNI 选后端并双向透传。
type Proxy struct {
	cfg     ProxyConfig
	router  *Router
	metrics *Metrics
	dialer  *net.Dialer

	mu      sync.Mutex
	conns   map[net.Conn]struct{}
	closing bool
	wg      sync.WaitGroup
}

// NewProxy 创建代理。
func NewProxy(cfg ProxyConfig, router *Router, metrics *Metrics) *Proxy {
	return &Proxy{
		cfg:     cfg,
		router:  router,
		metrics: metrics,
		dialer: &net.Dialer{
			Timeout:   cfg.DialTimeout,
			KeepAlive: 30 * time.Second,
		},
		conns: map[net.Conn]struct{}{},
	}
}

// Run 监听并服务，直到 ctx 取消；之后停止接受新连接，等待 ShutdownGrace 后强制关闭余下连接。
func (p *Proxy) Run(ctx context.Context) error {
	lc := net.ListenConfig{KeepAlive: 30 * time.Second}
	ln, err := lc.Listen(ctx, "tcp", p.cfg.Listen)
	if err != nil {
		return err
	}
	klog.InfoS("proxy listening", "addr", ln.Addr().String())

	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()

	var tempDelay time.Duration
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				// 瞬时错误（文件描述符耗尽等）：退避重试，与 net/http 做法一致
				if tempDelay == 0 {
					tempDelay = 5 * time.Millisecond
				} else {
					tempDelay *= 2
				}
				if tempDelay > time.Second {
					tempDelay = time.Second
				}
				klog.ErrorS(err, "accept failed; retrying", "delay", tempDelay)
				time.Sleep(tempDelay)
				continue
			}
			return err
		}
		tempDelay = 0
		if !p.track(conn) {
			p.metrics.Reject(RejectShuttingDown)
			_ = conn.Close()
			continue
		}
		p.wg.Add(1)
		go func() {
			defer p.wg.Done()
			defer p.untrack(conn)
			p.handle(ctx, conn)
		}()
	}

	return p.shutdown()
}

// shutdown 等待现有连接自然结束，超过 ShutdownGrace 后强制关闭。
func (p *Proxy) shutdown() error {
	p.mu.Lock()
	p.closing = true
	n := len(p.conns)
	p.mu.Unlock()
	klog.InfoS("proxy draining", "activeConnections", n, "grace", p.cfg.ShutdownGrace)

	done := make(chan struct{})
	go func() {
		p.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(p.cfg.ShutdownGrace):
		p.mu.Lock()
		for c := range p.conns {
			_ = c.Close()
		}
		remaining := len(p.conns)
		p.mu.Unlock()
		klog.InfoS("proxy grace period elapsed; closed remaining connections", "closed", remaining)
		<-done
	}
	klog.InfoS("proxy stopped")
	return nil
}

func (p *Proxy) track(c net.Conn) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closing {
		return false
	}
	p.conns[c] = struct{}{}
	return true
}

func (p *Proxy) untrack(c net.Conn) {
	p.mu.Lock()
	delete(p.conns, c)
	p.mu.Unlock()
}

// handle 处理一条客户端连接：读 ClientHello → 选路由 → 拨后端 → 透传。
func (p *Proxy) handle(ctx context.Context, client net.Conn) {
	defer client.Close()
	remote := client.RemoteAddr().String()

	// 1. 读 ClientHello（带握手超时）
	_ = client.SetReadDeadline(time.Now().Add(p.cfg.HandshakeTimeout))
	raw, serverName, err := ReadClientHello(client)
	if err != nil {
		reason := RejectBadRequest
		var ne net.Error
		switch {
		case errors.Is(err, ErrNoSNI):
			reason = RejectNoSNI
		case errors.Is(err, os.ErrDeadlineExceeded), errors.As(err, &ne) && ne.Timeout():
			reason = RejectHandshakeTimeo
		}
		p.metrics.Reject(reason)
		klog.V(2).InfoS("rejected connection", "remote", remote, "reason", reason, "err", err.Error())
		return
	}
	_ = client.SetReadDeadline(time.Time{})

	// 2. 路由
	backendAddr, reason, ok := p.router.Resolve(serverName)
	if !ok {
		p.metrics.Reject(reason)
		klog.V(2).InfoS("rejected connection", "remote", remote, "sni", serverName, "reason", reason)
		return
	}
	vc := serverName[:len(serverName)-len(p.router.suffix)]

	// 3. 拨后端
	dialCtx, cancel := context.WithTimeout(ctx, p.cfg.DialTimeout)
	backend, err := p.dialer.DialContext(dialCtx, "tcp", backendAddr)
	cancel()
	if err != nil {
		p.metrics.Reject(RejectDialFailed)
		klog.ErrorS(err, "dial backend failed", "remote", remote, "vcluster", vc, "backend", backendAddr)
		return
	}
	defer backend.Close()

	// 4. 透传
	p.metrics.ConnOpened(vc)
	defer p.metrics.ConnClosed(vc)
	klog.V(3).InfoS("proxying", "remote", remote, "vcluster", vc, "backend", backendAddr)

	in, out, perr := pipe(ctx, client, backend, raw, p.cfg.IdleTimeout, func(c2b, b2c int64) {
		p.metrics.AddBytes(vc, c2b, b2c)
	})
	if perr != nil {
		klog.V(2).InfoS("connection closed with error", "remote", remote, "vcluster", vc,
			"err", perr.Error(), "bytesIn", in, "bytesOut", out)
		return
	}
	klog.V(3).InfoS("connection closed", "remote", remote, "vcluster", vc, "bytesIn", in, "bytesOut", out)
}

// ---- 双向转发 ----

// errIdleTimeout 连接因空闲超时被关闭。
var errIdleTimeout = errors.New("idle timeout")

// copyBufPool 复用 32KiB 转发缓冲，避免每条连接两次分配。
var copyBufPool = sync.Pool{New: func() interface{} { b := make([]byte, 32<<10); return &b }}

// halfCloser *net.TCPConn 等支持半关闭的连接。
type halfCloser interface {
	CloseWrite() error
}

// activity 记录最近一次有数据流动的时间（UnixNano），供空闲看门狗使用。
type activity struct{ last atomic.Int64 }

func (a *activity) touch() { a.last.Store(time.Now().UnixNano()) }
func (a *activity) idleFor() time.Duration {
	return time.Since(time.Unix(0, a.last.Load()))
}

// countingReader 每次成功 Read 后刷新活动时间并累加字节数；每 1MiB 回调一次，减少指标锁竞争。
type countingReader struct {
	r     io.Reader
	act   *activity
	flush func(delta int64)
	acc   int64
}

func (c *countingReader) Read(b []byte) (int, error) {
	n, err := c.r.Read(b)
	if n > 0 {
		c.act.touch()
		c.acc += int64(n)
		if c.acc >= 1<<20 {
			c.flush(c.acc)
			c.acc = 0
		}
	}
	return n, err
}

func (c *countingReader) done() {
	if c.acc > 0 {
		c.flush(c.acc)
		c.acc = 0
	}
}

const (
	dirClientToBackend = "c2b"
	dirBackendToClient = "b2c"
)

type copyResult struct {
	dir string
	n   int64
	err error
}

// copyDirection 单向拷贝；src 返回 EOF 时对 dst 半关闭，让对方把剩余数据发完后自行关闭。
func copyDirection(dir string, dst, src net.Conn, act *activity, flush func(int64), results chan<- copyResult) {
	cr := &countingReader{r: src, act: act, flush: flush}
	bufp := copyBufPool.Get().(*[]byte)
	n, err := io.CopyBuffer(dst, cr, *bufp)
	copyBufPool.Put(bufp)
	cr.done()
	if err == nil {
		if hc, ok := dst.(halfCloser); ok {
			_ = hc.CloseWrite()
		} else {
			_ = dst.Close()
		}
	}
	results <- copyResult{dir: dir, n: n, err: err}
}

// pipe 把 preface 写给 backend 后双向转发，直到两个方向都结束、出错、空闲超时或 ctx 取消。
// onBytes 周期性回调累计字节（client→backend, backend→client）；返回值为两个方向的总字节数。
func pipe(ctx context.Context, client, backend net.Conn, preface []byte, idle time.Duration,
	onBytes func(c2b, b2c int64)) (c2b, b2c int64, err error) {
	act := &activity{}
	act.touch()

	if len(preface) > 0 {
		if _, werr := backend.Write(preface); werr != nil {
			return 0, 0, werr
		}
		c2b += int64(len(preface))
		onBytes(int64(len(preface)), 0)
	}

	stop := make(chan struct{})
	defer close(stop)

	// ctx 取消 → 立即关闭两端（Proxy.Run 的 ShutdownGrace 已在外层给过宽限）
	go func() {
		select {
		case <-ctx.Done():
			_ = client.Close()
			_ = backend.Close()
		case <-stop:
		}
	}()

	// 空闲看门狗：两个方向都无数据超过 idle 则关闭两端
	var idleHit atomic.Bool
	if idle > 0 {
		go func() {
			check := idle / 4
			if check < time.Second {
				check = time.Second
			}
			t := time.NewTicker(check)
			defer t.Stop()
			for {
				select {
				case <-stop:
					return
				case <-t.C:
					if act.idleFor() >= idle {
						idleHit.Store(true)
						_ = client.Close()
						_ = backend.Close()
						return
					}
				}
			}
		}()
	}

	results := make(chan copyResult, 2)
	go copyDirection(dirClientToBackend, backend, client, act, func(d int64) { onBytes(d, 0) }, results)
	go copyDirection(dirBackendToClient, client, backend, act, func(d int64) { onBytes(0, d) }, results)

	var firstErr error
	for i := 0; i < 2; i++ {
		r := <-results
		if r.dir == dirClientToBackend {
			c2b += r.n
		} else {
			b2c += r.n
		}
		if r.err != nil && firstErr == nil && !isBenignCloseErr(r.err) {
			firstErr = r.err
			// 真实错误：关闭两端让另一方向尽快返回
			_ = client.Close()
			_ = backend.Close()
		}
	}

	if idleHit.Load() {
		return c2b, b2c, errIdleTimeout
	}
	if ctx.Err() != nil {
		return c2b, b2c, nil
	}
	return c2b, b2c, firstErr
}

// isBenignCloseErr 连接正常收尾时会出现的错误：我们自己关闭的连接、对端重置、写入已关闭的管道。
func isBenignCloseErr(err error) bool {
	return errors.Is(err, net.ErrClosed) ||
		errors.Is(err, io.EOF) ||
		errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, syscall.EPIPE)
}
