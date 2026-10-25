package webhook

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/go-logr/logr"
	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	quota "k8s.io/apiserver/pkg/quota/v1"
	"k8s.io/kubernetes/pkg/quota/v1/evaluator/core"
	"k8s.io/utils/clock"
	"sigs.k8s.io/controller-runtime/pkg/client"
	crwebhook "sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"vk8s.io/vk8s/internal/names"
	"vk8s.io/vk8s/internal/syncer/translate"
)

// QuotaValidator 平台配额在虚拟 apiserver 侧的即时拒绝（设计 2026-10-25 §2 / §3.2）。
//
// 权威仍是宿主租户 ns 的 ResourceQuota vk8s-quota：hard 含控制平面开销，used 由宿主 apiserver 在准入时同步更新。
// 有效用量 = used + 在途量（虚拟集群里已存在、宿主侧还没有对应对象的 Pod / PVC），请求量用 k8s 自带求值器，
// 与原生算法一致。webhook 自身的任何问题都放行（failurePolicy Ignore + 这里的错误路径），宿主兜底。
type QuotaValidator struct {
	Log logr.Logger
	// Host 宿主缓存（ResourceQuota、Pod、PVC）。
	Host client.Reader
	// Virtual 虚拟缓存（Pod、PVC），用于在途量。
	Virtual       client.Reader
	HostNamespace string
	Clock         clock.Clock
	// ReadTimeout 一次判定里缓存读取的总预算；informer 同步不了时放行而不是拖到 apiserver 超时。零值 1s。
	ReadTimeout time.Duration

	// admitted 刚放行、尚未出现在虚拟缓存里的 CREATE：同一批并发创建互相看不见，靠它互相计数。
	mu       sync.Mutex
	admitted map[string]admittedEntry
}

// admittedTTL 账本条目最长保留时间：对象落库后会被在途量接管，从未落库（被别的 webhook 拒了）则到期清除。
const admittedTTL = 10 * time.Second

type admittedEntry struct {
	usage corev1.ResourceList
	at    time.Time
}

// errNoQuota 宿主 RQ 不存在；Evaluate 已计 skipped{no_quota}，调用方不再重复计数。
var errNoQuota = errors.New("host resourcequota not found")

// Decision 非 nil 表示拒绝。
type Decision struct {
	Exceeded []corev1.ResourceName
	Message  string
}

// Evaluate 判定 obj（old 仅 UPDATE 时非 nil）。返回 (nil, nil) 放行；err 非 nil 由调用方放行并计 skipped。
func (v *QuotaValidator) Evaluate(ctx context.Context, obj, old client.Object) (*Decision, error) {
	rq := &corev1.ResourceQuota{}
	if err := v.Host.Get(ctx, client.ObjectKey{Namespace: v.HostNamespace, Name: names.HostResourceQuotaName}, rq); err != nil {
		if apierrors.IsNotFound(err) {
			QuotaSkipped.WithLabelValues("no_quota").Inc()
			return nil, fmt.Errorf("%w: %s/%s", errNoQuota, v.HostNamespace, names.HostResourceQuotaName)
		}
		return nil, fmt.Errorf("read host resourcequota: %w", err)
	}
	hard := rq.Spec.Hard
	limited := quota.ResourceNames(hard)

	requested, err := requestedUsage(obj, old, v.Clock)
	if err != nil {
		return nil, err
	}
	need := removeZeros(quota.Mask(requested, limited))
	if len(need) == 0 {
		return nil, nil
	}

	inflight, err := v.inflightUsage(ctx)
	if err != nil {
		return nil, err
	}
	used := quota.Add(quota.Add(rq.Status.Used, inflight), v.recentUsage(ctx))
	// 与原生插件一致：只比较本次请求涉及的维度，别的维度已经超限不影响无关对象
	after := quota.Add(quota.Mask(used, quota.ResourceNames(need)), need)
	ok, exceeded := quota.LessThanOrEqual(after, hard)
	if ok {
		return nil, nil
	}
	sort.Slice(exceeded, func(i, j int) bool { return exceeded[i] < exceeded[j] })
	for _, r := range exceeded {
		QuotaDenied.WithLabelValues(string(r)).Inc()
	}

	// 用户视角：hard 与 used 各扣掉控制平面开销（只对超出的维度算，避免把开销独有的维度带进文案）
	overhead := quota.Mask(parseOverhead(rq), exceeded)
	failedRequested := quota.Mask(need, exceeded)
	failedUsed := quota.SubtractWithNonNegativeResult(quota.Mask(used, exceeded), overhead)
	failedHard := quota.SubtractWithNonNegativeResult(quota.Mask(hard, exceeded), overhead)
	return &Decision{Exceeded: exceeded, Message: quotaMessage(rq.Name, failedRequested, failedUsed, failedHard)}, nil
}

