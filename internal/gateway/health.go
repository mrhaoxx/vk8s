package gateway

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"k8s.io/klog/v2"
)

// HealthServer 提供 /healthz、/readyz、/metrics。
type HealthServer struct {
	addr    string
	ready   func() bool
	metrics *Metrics
}

// NewHealthServer 创建健康检查服务。ready 为 true 时 /readyz 返回 200（informer 已同步）。
func NewHealthServer(addr string, ready func() bool, metrics *Metrics) *HealthServer {
	return &HealthServer{addr: addr, ready: ready, metrics: metrics}
}

// Handler 返回 http.Handler，便于复用。
func (h *HealthServer) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		if h.ready() {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ok\n"))
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("informer not synced\n"))
	})
	mux.Handle("/metrics", promhttp.HandlerFor(h.metrics.Registry, promhttp.HandlerOpts{
		ErrorHandling: promhttp.ContinueOnError,
	}))
	return mux
}

// Run 监听直到 ctx 取消。
func (h *HealthServer) Run(ctx context.Context) error {
	srv := &http.Server{
		Addr:              h.addr,
		Handler:           h.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	errc := make(chan error, 1)
	go func() {
		klog.InfoS("health server listening", "addr", h.addr)
		errc <- srv.ListenAndServe()
	}()
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
		return nil
	case err := <-errc:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}
