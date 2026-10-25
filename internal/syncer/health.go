package syncer

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/go-logr/logr"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	crmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
)

// readiness syncer 的就绪状态：bootstrap 完成且两端 cache 同步后才 ready。
type readiness struct {
	bootstrapDone atomic.Bool
	cachesSynced  atomic.Bool
}

func (r *readiness) ready() bool {
	return r.bootstrapDone.Load() && r.cachesSynced.Load()
}

// healthServer 在 --health-listen 上提供 /healthz、/readyz、/metrics。
type healthServer struct {
	log    logr.Logger
	listen string
	ready  *readiness
}

// Start 实现 manager.Runnable。
func (h *healthServer) Start(ctx context.Context) error {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) {
		if !h.ready.ready() {
			msg := "not ready:"
			if !h.ready.bootstrapDone.Load() {
				msg += " bootstrap pending"
			}
			if !h.ready.cachesSynced.Load() {
				msg += " caches syncing"
			}
			http.Error(w, msg, http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	// controller-runtime 的 Registry：reconcile 指标 + resources 包注册的同步错误 / 翻译拒绝 / token 续签失败计数
	mux.Handle("GET /metrics", promhttp.HandlerFor(crmetrics.Registry, promhttp.HandlerOpts{}))

	srv := &http.Server{
		Addr:              h.listen,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() {
		h.log.Info("health server listening", "addr", h.listen)
		errCh <- srv.ListenAndServe()
	}()
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
		return nil
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}
