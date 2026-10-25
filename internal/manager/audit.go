package manager

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"vk8s.io/vk8s/internal/audit"
	"vk8s.io/vk8s/internal/audit/clickhouse"
	"vk8s.io/vk8s/internal/config"
)

// clickhousePasswordKey Secret 里的密码 key。
const clickhousePasswordKey = "password"

// auditRuntime 审计相关的运行期组件；Enabled 为假时其余字段为 nil。
type auditRuntime struct {
	Enabled  bool
	Pipeline *audit.Pipeline
	Querier  audit.Querier
}

// setupAudit 按配置组装 sink 与管线并注册到 manager：管线与迁移器都是 Runnable（所有副本运行）。
// ClickHouse 不可达不阻止 manager 启动：迁移器后台重试，事件先留在队列里。
func setupAudit(ctx context.Context, direct client.Client, cfg *config.Config, mgr ctrl.Manager) (*auditRuntime, error) {
	if !cfg.Audit.Enabled {
		return &auditRuntime{}, nil
	}
	password, err := readSecretKey(ctx, direct, cfg.SystemNamespace, cfg.Audit.ClickHouse.PasswordSecretRef, clickhousePasswordKey)
	if err != nil {
		return nil, fmt.Errorf("audit: %w", err)
	}
	sink, err := clickhouse.New(clickhouse.Config{
		URL: cfg.Audit.ClickHouse.URL, Database: cfg.Audit.ClickHouse.Database,
		Username: cfg.Audit.ClickHouse.Username, Password: password,
	})
	if err != nil {
		return nil, fmt.Errorf("audit: %w", err)
	}
	pipeline := audit.NewPipeline(sink, audit.Options{
		QueueSize: cfg.Audit.QueueSize, BatchSize: cfg.Audit.BatchSize, BatchInterval: cfg.Audit.BatchInterval.Duration,
	})
	if err := mgr.Add(pipeline); err != nil {
		return nil, fmt.Errorf("audit: add pipeline: %w", err)
	}
	if err := mgr.Add(&auditMigrator{sink: sink, retentionDays: cfg.Audit.RetentionDays, interval: 30 * time.Second}); err != nil {
		return nil, fmt.Errorf("audit: add migrator: %w", err)
	}
	return &auditRuntime{Enabled: true, Pipeline: pipeline, Querier: sink}, nil
}

// readSecretKey 读 "namespace/name" 引用的 Secret 的一个 key；引用为空返回空串。
func readSecretKey(ctx context.Context, c client.Client, defaultNS string, ref config.SecretRef, key string) (string, error) {
	ns, name := ref.Split()
	if name == "" {
		return "", nil
	}
	if ns == "" {
		ns = defaultNS
	}
	secret := &corev1.Secret{}
	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, secret); err != nil {
		return "", fmt.Errorf("read secret %s/%s: %w", ns, name, err)
	}
	v := string(secret.Data[key])
	if v == "" {
		return "", fmt.Errorf("secret %s/%s has no key %q", ns, name, key)
	}
	return v, nil
}

// auditMigrator 启动时探活并建库建表；失败按 interval 重试直到成功或 ctx 结束，不阻塞 manager 就绪。
type auditMigrator struct {
	sink          audit.Sink
	retentionDays int
	interval      time.Duration
}

func (m *auditMigrator) NeedLeaderElection() bool { return false }

// Start 实现 Runnable；成功后返回 nil（不常驻），ctx 结束也返回 nil。
func (m *auditMigrator) Start(ctx context.Context) error {
	log := logf.FromContext(ctx).WithName("audit-migrator")
	interval := m.interval
	if interval <= 0 {
		interval = 30 * time.Second
	}
	for {
		if err := m.attempt(ctx); err == nil {
			audit.SinkUp.Set(1)
			log.Info("audit storage ready", "retentionDays", m.retentionDays)
			return nil
		} else {
			audit.SinkUp.Set(0)
			log.Error(err, "audit storage not ready, will retry", "retryIn", interval.String())
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(interval):
		}
	}
}

func (m *auditMigrator) attempt(ctx context.Context) error {
	if err := m.sink.Ping(ctx); err != nil {
		return err
	}
	return m.sink.Migrate(ctx, m.retentionDays)
}
