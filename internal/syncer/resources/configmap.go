package resources

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"

	"vk8s.io/vk8s/internal/syncer/translate"
)

// setupConfigMap ConfigMap 虚 → 宿 全量同步。
func setupConfigMap(mgr ctrl.Manager, c *Context) error {
	d := &downSync[*corev1.ConfigMap]{
		c:        c,
		resource: "configmap",
		newObj:   func() *corev1.ConfigMap { return &corev1.ConfigMap{} },
		translate: func(_ context.Context, v *corev1.ConfigMap) (*corev1.ConfigMap, error) {
			return translate.TranslateConfigMap(v, c.HostNamespace), nil
		},
		mutate: translate.ApplyConfigMapMutations,
		needRecreate: func(desired, actual *corev1.ConfigMap) bool {
			// immutable 的 ConfigMap 不能改内容；虚拟侧同样不可改，只在 immutable 标志本身变化时重建
			return actual.Immutable != nil && *actual.Immutable && (desired.Immutable == nil || !*desired.Immutable)
		},
	}
	return d.setup(mgr)
}
