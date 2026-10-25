// Package bootstrap 实现 syncer 的启动自愈清单（设计文档 7.10）：启动时执行一遍、之后每 5 分钟重复，全部幂等。
// 用户是虚拟集群的 cluster-admin，删了这些对象也会被补回。
package bootstrap

import (
	"context"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	rbacv1 "k8s.io/api/rbac/v1"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"vk8s.io/vk8s/internal/names"
	"vk8s.io/vk8s/internal/syncer/resources"
)

// Options bootstrap 所需的全部输入。
type Options struct {
	Log logr.Logger

	VCName        string
	HostNamespace string
	// HostDNSServiceIP CoreDNS forward 的上游（宿主 kube-dns）。
	HostDNSServiceIP string
	// InternalCA webhook caBundle。
	InternalCA []byte
	// PodIP syncer 自身 pod IP，metrics Endpoints 指向它。
	PodIP string
	// WebhookPort webhook 监听端口（127.0.0.1）。
	WebhookPort int

	// Virtual / Host 直连 client（不经 cache）。
	Virtual client.Client
	Host    client.Client
}

// Runner 执行清单。
type Runner struct {
	Options
}

// New 构造。
func New(o Options) *Runner { return &Runner{Options: o} }

// Run 顺序执行全部步骤；单步失败不阻断后续步骤，最后聚合返回。
func (r *Runner) Run(ctx context.Context) error {
	steps := []struct {
		name string
		fn   func(context.Context) error
	}{
		{"admins ClusterRoleBinding", r.ensureAdminsBinding},
		{"default/kubernetes ClusterIP", r.alignKubernetesService},
		{"service MutatingWebhookConfiguration", r.ensureWebhook},
		{"quota ValidatingWebhookConfiguration", r.ensureQuotaWebhook},
		{"CoreDNS", r.ensureCoreDNS},
		{"metrics.k8s.io", r.ensureMetrics},
		{"orphan cleanup", r.cleanOrphans},
	}
	var errs []error
	for _, s := range steps {
		if err := s.fn(ctx); err != nil {
			r.Log.Error(err, "bootstrap step failed", "step", s.name)
			errs = append(errs, fmt.Errorf("%s: %w", s.name, err))
		}
	}
	return utilerrors.NewAggregate(errs)
}

// RunPeriodically 每 interval 重跑一次，直到 ctx 结束。实现 manager.Runnable。
func (r *Runner) RunPeriodically(interval time.Duration) func(context.Context) error {
	return func(ctx context.Context) error {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return nil
			case <-ticker.C:
				if err := r.Run(ctx); err != nil {
					r.Log.Error(err, "periodic bootstrap finished with errors")
				}
			}
		}
	}
}

// ensure 幂等 create-or-update：obj 只带名字，mutate 填期望值（已存在时在读回的对象上覆盖我们关心的字段）。
func (r *Runner) ensure(ctx context.Context, obj client.Object, mutate func()) error {
	res, err := controllerutil.CreateOrUpdate(ctx, r.Virtual, obj, func() error {
		mutate()
		return nil
	})
	if err != nil {
		return fmt.Errorf("%T %s: %w", obj, client.ObjectKeyFromObject(obj), err)
	}
	if res != controllerutil.OperationResultNone {
		r.Log.Info("bootstrap applied", "kind", fmt.Sprintf("%T", obj), "name", client.ObjectKeyFromObject(obj).String(), "result", res)
	}
	return nil
}

// ensureAdminsBinding ClusterRoleBinding vk8s:admins → cluster-admin（TokenReview 返回的 group）。
func (r *Runner) ensureAdminsBinding(ctx context.Context) error {
	crb := &rbacv1.ClusterRoleBinding{}
	crb.Name = names.AdminsClusterRoleBinding
	return r.ensure(ctx, crb, func() {
		setManaged(crb)
		crb.RoleRef = rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: "cluster-admin"}
		crb.Subjects = []rbacv1.Subject{{APIGroup: rbacv1.GroupName, Kind: rbacv1.GroupKind, Name: names.GroupAdmins}}
	})
}

// cleanOrphans 删除宿主 ns 里 managed=true 但虚拟对象已不存在的对象。
func (r *Runner) cleanOrphans(ctx context.Context) error {
	return resources.CleanOrphans(ctx, r.Log.WithName("orphans"), r.Host, r.Virtual, r.HostNamespace)
}

// setManaged 给 syncer 投放的虚拟对象打 managed 标签。
func setManaged(obj client.Object) {
	l := obj.GetLabels()
	if l == nil {
		l = map[string]string{}
	}
	l[names.LabelManaged] = "true"
	obj.SetLabels(l)
}
