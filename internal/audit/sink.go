package audit

import (
	"context"
	"errors"
	"time"
)

// ErrUnavailable 存储暂时不可达（连接失败、5xx、超时、迁移尚未完成）：管线无限退避重试，API 返回 503。
// 其他错误视为存储明确拒绝（如解析失败），管线有限重试后整批丢弃。
var ErrUnavailable = errors.New("audit sink unavailable")

// Filter 查询条件。零值字段表示不过滤；Outcome 为 success（状态码 < 400）或 failure（≥ 400）。
type Filter struct {
	From, To  time.Time
	VCluster  string
	User      string
	Verb      string
	Resource  string
	Namespace string
	Name      string
	Source    Source
	Outcome   string
	// Token 访问令牌 ID（annotations[vk8s.io/token-id]），只对 kube 事件有意义。
	Token string
	Limit int
	// 游标：只取 (ts, id) 严格小于 (BeforeTS, BeforeID) 的记录。
	BeforeTS time.Time
	BeforeID string
}

// Sink 审计存储的写端。Write 整批成功或整批失败。
type Sink interface {
	Write(ctx context.Context, events []Event) error
	Ping(ctx context.Context) error
	Migrate(ctx context.Context, retentionDays int) error
}

// Querier 审计存储的读端。Get 的 vcluster 为空表示不限；找不到返回 (nil, nil)。
type Querier interface {
	Query(ctx context.Context, f Filter) ([]Event, error)
	Get(ctx context.Context, id, vcluster string) (*Event, error)
}

// NopSink 丢弃一切，供测试与未配置存储时使用。
type NopSink struct{}

func (NopSink) Write(context.Context, []Event) error { return nil }
func (NopSink) Ping(context.Context) error           { return nil }
func (NopSink) Migrate(context.Context, int) error   { return nil }
