package kubelet

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/go-logr/logr"

	"vk8s.io/vk8s/internal/names"
)

const (
	// connectReadTimeout 读取 CONNECT 请求行的超时。
	connectReadTimeout = 10 * time.Second
	// dialTimeout 直连转发时连接目标的超时。
	dialTimeout = 10 * time.Second
)

// ConnectServer 在 UDS 上实现 HTTP CONNECT 服务端并按目标分流：
// 10250 → 假 kubelet；<MetricsAddr>:8443 → metrics.k8s.io；其余目标直接从 syncer 所在网络转发
// （service 形式的 admission / conversion webhook、用户的聚合 APIService、pods/services proxy，两侧 IP 相同无需翻译）。
type ConnectServer struct {
	Log        logr.Logger
	SocketPath string
	// KubeletHandler 假 kubelet 的 http.Handler；KubeletTLS 其服务端 TLS 配置（fake-kubelet 证书，要求客户端证书）。
	KubeletHandler http.Handler
	KubeletTLS     *tls.Config
	// MetricsHandler metrics.k8s.io 的 http.Handler；MetricsTLS 其 TLS 配置（要求 front-proxy-client 客户端证书）。
	MetricsHandler http.Handler
	MetricsTLS     *tls.Config
	// MetricsAddr metrics.k8s.io 在虚拟集群 EndpointSlice 里登记的地址（syncer 自身 pod IP）。
	// 为空时 8443 端口不看地址一律接给 MetricsHandler（本机探针用）。
	MetricsAddr string
}

// Start 监听 UDS 并服务直到 ctx 结束。实现 manager.Runnable。
func (s *ConnectServer) Start(ctx context.Context) error {
	if err := os.Remove(s.SocketPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove stale socket %s: %w", s.SocketPath, err)
	}
	ln, err := net.Listen("unix", s.SocketPath)
	if err != nil {
		return fmt.Errorf("listen %s: %w", s.SocketPath, err)
	}
	// 同 pod 内 apiserver 容器以同一 UID 运行，默认权限即可连接
	_ = os.Chmod(s.SocketPath, 0o660)

	kubeletLn := newChanListener(socketAddr{"fake-kubelet:" + strconv.Itoa(names.FakeKubeletPort)}, s.KubeletTLS)
	metricsLn := newChanListener(socketAddr{"metrics:" + strconv.Itoa(names.SyncerMetricsAPIPort)}, s.MetricsTLS)

	kubeletSrv := &http.Server{
		Handler:           s.KubeletHandler,
		ReadHeaderTimeout: 30 * time.Second,
		// 连接进来前已包成 *tls.Conn，Serve 会自动握手；禁用 h2，SPDY/WebSocket upgrade 只走 HTTP/1.1
		TLSNextProto: map[string]func(*http.Server, *tls.Conn, http.Handler){},
		ErrorLog:     nil,
		// CONNECT 的目标（节点 IP）随连接带进请求，指标路由据此认出节点
		ConnContext: connContext,
	}
	metricsSrv := &http.Server{
		Handler:           s.MetricsHandler,
		ReadHeaderTimeout: 30 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      60 * time.Second,
		TLSNextProto:      map[string]func(*http.Server, *tls.Conn, http.Handler){},
	}
	go func() {
		if err := kubeletSrv.Serve(kubeletLn); err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, net.ErrClosed) {
			s.Log.Error(err, "fake kubelet server exited")
		}
	}()
	go func() {
		if err := metricsSrv.Serve(metricsLn); err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, net.ErrClosed) {
			s.Log.Error(err, "metrics api server exited")
		}
	}()
	go func() {
		<-ctx.Done()
		_ = ln.Close()
		_ = kubeletLn.Close()
		_ = metricsLn.Close()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = kubeletSrv.Shutdown(shutdownCtx)
		_ = metricsSrv.Shutdown(shutdownCtx)
	}()

	s.Log.Info("konnectivity CONNECT server listening", "socket", s.SocketPath)
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				continue
			}
			return fmt.Errorf("accept on %s: %w", s.SocketPath, err)
		}
		go s.handle(ctx, conn, kubeletLn, metricsLn)
	}
}