var _ admission.Handler = (*QuotaValidator)(nil)

// Handle 实现 admission.Handler：按 Kind 解码（UPDATE 连同 OldObject），交给 Evaluate；
// 解码或求值出错一律放行并计 skipped（no_quota 已在 Evaluate 内计数）；拒绝即 403 Forbidden，文案原样。
func (v *QuotaValidator) Handle(ctx context.Context, req admission.Request) admission.Response {
	obj, old, err := decodeQuotaRequest(req)
	if err != nil {
		QuotaSkipped.WithLabelValues("error").Inc()
		v.Log.Error(err, "quota webhook: decode failed, allowing", "kind", req.Kind.Kind, "namespace", req.Namespace, "name", req.Name)
		return admission.Allowed("quota check skipped")
	}
	if obj == nil {
		return admission.Allowed("not a quota-tracked kind")
	}
	ctx, cancel := context.WithTimeout(ctx, v.readTimeout())
	defer cancel()
	decision, err := v.Evaluate(ctx, obj, old)
	if err != nil {
		if !errors.Is(err, errNoQuota) {
			QuotaSkipped.WithLabelValues("error").Inc()
		}
		v.Log.V(1).Info("quota webhook: no verdict, allowing", "reason", err.Error(), "kind", req.Kind.Kind, "namespace", req.Namespace, "name", req.Name)
		return admission.Allowed("quota check skipped")
	}
	if decision == nil {
		if req.Operation == admissionv1.Create && !(req.DryRun != nil && *req.DryRun) {
			v.RecordAdmitted(obj)
		}
		return admission.Allowed("")
	}
	return admission.Denied(decision.Message)
}

func (v *QuotaValidator) readTimeout() time.Duration {
	if v.ReadTimeout > 0 {
		return v.ReadTimeout
	}
	return time.Second
}

// decodeQuotaRequest 返回 (obj, old)；不认识的 Kind 返回 (nil, nil, nil)。
func decodeQuotaRequest(req admission.Request) (client.Object, client.Object, error) {
	newObj := func() client.Object {
		switch req.Kind.Kind {
		case "Pod":
			return &corev1.Pod{}
		case "PersistentVolumeClaim":
			return &corev1.PersistentVolumeClaim{}
		}
		return nil
	}
	obj := newObj()
	if obj == nil {
		return nil, nil, nil
	}
	if err := json.Unmarshal(req.Object.Raw, obj); err != nil {
		return nil, nil, fmt.Errorf("decode %s: %w", req.Kind.Kind, err)
	}
	if obj.GetNamespace() == "" {
		obj.SetNamespace(req.Namespace)
	}
	var old client.Object
	if req.Operation == admissionv1.Update && len(req.OldObject.Raw) > 0 {
		old = newObj()
		if err := json.Unmarshal(req.OldObject.Raw, old); err != nil {
			return nil, nil, fmt.Errorf("decode old %s: %w", req.Kind.Kind, err)
		}
	}
	return obj, old, nil
}

