package controllers

import (
	"context"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"vk8s.io/vk8s/api/v1alpha1"
)

// DefaultTokenRetention 过期 token 保留时长（供排查），之后删除。
const DefaultTokenRetention = 24 * time.Hour

// AccessTokenGCReconciler 删除过期超过 Retention 的 AccessToken。
type AccessTokenGCReconciler struct {
	client.Client
	Retention time.Duration
	// Now 可注入以便测试；为空取 time.Now。
	Now func() time.Time
}

// SetupWithManager 注册 controller。
func (r *AccessTokenGCReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named("accesstoken-gc").
		For(&v1alpha1.AccessToken{}).
		Complete(r)
}

// Reconcile 到期即删，否则按剩余时间重入。
func (r *AccessTokenGCReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	tok := &v1alpha1.AccessToken{}
	if err := r.Get(ctx, req.NamespacedName, tok); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !tok.DeletionTimestamp.IsZero() || tok.Spec.ExpiresAt.IsZero() {
		return ctrl.Result{}, nil
	}
	retention := r.Retention
	if retention <= 0 {
		retention = DefaultTokenRetention
	}
	now := time.Now()
	if r.Now != nil {
		now = r.Now()
	}
	deleteAt := tok.Spec.ExpiresAt.Time.Add(retention)
	if now.Before(deleteAt) {
		return ctrl.Result{RequeueAfter: deleteAt.Sub(now)}, nil
	}
	if err := r.Delete(ctx, tok); err != nil && !apierrors.IsNotFound(err) {
		return ctrl.Result{}, err
	}
	log.Info("deleted expired access token", "token", tok.Name, "user", tok.Spec.User, "vcluster", tok.Spec.VCluster, "expiredAt", tok.Spec.ExpiresAt.Time)
	return ctrl.Result{}, nil
}
