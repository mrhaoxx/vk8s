package resources

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"vk8s.io/vk8s/internal/syncer/translate"
)

// 节点不再被 nodePlacement 允许时驱逐其上的虚拟 Pod。两个入口：
//   - Node 镜像控制器：placement 变化、宿主节点标签变化或节点被删，使某节点不再被允许时，按索引列出其上全部虚拟 Pod 驱逐；
//   - Pod 控制器：宿主把 Pod 调度到不再允许的节点时（placement 变化前创建、变化后才调度），驱逐而不是 binding。
//
// 驱逐 = 删除虚拟 Pod，gracePeriod 取 Pod 自身值与 names.PlacementEvictionMaxGracePeriodSeconds 的较小者；
// Pod reconciler 的删除路径以同一 gracePeriod 删宿主 Pod，虚拟集群里的控制器按新约束重建。不看 PDB：
// nodePlacement 是硬隔离边界，租户的 PDB 不应阻止管理员把负载赶出节点。

// IndexPodNodeName 虚拟 Pod 按 spec.nodeName 查找的 cache 索引名。
const IndexPodNodeName = "spec.nodeName"

// podNodeNameIndexer IndexPodNodeName 的索引函数。
func podNodeNameIndexer(obj client.Object) []string {
	pod, ok := obj.(*corev1.Pod)
	if !ok || pod.Spec.NodeName == "" {
		return nil
	}
	return []string{pod.Spec.NodeName}
}

// RegisterIndexes 在虚拟 cache 上建索引，必须在 manager 启动前调用。
func RegisterIndexes(ctx context.Context, indexer client.FieldIndexer) error {
	return indexer.IndexField(ctx, &corev1.Pod{}, IndexPodNodeName, podNodeNameIndexer)
}

// evictPodsOnNode 驱逐绑定在 node 上、且宿主上确实存在对应 Pod 的全部虚拟 Pod。
// 宿主 Pod 不存在的跳过：可能是 nodeName 指向该节点但被 SyncRejected 的 Pod（从未在节点上运行），
// 或宿主 Pod 被外力删除（Pod reconciler 自己会收尾）。单个失败不阻断其余，最后聚合返回以便重试。
func (c *Context) evictPodsOnNode(ctx context.Context, node string) error {
	list := &corev1.PodList{}
	if err := c.Virtual.List(ctx, list, client.MatchingFields{IndexPodNodeName: node}); err != nil {
		return fmt.Errorf("list pods on node %s: %w", node, err)
	}
	var errs []error
	for i := range list.Items {
		vpod := &list.Items[i]
		hpod := &corev1.Pod{}
		if err := c.Host.Get(ctx, c.HostKey(translate.HostNameFor(vpod)), hpod); err != nil {
			if !isNotFound(err) {
				errs = append(errs, fmt.Errorf("check host pod for %s/%s: %w", vpod.Namespace, vpod.Name, err))
			}
			continue
		}
		if err := c.evictPod(ctx, vpod, hpod, node); err != nil {
			errs = append(errs, err)
		}
	}
	return utilerrors.NewAggregate(errs)
}

// evictPod 驱逐一个宿主 Pod 为 hpod 的虚拟 Pod。已在删除中或已终态的跳过；宿主 Pod 已终态但 status
// 尚未回写的也跳过，否则 Job 会把它当作消失的 Pod 重跑。Event 与指标只在确实删掉之后记录：
// Node 与 Pod 两个控制器可能竞争驱逐同一个 Pod，第二次 Delete 得到 NotFound 不算一次驱逐。
func (c *Context) evictPod(ctx context.Context, vpod, hpod *corev1.Pod, node string) error {
	if !translate.Evictable(vpod) || translate.IsTerminal(hpod) {
		return nil
	}
	grace := translate.EvictionGracePeriod(vpod)
	if err := c.Virtual.Delete(ctx, vpod, client.GracePeriodSeconds(grace)); err != nil {
		if isNotFound(err) {
			return nil
		}
		return fmt.Errorf("evict pod %s/%s: %w", vpod.Namespace, vpod.Name, err)
	}
	c.Recorder.Eventf(vpod, corev1.EventTypeWarning, EventEvictedByNodePlacement,
		"node %s is no longer allowed for this virtual cluster; deleting pod with a %ds grace period", node, grace)
	PlacementEvictions.Inc()
	return nil
}
