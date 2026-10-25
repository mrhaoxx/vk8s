package resources

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"vk8s.io/vk8s/internal/syncer/translate"
)

// CleanOrphans 删除宿主 ns 内 `vk8s.io/managed=true` 但对应虚拟对象已不存在的对象。
// 使用直连 client（不依赖 cache 是否同步），由 bootstrap 在启动时与每 5 分钟调用。
// 遇到查询错误时宁可保留对象，不在不确定时删除。
func CleanOrphans(ctx context.Context, log logr.Logger, host, virtual client.Client, hostNamespace string) error {
	var errs []error
	collect := func(err error) {
		if err != nil {
			errs = append(errs, err)
		}
	}
	collect(cleanKind(ctx, log, host, virtual, hostNamespace, "pod",
		&corev1.PodList{}, func(l *corev1.PodList) []*corev1.Pod { return ptrs(l.Items) },
		func() client.Object { return &corev1.Pod{} }))
	collect(cleanKind(ctx, log, host, virtual, hostNamespace, "service",
		&corev1.ServiceList{}, func(l *corev1.ServiceList) []*corev1.Service { return ptrs(l.Items) },
		func() client.Object { return &corev1.Service{} }))
	collect(cleanKind(ctx, log, host, virtual, hostNamespace, "endpoints",
		&corev1.EndpointsList{}, func(l *corev1.EndpointsList) []*corev1.Endpoints { return ptrs(l.Items) },
		func() client.Object { return &corev1.Endpoints{} }))
	collect(cleanKind(ctx, log, host, virtual, hostNamespace, "endpointslice",
		&discoveryv1.EndpointSliceList{}, func(l *discoveryv1.EndpointSliceList) []*discoveryv1.EndpointSlice { return ptrs(l.Items) },
		func() client.Object { return &discoveryv1.EndpointSlice{} }))
	collect(cleanKind(ctx, log, host, virtual, hostNamespace, "configmap",
		&corev1.ConfigMapList{}, func(l *corev1.ConfigMapList) []*corev1.ConfigMap { return ptrs(l.Items) },
		func() client.Object { return &corev1.ConfigMap{} }))
	collect(cleanKind(ctx, log, host, virtual, hostNamespace, "persistentvolumeclaim",
		&corev1.PersistentVolumeClaimList{}, func(l *corev1.PersistentVolumeClaimList) []*corev1.PersistentVolumeClaim { return ptrs(l.Items) },
		func() client.Object { return &corev1.PersistentVolumeClaim{} }))
	collect(cleanKind(ctx, log, host, virtual, hostNamespace, "ingress",
		&networkingv1.IngressList{}, func(l *networkingv1.IngressList) []*networkingv1.Ingress { return ptrs(l.Items) },
		func() client.Object { return &networkingv1.Ingress{} }))
	collect(cleanSecrets(ctx, log, host, virtual, hostNamespace))
	return errors.NewAggregate(errs)
}

func ptrs[T any](items []T) []*T {
	out := make([]*T, len(items))
	for i := range items {
		out[i] = &items[i]
	}
	return out
}

// cleanKind 对一种资源执行孤儿清理：宿主 managed 对象 → 反查虚拟键 → 虚拟对象 NotFound 则删除。
func cleanKind[T client.Object, L client.ObjectList](
	ctx context.Context, log logr.Logger, host, virtual client.Client, hostNamespace, resource string,
	list L, items func(L) []T, newVirtual func() client.Object,
) error {
	if err := host.List(ctx, list, client.InNamespace(hostNamespace), client.MatchingLabelsSelector{Selector: translate.ManagedSelector()}); err != nil {
		return fmt.Errorf("list host %s: %w", resource, err)
	}
	var errs []error
	for _, obj := range items(list) {
		key, ok := translate.VirtualKey(obj)
		if !ok {
			continue
		}
		if orphan, err := virtualMissing(ctx, virtual, newVirtual(), key); err != nil {
			errs = append(errs, err)
		} else if orphan {
			log.Info("deleting orphaned host object", "resource", resource, "name", obj.GetName(), "virtual", key.String())
			if err := host.Delete(ctx, obj); client.IgnoreNotFound(err) != nil {
				errs = append(errs, fmt.Errorf("delete orphan %s %s: %w", resource, obj.GetName(), err))
				continue
			}
			OrphansDeleted.WithLabelValues(resource).Inc()
		}
	}
	return errors.NewAggregate(errs)
}

// cleanSecrets Secret 需区分两类：普通同步 Secret 对应虚拟 Secret；SA token Secret 对应宿主 Pod。
func cleanSecrets(ctx context.Context, log logr.Logger, host, virtual client.Client, hostNamespace string) error {
	list := &corev1.SecretList{}
	if err := host.List(ctx, list, client.InNamespace(hostNamespace), client.MatchingLabelsSelector{Selector: translate.ManagedSelector()}); err != nil {
		return fmt.Errorf("list host secrets: %w", err)
	}
	var errs []error
	for i := range list.Items {
		s := &list.Items[i]
		var orphan bool
		var err error
		if IsSATokenSecret(s) {
			pod := &corev1.Pod{}
			err = host.Get(ctx, client.ObjectKey{Namespace: hostNamespace, Name: s.Labels[LabelSATokenPod]}, pod)
			orphan = isNotFound(err)
			if orphan {
				// 宿主 Pod 不在，但虚拟 Pod 还在且尚未重建时也保留一会儿：由 Pod reconciler 决定重建或清理
				if key, ok := translate.VirtualKey(s); ok {
					if missing, verr := virtualMissing(ctx, virtual, &corev1.Pod{}, key); verr != nil || !missing {
						orphan = false
						err = verr
					}
				}
			} else if err != nil {
				errs = append(errs, err)
				continue
			}
		} else {
			key, ok := translate.VirtualKey(s)
			if !ok {
				continue
			}
			orphan, err = virtualMissing(ctx, virtual, &corev1.Secret{}, key)
		}
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if orphan {
			log.Info("deleting orphaned host object", "resource", "secret", "name", s.Name)
			if err := host.Delete(ctx, s); client.IgnoreNotFound(err) != nil {
				errs = append(errs, fmt.Errorf("delete orphan secret %s: %w", s.Name, err))
				continue
			}
			OrphansDeleted.WithLabelValues("secret").Inc()
		}
	}
	return errors.NewAggregate(errs)
}

// virtualMissing 虚拟对象是否确定不存在（NotFound）；其他错误原样返回。
func virtualMissing(ctx context.Context, virtual client.Client, obj client.Object, key types.NamespacedName) (bool, error) {
	err := virtual.Get(ctx, key, obj)
	if err == nil {
		return false, nil
	}
	if isNotFound(err) {
		return true, nil
	}
	return false, err
}
