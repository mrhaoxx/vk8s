package resources

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	"vk8s.io/vk8s/internal/syncer/translate"
)

// setupSecret Secret 虚 → 宿 全量同步；跳过旧式 service-account-token 类型。
// 宿主侧 watch 排除 syncer 自己签发的 SA token Secret（它们属于 Pod reconciler）。
func setupSecret(mgr ctrl.Manager, c *Context) error {
	d := &downSync[*corev1.Secret]{
		c:        c,
		resource: "secret",
		newObj:   func() *corev1.Secret { return &corev1.Secret{} },
		skip: func(_ context.Context, v *corev1.Secret) (bool, error) {
			return translate.IsServiceAccountTokenSecret(v), nil
		},
		translate: func(_ context.Context, v *corev1.Secret) (*corev1.Secret, error) {
			return translate.TranslateSecret(v, c.HostNamespace), nil
		},
		mutate: translate.ApplySecretMutations,
		needRecreate: func(desired, actual *corev1.Secret) bool {
			if actual.Type != desired.Type {
				return true
			}
			return actual.Immutable != nil && *actual.Immutable && (desired.Immutable == nil || !*desired.Immutable)
		},
	}
	notToken := predicate.NewTypedPredicateFuncs[*corev1.Secret](func(s *corev1.Secret) bool {
		return translate.IsManaged(s) && !IsSATokenSecret(s)
	})
	return ctrl.NewControllerManagedBy(mgr).
		Named(d.resource).
		For(&corev1.Secret{}).
		WatchesRawSource(source.Kind[*corev1.Secret](c.HostCache, &corev1.Secret{},
			handler.TypedEnqueueRequestsFromMapFunc[*corev1.Secret, reconcile.Request](func(_ context.Context, s *corev1.Secret) []reconcile.Request {
				key, ok := translate.VirtualKey(s)
				if !ok {
					return nil
				}
				return []reconcile.Request{{NamespacedName: key}}
			}), notToken)).
		Complete(d)
}