// handle 解析一条 CONNECT 请求，按目标把连接交给对应 TLS server 或直连转发。
func (s *ConnectServer) handle(ctx context.Context, conn net.Conn, kubeletLn, metricsLn *chanListener) {
	_ = conn.SetReadDeadline(time.Now().Add(connectReadTimeout))
	br := bufio.NewReader(conn)
	req, err := http.ReadRequest(br)
	if err != nil {
		_ = conn.Close()
		return
	}
	if req.Method != http.MethodConnect {
		writeStatus(conn, http.StatusMethodNotAllowed, "only CONNECT is supported")
		_ = conn.Close()
		return
	}
	target := req.Host
	if target == "" {
		target = req.URL.Host
	}
	host, port, err := net.SplitHostPort(target)
	if err != nil {
		writeStatus(conn, http.StatusBadRequest, "CONNECT target must be host:port")
		_ = conn.Close()
		return
	}
	var ln *chanListener
	switch {
	case port == strconv.Itoa(names.FakeKubeletPort):
		ln = kubeletLn
	case port == strconv.Itoa(names.SyncerMetricsAPIPort) && (s.MetricsAddr == "" || host == s.MetricsAddr):
		ln = metricsLn
	}
	if ln == nil {
		s.forward(ctx, conn, br, target)
		return
	}
	_ = conn.SetReadDeadline(time.Time{})
	if _, err := io.WriteString(conn, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		_ = conn.Close()
		return
	}
	// 客户端在收到 200 之前不会发 TLS 数据，但若 reader 已有缓冲则不能丢
	var raw net.Conn = conn
	if br.Buffered() > 0 {
		raw = &bufferedConn{Conn: conn, r: br}
	}
	raw = &targetConn{Conn: raw, target: target}
	tlsConn := tls.Server(raw, ln.tlsConfig)
	if !ln.push(ctx, tlsConn) {
		_ = tlsConn.Close()
	}
}

// forward 把 CONNECT 连接直连转发到 target：apiserver 要主动连的用户 Pod 都走这里。
// 一方读到 EOF 后对另一方半关闭，两个方向都结束再关闭连接；ctx 结束时强制关闭。
func (s *ConnectServer) forward(ctx context.Context, conn net.Conn, br *bufio.Reader, target string) {
	upstream, err := (&net.Dialer{Timeout: dialTimeout}).DialContext(ctx, "tcp", target)
	if err != nil {
		s.Log.Info("tunnel dial failed", "target", target, "error", err.Error())
		writeStatus(conn, http.StatusBadGateway, "dial "+target+": "+err.Error())
		_ = conn.Close()
		return
	}
	_ = conn.SetReadDeadline(time.Time{})
	if _, err := io.WriteString(conn, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		_ = conn.Close()
		_ = upstream.Close()
		return
	}
	s.Log.V(1).Info("tunnel forwarding", "target", target)
	var raw net.Conn = conn
	if br.Buffered() > 0 {
		raw = &bufferedConn{Conn: conn, r: br}
	}
	stop := context.AfterFunc(ctx, func() {
		_ = raw.Close()
		_ = upstream.Close()
	})
	defer stop()
	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(upstream, raw)
		closeWrite(upstream)
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(raw, upstream)
		closeWrite(raw)
		done <- struct{}{}
	}()
	<-done
	<-done
	_ = raw.Close()
	_ = upstream.Close()
}

// closeWrite 半关闭写方向，让对端读到 EOF；不支持半关闭的连接直接关闭。
func closeWrite(c net.Conn) {
	if bc, ok := c.(*bufferedConn); ok {
		c = bc.Conn
	}
	if cw, ok := c.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
		return
	}
	_ = c.Close()
}

func writeStatus(w io.Writer, code int, msg string) {
	_, _ = fmt.Fprintf(w, "HTTP/1.1 %d %s\r\nContent-Type: text/plain\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s",
		code, http.StatusText(code), len(msg), msg)
}

// bufferedConn 先读 bufio.Reader 里已缓冲的数据，再读底层连接。
type bufferedConn struct {
	net.Conn
	r *bufio.Reader
}

func (c *bufferedConn) Read(p []byte) (int, error) { return c.r.Read(p) }

// ServerTLSConfig 构造要求客户端证书的服务端 TLS 配置：证书 certFile/keyFile，客户端 CA 为 clientCAPEM；
// requiredCN 非空时客户端证书 CN 必须一致（聚合层要求 front-proxy-client）。只允许 HTTP/1.1。
func ServerTLSConfig(certFile, keyFile string, clientCAPEM []byte, requiredCN string) (*tls.Config, error) {
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("load serving certificate: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(clientCAPEM) {
		return nil, errors.New("client CA bundle contains no certificates")
	}
	cfg := &tls.Config{
		Certificates: []tls.Certificate{cert},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    pool,
		MinVersion:   tls.VersionTLS12,
		NextProtos:   []string{"http/1.1"},
	}
	if requiredCN != "" {
		cfg.VerifyConnection = func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return errors.New("client certificate required")
			}
			if cn := cs.PeerCertificates[0].Subject.CommonName; cn != requiredCN {
				return fmt.Errorf("client certificate CN %q is not %q", cn, requiredCN)
			}
			return nil
		}
	}
	return cfg, nil
}