// RegisterQuota 把 QuotaValidator 挂到 names.QuotaWebhookPath。controller-runtime 的 Server.Register 允许在 Start 之后调用。
func RegisterQuota(srv crwebhook.Server, v *QuotaValidator) {
	srv.Register(names.QuotaWebhookPath, &admission.Webhook{Handler: v})
}

// requestedUsage 对象的请求量。Pod 走 core.PodUsageFunc（终态不计、init 取最大值、扩展资源 requests.<name>）；
// PVC 走 PVC 求值器，UPDATE 只计 requests.storage 的增量，计数维度不重复计。
func requestedUsage(obj, old client.Object, clk clock.Clock) (corev1.ResourceList, error) {
	switch o := obj.(type) {
	case *corev1.Pod:
		return core.PodUsageFunc(o, clk)
	case *corev1.PersistentVolumeClaim:
		ev := core.NewPersistentVolumeClaimEvaluator(nil)
		usage, err := ev.Usage(o)
		if err != nil {
			return nil, err
		}
		prev, isUpdate := old.(*corev1.PersistentVolumeClaim)
		if !isUpdate || prev == nil {
			return usage, nil
		}
		prevUsage, err := ev.Usage(prev)
		if err != nil {
			return nil, err
		}
		delta := quota.SubtractWithNonNegativeResult(usage, prevUsage)
		for name := range delta {
			if isCountResource(name) {
				delete(delta, name)
			}
		}
		return delta, nil
	}
	return corev1.ResourceList{}, nil
}

// inflightUsage 虚拟集群里已存在、宿主缓存里还没有对应对象的 Pod（非终态）与 PVC 的用量之和。
// 两侧各一次 List（只读，不深拷贝），不逐个 Get。
func (v *QuotaValidator) inflightUsage(ctx context.Context) (corev1.ResourceList, error) {
	total := corev1.ResourceList{}
	inNS := client.InNamespace(v.HostNamespace)

	hostPods := &corev1.PodList{}
	if err := v.Host.List(ctx, hostPods, inNS, client.UnsafeDisableDeepCopy); err != nil {
		return nil, fmt.Errorf("list host pods: %w", err)
	}
	onHost := make(map[string]struct{}, len(hostPods.Items))
	for i := range hostPods.Items {
		onHost[hostPods.Items[i].Name] = struct{}{}
	}
	pods := &corev1.PodList{}
	if err := v.Virtual.List(ctx, pods, client.UnsafeDisableDeepCopy); err != nil {
		return nil, fmt.Errorf("list virtual pods: %w", err)
	}
	for i := range pods.Items {
		p := &pods.Items[i]
		if !core.QuotaV1Pod(p, v.Clock) {
			continue
		}
		if _, ok := onHost[translate.HostName(p.Name, p.Namespace)]; ok {
			continue
		}
		u, err := core.PodUsageFunc(p, v.Clock)
		if err != nil {
			return nil, err
		}
		total = quota.Add(total, u)
	}

	hostPVCs := &corev1.PersistentVolumeClaimList{}
	if err := v.Host.List(ctx, hostPVCs, inNS, client.UnsafeDisableDeepCopy); err != nil {
		return nil, fmt.Errorf("list host pvcs: %w", err)
	}
	pvcOnHost := make(map[string]struct{}, len(hostPVCs.Items))
	for i := range hostPVCs.Items {
		pvcOnHost[hostPVCs.Items[i].Name] = struct{}{}
	}
	pvcs := &corev1.PersistentVolumeClaimList{}
	if err := v.Virtual.List(ctx, pvcs, client.UnsafeDisableDeepCopy); err != nil {
		return nil, fmt.Errorf("list virtual pvcs: %w", err)
	}
	ev := core.NewPersistentVolumeClaimEvaluator(nil)
	for i := range pvcs.Items {
		c := &pvcs.Items[i]
		if _, ok := pvcOnHost[translate.HostName(c.Name, c.Namespace)]; ok {
			continue
		}
		u, err := ev.Usage(c)
		if err != nil {
			return nil, err
		}
		total = quota.Add(total, u)
	}
	return total, nil
}

