package resources

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"

	"vk8s.io/vk8s/internal/syncer/translate"
)

// setupPVC PVC 虚 → 宿；宿 → 虚 status。删虚拟 PVC 即删宿主 PVC。
func setupPVC(mgr ctrl.Manager, c *Context) error {
	d := &downSync[*corev1.PersistentVolumeClaim]{
		c:        c,
		resource: "persistentvolumeclaim",
		newObj:   func() *corev1.PersistentVolumeClaim { return &corev1.PersistentVolumeClaim{} },
		translate: func(_ context.Context, v *corev1.PersistentVolumeClaim) (*corev1.PersistentVolumeClaim, error) {
			return translate.TranslatePVC(v, c.HostNamespace), nil
		},
		mutate:    translate.ApplyPVCMutations,
		writeback: translate.PVCStatusChanged,
	}
	return d.setup(mgr)
}
