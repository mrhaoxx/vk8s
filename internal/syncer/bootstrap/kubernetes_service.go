package bootstrap

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"vk8s.io/vk8s/internal/names"
)

const (
	kubernetesServiceName = "kubernetes"
	// alignAttempts apiserver 内的 kubernetes-service 控制器每 10s 会把缺失的 Service 以 CIDR 首 IP 重建，
	// 删除与重建之间存在竞争，失败就立刻再试几次。
	alignAttempts = 5
)

// alignKubernetesService 把虚拟 default/kubernetes 的 ClusterIP 对齐到宿主 Service vc-<name> 的 ClusterIP。
// ClusterIP 不可变，不一致时删掉重建；ports 443→6443 不变。上游控制器只校验 type 与 ports，不会改回。
func (r *Runner) alignKubernetesService(ctx context.Context) error {
	hostSvc := &corev1.Service{}
	if err := r.Host.Get(ctx, client.ObjectKey{Namespace: r.HostNamespace, Name: names.ServiceName(r.VCName)}, hostSvc); err != nil {
		return fmt.Errorf("read host service %s: %w", names.ServiceName(r.VCName), err)
	}
	wantIP := hostSvc.Spec.ClusterIP
	if wantIP == "" || wantIP == corev1.ClusterIPNone {
		return fmt.Errorf("host service %s has no ClusterIP", names.ServiceName(r.VCName))
	}

	key := client.ObjectKey{Namespace: metav1.NamespaceDefault, Name: kubernetesServiceName}
	var lastErr error
	for attempt := 0; attempt < alignAttempts; attempt++ {
		cur := &corev1.Service{}
		err := r.Virtual.Get(ctx, key, cur)
		switch {
		case err == nil && cur.Spec.ClusterIP == wantIP:
			return nil
		case err == nil:
			r.Log.Info("realigning default/kubernetes clusterIP", "from", cur.Spec.ClusterIP, "to", wantIP)
			if err := r.Virtual.Delete(ctx, cur, client.Preconditions{UID: &cur.UID}); client.IgnoreNotFound(err) != nil {
				lastErr = err
				continue
			}
		case !apierrors.IsNotFound(err):
			return err
		}
		if err := r.Virtual.Create(ctx, desiredKubernetesService(wantIP)); err != nil {
			lastErr = err
			if apierrors.IsAlreadyExists(err) {
				// 被上游控制器抢先重建了，下一轮会检查 IP 并再删
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(200 * time.Millisecond):
				}
				continue
			}
			return err
		}
		return nil
	}
	return fmt.Errorf("align default/kubernetes after %d attempts: %w", alignAttempts, lastErr)
}

// desiredKubernetesService 与 apiserver 自建的形态一致，只是 ClusterIP 显式指定。
func desiredKubernetesService(ip string) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      kubernetesServiceName,
			Namespace: metav1.NamespaceDefault,
			Labels:    map[string]string{"component": "apiserver", "provider": "kubernetes"},
		},
		Spec: corev1.ServiceSpec{
			Type:            corev1.ServiceTypeClusterIP,
			ClusterIP:       ip,
			ClusterIPs:      []string{ip},
			SessionAffinity: corev1.ServiceAffinityNone,
			Ports: []corev1.ServicePort{{
				Name:       "https",
				Protocol:   corev1.ProtocolTCP,
				Port:       names.ServicePort,
				TargetPort: intstr.FromInt32(names.APIServerPort),
			}},
		},
	}
}