// RecordAdmitted 记住一个刚放行的 CREATE（Handle 在非 dry-run 放行后调用）。
func (v *QuotaValidator) RecordAdmitted(obj client.Object) {
	usage, err := requestedUsage(obj, nil, v.Clock)
	if err != nil || len(usage) == 0 {
		return
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.admitted == nil {
		v.admitted = map[string]admittedEntry{}
	}
	v.admitted[admittedKey(obj)] = admittedEntry{usage: usage, at: v.Clock.Now()}
}

// recentUsage 账本里仍然「看不见」的条目之和：已出现在虚拟缓存的（由在途量接管）与超过 TTL 的一并清除。
// 读缓存出错时保守地保留条目。
func (v *QuotaValidator) recentUsage(ctx context.Context) corev1.ResourceList {
	v.mu.Lock()
	defer v.mu.Unlock()
	total := corev1.ResourceList{}
	now := v.Clock.Now()
	for key, e := range v.admitted {
		if now.Sub(e.at) > admittedTTL {
			delete(v.admitted, key)
			continue
		}
		kind, ns, name := splitAdmittedKey(key)
		var obj client.Object
		switch kind {
		case "Pod":
			obj = &corev1.Pod{}
		case "PersistentVolumeClaim":
			obj = &corev1.PersistentVolumeClaim{}
		default:
			delete(v.admitted, key)
			continue
		}
		if err := v.Virtual.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, obj); err == nil {
			delete(v.admitted, key)
			continue
		}
		total = quota.Add(total, e.usage)
	}
	return total
}

func admittedKey(obj client.Object) string {
	kind := "Pod"
	if _, ok := obj.(*corev1.PersistentVolumeClaim); ok {
		kind = "PersistentVolumeClaim"
	}
	return kind + "/" + obj.GetNamespace() + "/" + obj.GetName()
}

func splitAdmittedKey(key string) (kind, ns, name string) {
	parts := strings.SplitN(key, "/", 3)
	if len(parts) != 3 {
		return "", "", ""
	}
	return parts[0], parts[1], parts[2]
}

// parseOverhead RQ 注解里的控制平面开销；缺失或损坏视为零（文案退化为宿主口径，不影响判定）。
func parseOverhead(rq *corev1.ResourceQuota) corev1.ResourceList {
	raw := rq.Annotations[names.AnnotationQuotaOverhead]
	if raw == "" {
		return corev1.ResourceList{}
	}
	var out corev1.ResourceList
	if err := json.Unmarshal([]byte(raw), &out); err != nil || out == nil {
		return corev1.ResourceList{}
	}
	return out
}

// quotaMessage 与 apiserver 配额插件同格式：资源名字典序，k=v 逗号连接。
func quotaMessage(name string, requested, used, hard corev1.ResourceList) string {
	return fmt.Sprintf("exceeded quota: %s, requested: %s, used: %s, limited: %s", name, prettyPrint(requested), prettyPrint(used), prettyPrint(hard))
}

func prettyPrint(item corev1.ResourceList) string {
	keys := make([]string, 0, len(item))
	for k := range item {
		keys = append(keys, string(k))
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		q := item[corev1.ResourceName(k)]
		parts = append(parts, k+"="+q.String())
	}
	return strings.Join(parts, ",")
}

func removeZeros(rl corev1.ResourceList) corev1.ResourceList {
	out := corev1.ResourceList{}
	for k, q := range rl {
		if !q.IsZero() {
			out[k] = q
		}
	}
	return out
}

// isCountResource 对象计数类维度（UPDATE 时不重复计）。
func isCountResource(name corev1.ResourceName) bool {
	s := string(name)
	return name == corev1.ResourcePersistentVolumeClaims || name == corev1.ResourcePods ||
		strings.HasPrefix(s, "count/") || strings.HasSuffix(s, ".storageclass.storage.k8s.io/persistentvolumeclaims")
}
