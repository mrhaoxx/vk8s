package kubelet

import (
	"bufio"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
)

// startConnectServer 在临时 UDS 上启动 ConnectServer，返回 socket 路径。
func startConnectServer(t *testing.T) string {
	t.Helper()
	// macOS 的 UDS 路径上限 104 字节，不用 t.TempDir()
	dir, err := os.MkdirTemp("/tmp", "vk8s-connect-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "k.sock")
	srv := &ConnectServer{
		Log:            logr.Discard(),
		SocketPath:     sock,
		KubeletHandler: http.NotFoundHandler(),
		KubeletTLS:     &tls.Config{},
		MetricsHandler: http.NotFoundHandler(),
		MetricsTLS:     &tls.Config{},
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = srv.Start(ctx) }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(sock); err == nil {
			return sock
		}
		if time.Now().After(deadline) {
			t.Fatal("socket not created")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// connect 发 CONNECT 请求（可紧跟 payload，模拟客户端不等 200 就发数据），返回状态行与 reader。
func connect(t *testing.T, sock, target, payload string) (net.Conn, *bufio.Reader, string) {
	t.Helper()
	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	_, _ = fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n%s", target, target, payload)
	br := bufio.NewReader(conn)
	status, err := br.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if line == "\r\n" {
			break
		}
	}
	return conn, br, strings.TrimSpace(status)
}

func TestConnectServerForwardsOtherTargets(t *testing.T) {
	echo, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer echo.Close()
	go func() {
		for {
			c, err := echo.Accept()
			if err != nil {
				return
			}
			go func() {
				_, _ = io.Copy(c, c)
				_ = c.Close()
			}()
		}
	}()

	sock := startConnectServer(t)
	// payload 紧跟 CONNECT 请求发出，必须经 bufferedConn 原样转发
	conn, br, status := connect(t, sock, echo.Addr().String(), "hello")
	if !strings.Contains(status, "200") {
		t.Fatalf("status %q", status)
	}
	buf := make([]byte, 5)
	if _, err := io.ReadFull(br, buf); err != nil || string(buf) != "hello" {
		t.Fatalf("echo of buffered payload: %q %v", buf, err)
	}
	if _, err := io.WriteString(conn, "world"); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(br, buf); err != nil || string(buf) != "world" {
		t.Fatalf("echo: %q %v", buf, err)
	}
	// 客户端半关闭 → echo 读到 EOF 关闭 → 客户端读到 EOF
	_ = conn.(*net.UnixConn).CloseWrite()
	if _, err := br.ReadByte(); err != io.EOF {
		t.Fatalf("expected EOF after half close, got %v", err)
	}
}

func TestConnectServerRejectsUnreachableTarget(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close() // 端口立刻不可达

	sock := startConnectServer(t)
	_, _, status := connect(t, sock, addr, "")
	if !strings.Contains(status, "502") {
		t.Fatalf("status %q", status)
	}
}
