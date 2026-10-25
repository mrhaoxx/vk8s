package translate

import (
	"sort"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/utils/ptr"

	"vk8s.io/vk8s/internal/names"
	"vk8s.io/vk8s/internal/syncer/placement"
)

const (
	// SATokenVolumeName 注入到宿主 Pod 的 SA token secret volume 名。
	SATokenVolumeName = "vk8s-sa-token"
	// SATokenMountPath 与 kubelet 自动挂载路径一致，Pod 内 client-go 无感知。
	SATokenMountPath = "/var/run/secrets/kubernetes.io/serviceaccount"
	// SATokenKeyToken / SATokenKeyCA / SATokenKeyNamespace 宿主 SA token Secret 的 key；
	// SATokenKeyPodName / SATokenKeyPodUID 给 downwardAPI 卷提供虚拟 Pod 的名字与 UID。
	SATokenKeyToken     = "token"
	SATokenKeyCA        = "ca.crt"
	SATokenKeyNamespace = "namespace"
	SATokenKeyPodName   = "pod-name"
	SATokenKeyPodUID    = "pod-uid"

	// DefaultSchedulerName 宿主侧统一使用默认调度器。
	DefaultSchedulerName = "default-scheduler"
	// HostServiceAccountName 宿主 Pod 一律使用 default SA（且不自动挂载）。
	HostServiceAccountName = "default"

	kubernetesServicePort = "443"
	clusterDomain         = "cluster.local"
)

// PodEnv Pod 翻译所需的集群上下文，由调用方从 cache 中读取后传入。
type PodEnv struct {
	// HostNamespace 宿主 ns。
	HostNamespace string
	// Placement 生效的调度约束：管理员配置的静态放置 + 预约租约。
	Placement placement.Effective
	// NodeAllowed 判定节点名是否在允许集合内（宿主节点存在且满足 Placement）。
	// 为 nil 时任何显式 nodeName 都被拒绝。
	NodeAllowed func(name string) bool
	// KubernetesServiceIP 虚拟 default/kubernetes 的 ClusterIP。
	KubernetesServiceIP string
	// DNSServiceIP 虚拟 kube-system/kube-dns 的 ClusterIP；dnsPolicy 需要改写而该值为空时返回 NotReadyError。
	DNSServiceIP string
}

// NeedsSAToken 该虚拟 Pod 是否需要 syncer 代为签发 SA token：
// 未显式关闭自动挂载，或 volumes 里有 projected serviceAccountToken 源。
func NeedsSAToken(pod *corev1.Pod) bool {
	if pod.Spec.AutomountServiceAccountToken == nil || *pod.Spec.AutomountServiceAccountToken {
		return true
	}
	for i := range pod.Spec.Volumes {
		if p := pod.Spec.Volumes[i].Projected; p != nil {
			for j := range p.Sources {
				if p.Sources[j].ServiceAccountToken != nil {
					return true
				}
			}
		}
	}
	return false
}

// ServiceAccountNameOf 虚拟 Pod 使用的 SA 名（空则 default）。
func ServiceAccountNameOf(pod *corev1.Pod) string {
	if pod.Spec.ServiceAccountName != "" {
		return pod.Spec.ServiceAccountName
	}
	if pod.Spec.DeprecatedServiceAccount != "" {
		return pod.Spec.DeprecatedServiceAccount
	}
	return HostServiceAccountName
}

// TranslatePod 按设计文档 7.3 的六步把虚拟 Pod 翻译成宿主 Pod。
// 返回 RejectError 表示对象不合规（不重试）；NotReadyError 表示依赖未就绪（稍后重试）。
func TranslatePod(vpod *corev1.Pod, env PodEnv) (*corev1.Pod, error) {
	hpod := &corev1.Pod{
		ObjectMeta: HostObjectMeta(vpod, env.HostNamespace),
		Spec:       *vpod.Spec.DeepCopy(),
	}
	spec := &hpod.Spec
	vns := vpod.Namespace
	needsToken := NeedsSAToken(vpod)
	tokenSecret := names.SATokenSecretName(hpod.Name)

	// 1. 名字映射：volumes、imagePullSecrets、env/envFrom 里的 ConfigMap/Secret/PVC 引用。
	rewriteVolumes(spec.Volumes, vns, tokenSecret, needsToken)
	for i := range spec.ImagePullSecrets {
		spec.ImagePullSecrets[i].Name = HostName(spec.ImagePullSecrets[i].Name, vns)
	}
	for i := range spec.Containers {
		rewriteContainerRefs(&spec.Containers[i], vns)
	}
	for i := range spec.InitContainers {
		rewriteContainerRefs(&spec.InitContainers[i], vns)
	}
	for i := range spec.EphemeralContainers {
		rewriteEphemeralContainerRefs(&spec.EphemeralContainers[i].EphemeralContainerCommon, vns)
	}

	// 2. 调度：nodePlacement 合并、tolerations 替换、nodeName 白名单、调度器与优先级重置；
	//    podAffinity / topologySpread 的选择器限定到虚拟 namespace。
	if err := applyPlacement(spec, env); err != nil {
		return nil, err
	}
	if err := scopePodAffinity(spec, vns); err != nil {
		return nil, err
	}

	// 3. ServiceAccount：宿主侧 default + 不自动挂载，token 由 syncer 以 Secret volume 注入。
	spec.ServiceAccountName = HostServiceAccountName
	spec.DeprecatedServiceAccount = ""
	automount := vpod.Spec.AutomountServiceAccountToken == nil || *vpod.Spec.AutomountServiceAccountToken
	spec.AutomountServiceAccountToken = ptr.To(false)
	if automount {
		injectSATokenVolume(spec, tokenSecret)
	}

	// 4. 环境变量：KUBERNETES_* 指向虚拟 apiserver，关闭 service links，downwardAPI 名字字面量化。
	if env.KubernetesServiceIP == "" {
		return nil, &NotReadyError{What: "virtual default/kubernetes ClusterIP"}
	}
	injected := kubernetesEnv(env.KubernetesServiceIP)
	for i := range spec.Containers {
		spec.Containers[i].Env = rewriteEnv(spec.Containers[i].Env, injected, vpod)
	}
	for i := range spec.InitContainers {
		spec.InitContainers[i].Env = rewriteEnv(spec.InitContainers[i].Env, injected, vpod)
	}
	for i := range spec.EphemeralContainers {
		spec.EphemeralContainers[i].Env = rewriteEnv(spec.EphemeralContainers[i].Env, injected, vpod)
	}
	spec.EnableServiceLinks = ptr.To(false)

	// 5. DNS：ClusterFirst(WithHostNet) → None + 虚拟 CoreDNS。
	if err := applyDNS(spec, vns, env.DNSServiceIP); err != nil {
		return nil, err
	}

	// 6. 其余原样。补一个与虚拟 Pod 名一致的 hostname，让容器内 `hostname` 与用户所见一致。
	if spec.Hostname == "" && len(validation.IsDNS1123Label(vpod.Name)) == 0 {
		spec.Hostname = vpod.Name
	}
	// v2: downwardAPI volume、resourceClaims 引用的名字改写。
	return hpod, nil
}

// scopePodAffinity 一个虚拟集群的全部 namespace 落在同一个宿主 ns，podAffinity / podAntiAffinity /
// topologySpreadConstraints 的 labelSelector 在宿主侧会跨虚拟 namespace 匹配，因此追加
// `vk8s.io/vnamespace` 约束：
//   - 未写 namespaces / namespaceSelector → 限定本 namespace（k8s 默认语义）；
//   - 写了 namespaces → `vnamespace In [...]`，宿主侧 namespaces 清空（即宿主 ns 自身）；
//   - namespaceSelector 为空选择器 `{}` → 全部 namespace，不加约束；非空 → 拒绝（无法在宿主侧表达）。
//
// labelSelector 为 nil 时 k8s 视为不匹配任何 Pod，保持原样。用户 `vk8s.io/` 前缀的 label 已被丢弃，不会冲突。
func scopePodAffinity(spec *corev1.PodSpec, vns string) error {
	if spec.Affinity != nil {
		if pa := spec.Affinity.PodAffinity; pa != nil {
			for i := range pa.RequiredDuringSchedulingIgnoredDuringExecution {
				if err := scopeAffinityTerm(&pa.RequiredDuringSchedulingIgnoredDuringExecution[i], vns); err != nil {
					return err
				}
			}
			for i := range pa.PreferredDuringSchedulingIgnoredDuringExecution {
				if err := scopeAffinityTerm(&pa.PreferredDuringSchedulingIgnoredDuringExecution[i].PodAffinityTerm, vns); err != nil {
					return err
				}
			}
		}
		if pa := spec.Affinity.PodAntiAffinity; pa != nil {
			for i := range pa.RequiredDuringSchedulingIgnoredDuringExecution {
				if err := scopeAffinityTerm(&pa.RequiredDuringSchedulingIgnoredDuringExecution[i], vns); err != nil {
					return err
				}
			}
			for i := range pa.PreferredDuringSchedulingIgnoredDuringExecution {
				if err := scopeAffinityTerm(&pa.PreferredDuringSchedulingIgnoredDuringExecution[i].PodAffinityTerm, vns); err != nil {
					return err
				}
			}
		}
	}
	// topologySpreadConstraints 只看本 namespace 的 Pod
	for i := range spec.TopologySpreadConstraints {
		scopeLabelSelector(&spec.TopologySpreadConstraints[i].LabelSelector, []string{vns})
	}
	return nil
}

func scopeAffinityTerm(term *corev1.PodAffinityTerm, vns string) error {
	namespaces := []string{vns}
	switch {
	case term.NamespaceSelector != nil:
		if len(term.NamespaceSelector.MatchLabels) > 0 || len(term.NamespaceSelector.MatchExpressions) > 0 {
			return Rejectf("pod affinity namespaceSelector is not supported in a virtual cluster; use namespaces")
		}
		// 空选择器 = 全部 namespace = 整个宿主 ns
		namespaces = nil
		term.NamespaceSelector = nil
	case len(term.Namespaces) > 0:
		namespaces = append([]string(nil), term.Namespaces...)
		sort.Strings(namespaces)
		term.Namespaces = nil
	}
	if namespaces != nil {
		scopeLabelSelector(&term.LabelSelector, namespaces)
	}
	return nil
}

// scopeLabelSelector 给非 nil 的 labelSelector 追加 `vk8s.io/vnamespace In namespaces`。
func scopeLabelSelector(sel **metav1.LabelSelector, namespaces []string) {
	if *sel == nil {
		return
	}
	s := *sel
	for _, e := range s.MatchExpressions {
		if e.Key == names.LabelVNamespace {
			return
		}
	}
	s.MatchExpressions = append(s.MatchExpressions, metav1.LabelSelectorRequirement{
		Key:      names.LabelVNamespace,
		Operator: metav1.LabelSelectorOpIn,
		Values:   namespaces,
	})
}

// MissingEphemeralContainers 期望宿主 Pod 里有、现有宿主 Pod 里还没有的 ephemeral 容器（`kubectl debug`
// 只能追加，不能改和删），调用方经 pods/ephemeralcontainers 子资源追加到宿主 Pod。
func MissingEphemeralContainers(desired, actual *corev1.Pod) []corev1.EphemeralContainer {
	have := make(map[string]struct{}, len(actual.Spec.EphemeralContainers))
	for _, ec := range actual.Spec.EphemeralContainers {
		have[ec.Name] = struct{}{}
	}
	var missing []corev1.EphemeralContainer
	for _, ec := range desired.Spec.EphemeralContainers {
		if _, ok := have[ec.Name]; !ok {
			missing = append(missing, *ec.DeepCopy())
		}
	}
	return missing
}

// rewriteVolumes 把 volumes 里对虚拟 ConfigMap/Secret/PVC 的引用改成宿主名；
// projected 的 serviceAccountToken 源替换成 syncer 签发的 token Secret（否则会挂上宿主 SA 的 token）。
func rewriteVolumes(volumes []corev1.Volume, vns, tokenSecret string, needsToken bool) {
	for i := range volumes {
		v := &volumes[i]
		switch {
		case v.ConfigMap != nil:
			v.ConfigMap.Name = HostName(v.ConfigMap.Name, vns)
		case v.Secret != nil:
			v.Secret.SecretName = HostName(v.Secret.SecretName, vns)
		case v.PersistentVolumeClaim != nil:
			v.PersistentVolumeClaim.ClaimName = HostName(v.PersistentVolumeClaim.ClaimName, vns)
		case v.CSI != nil && v.CSI.NodePublishSecretRef != nil:
			v.CSI.NodePublishSecretRef.Name = HostName(v.CSI.NodePublishSecretRef.Name, vns)
		case v.Projected != nil:
			for j := range v.Projected.Sources {
				src := &v.Projected.Sources[j]
				switch {
				case src.ConfigMap != nil:
					src.ConfigMap.Name = HostName(src.ConfigMap.Name, vns)
				case src.Secret != nil:
					src.Secret.Name = HostName(src.Secret.Name, vns)
				case src.ServiceAccountToken != nil:
					path := src.ServiceAccountToken.Path
					if path == "" {
						path = SATokenKeyToken
					}
					// 宿主上没有虚拟 SA，用 syncer 签发的 token 替换；audience/expiration 由 TokenRequest 侧统一。
					src.ServiceAccountToken = nil
					if needsToken {
						src.Secret = &corev1.SecretProjection{
							LocalObjectReference: corev1.LocalObjectReference{Name: tokenSecret},
							Items:                []corev1.KeyToPath{{Key: SATokenKeyToken, Path: path}},
						}
					}
				}
			}
		}
		// 放在改名之后：这里新加的 Secret 源指向宿主上的 token Secret，不能再被 HostName 改写。
		if needsToken {
			virtualizeDownward(v, tokenSecret)
		}
	}
}

// downwardVirtualKeys 宿主 kubelet 会按宿主 Pod 填值的 downwardAPI 字段，及其在 token Secret 里的 key。
var downwardVirtualKeys = map[string]string{
	"metadata.namespace": SATokenKeyNamespace,
	"metadata.name":      SATokenKeyPodName,
	"metadata.uid":       SATokenKeyPodUID,
}

// virtualizeDownward 把 downwardAPI（普通卷或投射源）里的 namespace/name/uid 项改由 token Secret 提供，
// 与 env 的 fieldRef 改写一致；其他字段（labels、annotations、resourceFieldRef 等）仍交给 kubelet。
// 普通 downwardAPI 卷需要混合来源时改成等价的 projected 卷。
func virtualizeDownward(v *corev1.Volume, tokenSecret string) {
	switch {
	case v.DownwardAPI != nil:
		keep, moved := splitDownward(v.DownwardAPI.Items)
		if len(moved) == 0 {
			return
		}
		var sources []corev1.VolumeProjection
		if len(keep) > 0 {
			sources = append(sources, corev1.VolumeProjection{DownwardAPI: &corev1.DownwardAPIProjection{Items: keep}})
		}
		sources = append(sources, tokenSecretSource(tokenSecret, moved))
		v.VolumeSource = corev1.VolumeSource{Projected: &corev1.ProjectedVolumeSource{
			Sources: sources, DefaultMode: v.DownwardAPI.DefaultMode,
		}}
	case v.Projected != nil:
		out := make([]corev1.VolumeProjection, 0, len(v.Projected.Sources)+1)
		for _, src := range v.Projected.Sources {
			if src.DownwardAPI == nil {
				out = append(out, src)
				continue
			}
			keep, moved := splitDownward(src.DownwardAPI.Items)
			if len(keep) > 0 {
				out = append(out, corev1.VolumeProjection{DownwardAPI: &corev1.DownwardAPIProjection{Items: keep}})
			}
			if len(moved) > 0 {
				out = append(out, tokenSecretSource(tokenSecret, moved))
			}
		}
		v.Projected.Sources = out
	}
}

func splitDownward(items []corev1.DownwardAPIVolumeFile) (keep []corev1.DownwardAPIVolumeFile, moved []corev1.KeyToPath) {
	for _, it := range items {
		if it.FieldRef != nil {
			if key, ok := downwardVirtualKeys[it.FieldRef.FieldPath]; ok {
				moved = append(moved, corev1.KeyToPath{Key: key, Path: it.Path, Mode: it.Mode})
				continue
			}
		}
		keep = append(keep, it)
	}
	return keep, moved
}

func tokenSecretSource(name string, items []corev1.KeyToPath) corev1.VolumeProjection {
	return corev1.VolumeProjection{Secret: &corev1.SecretProjection{
		LocalObjectReference: corev1.LocalObjectReference{Name: name}, Items: items,
	}}
}

func rewriteContainerRefs(c *corev1.Container, vns string) {
	rewriteEnvRefs(c.Env, c.EnvFrom, vns)
}

func rewriteEphemeralContainerRefs(c *corev1.EphemeralContainerCommon, vns string) {
	rewriteEnvRefs(c.Env, c.EnvFrom, vns)
}

func rewriteEnvRefs(env []corev1.EnvVar, envFrom []corev1.EnvFromSource, vns string) {
	for i := range env {
		vf := env[i].ValueFrom
		if vf == nil {
			continue
		}
		if vf.ConfigMapKeyRef != nil {
			vf.ConfigMapKeyRef.Name = HostName(vf.ConfigMapKeyRef.Name, vns)
		}
		if vf.SecretKeyRef != nil {
			vf.SecretKeyRef.Name = HostName(vf.SecretKeyRef.Name, vns)
		}
	}
	for i := range envFrom {
		if envFrom[i].ConfigMapRef != nil {
			envFrom[i].ConfigMapRef.Name = HostName(envFrom[i].ConfigMapRef.Name, vns)
		}
		if envFrom[i].SecretRef != nil {
			envFrom[i].SecretRef.Name = HostName(envFrom[i].SecretRef.Name, vns)
		}
	}
}

// applyPlacement 合并生效的节点约束（静态放置 + 预约租约）：
//   - 静态 term：nodeSelector 每项 → `key In [value]`，再加 `vk8s.io/bookable NotIn [true]`（可预约节点只能通过租约使用，
//     对没有该标签的节点 NotIn 也成立），追加到用户 required nodeAffinity 的每一条 term（term 间 OR、term 内 AND）；
//     nodeNames 非空时按 term×nodeName 展开为 `matchFields metadata.name In [name]`（apiserver 要求 In 只能带一个值）。
//     因为 NotIn 总是存在，nodeAffinity 总会被注入。
//   - 租约 term：每个租约节点 × 用户每条原始 term，只追加 `matchFields metadata.name In [node]`，不带静态约束也不带 NotIn；
//     与静态 term 并列（OR），这样租约节点不受静态规则限制，但仍受用户自己的 affinity 约束。
//   - tolerations 整体替换为 placement.tolerations。
//   - nodeName 非空且不在允许集合 → 拒绝。
//   - schedulerName 重置、priorityClassName/priority/preemptionPolicy 清空（宿主 Priority admission 会拒绝裸 priority）。
func applyPlacement(spec *corev1.PodSpec, env PodEnv) error {
	if spec.NodeName != "" {
		if env.NodeAllowed == nil || !env.NodeAllowed(spec.NodeName) {
			return Rejectf("spec.nodeName %q is not in the node set allowed for this virtual cluster", spec.NodeName)
		}
	}

	p := env.Placement.Static
	var exprs []corev1.NodeSelectorRequirement
	keys := make([]string, 0, len(p.NodeSelector))
	for k := range p.NodeSelector {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		exprs = append(exprs, corev1.NodeSelectorRequirement{
			Key:      k,
			Operator: corev1.NodeSelectorOpIn,
			Values:   []string{p.NodeSelector[k]},
		})
	}
	exprs = append(exprs, corev1.NodeSelectorRequirement{
		Key:      names.LabelBookable,
		Operator: corev1.NodeSelectorOpNotIn,
		Values:   []string{names.BookableTrue},
	})
	nodeNames := append([]string(nil), p.NodeNames...)
	sort.Strings(nodeNames)
	leased := append([]string(nil), env.Placement.Leased...)
	sort.Strings(leased)

	if spec.Affinity == nil {
		spec.Affinity = &corev1.Affinity{}
	}
	if spec.Affinity.NodeAffinity == nil {
		spec.Affinity.NodeAffinity = &corev1.NodeAffinity{}
	}
	required := spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution
	if required == nil || len(required.NodeSelectorTerms) == 0 {
		required = &corev1.NodeSelector{NodeSelectorTerms: []corev1.NodeSelectorTerm{{}}}
	}
	userTerms := required.NodeSelectorTerms
	var terms []corev1.NodeSelectorTerm
	for _, t := range userTerms {
		base := *t.DeepCopy()
		base.MatchExpressions = append(base.MatchExpressions, exprs...)
		if len(nodeNames) == 0 {
			terms = append(terms, base)
			continue
		}
		for _, n := range nodeNames {
			tt := *base.DeepCopy()
			tt.MatchFields = append(tt.MatchFields, nodeNameField(n))
			terms = append(terms, tt)
		}
	}
	for _, n := range leased {
		for _, t := range userTerms {
			tt := *t.DeepCopy()
			tt.MatchFields = append(tt.MatchFields, nodeNameField(n))
			terms = append(terms, tt)
		}
	}
	required.NodeSelectorTerms = terms
	spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution = required

	spec.Tolerations = nil
	for i := range p.Tolerations {
		spec.Tolerations = append(spec.Tolerations, *p.Tolerations[i].DeepCopy())
	}
	spec.SchedulerName = DefaultSchedulerName
	spec.PriorityClassName = ""
	spec.Priority = nil
	spec.PreemptionPolicy = nil
	return nil
}

// nodeNameField `metadata.name In [name]` 字段选择器。
func nodeNameField(name string) corev1.NodeSelectorRequirement {
	return corev1.NodeSelectorRequirement{Key: "metadata.name", Operator: corev1.NodeSelectorOpIn, Values: []string{name}}
}

// injectSATokenVolume 追加 token Secret volume 并挂到每个容器的 SA 目录（用户已有同路径挂载则跳过该容器）。
func injectSATokenVolume(spec *corev1.PodSpec, tokenSecret string) {
	spec.Volumes = append(spec.Volumes, corev1.Volume{
		Name: SATokenVolumeName,
		VolumeSource: corev1.VolumeSource{
			Secret: &corev1.SecretVolumeSource{
				SecretName:  tokenSecret,
				DefaultMode: ptr.To[int32](0o644),
			},
		},
	})
	mount := corev1.VolumeMount{Name: SATokenVolumeName, ReadOnly: true, MountPath: SATokenMountPath}
	addMount := func(mounts []corev1.VolumeMount) []corev1.VolumeMount {
		for _, m := range mounts {
			if m.MountPath == SATokenMountPath {
				return mounts
			}
		}
		return append(mounts, mount)
	}
	for i := range spec.Containers {
		spec.Containers[i].VolumeMounts = addMount(spec.Containers[i].VolumeMounts)
	}
	for i := range spec.InitContainers {
		spec.InitContainers[i].VolumeMounts = addMount(spec.InitContainers[i].VolumeMounts)
	}
	for i := range spec.EphemeralContainers {
		spec.EphemeralContainers[i].VolumeMounts = addMount(spec.EphemeralContainers[i].VolumeMounts)
	}
}

// kubernetesEnv kubelet 会为 default/kubernetes 注入的那组变量，这里显式写死为虚拟集群的地址。
func kubernetesEnv(ip string) []corev1.EnvVar {
	addr := "tcp://" + ip + ":" + kubernetesServicePort
	return []corev1.EnvVar{
		{Name: "KUBERNETES_SERVICE_HOST", Value: ip},
		{Name: "KUBERNETES_SERVICE_PORT", Value: kubernetesServicePort},
		{Name: "KUBERNETES_SERVICE_PORT_HTTPS", Value: kubernetesServicePort},
		{Name: "KUBERNETES_PORT", Value: addr},
		{Name: "KUBERNETES_PORT_443_TCP", Value: addr},
		{Name: "KUBERNETES_PORT_443_TCP_PROTO", Value: "tcp"},
		{Name: "KUBERNETES_PORT_443_TCP_PORT", Value: kubernetesServicePort},
		{Name: "KUBERNETES_PORT_443_TCP_ADDR", Value: ip},
	}
}

// rewriteEnv 在用户 env 前插入 KUBERNETES_*（用户显式定义的同名变量优先），
// 并把 downwardAPI 的 metadata.name/namespace/uid 改为虚拟对象的字面量。
func rewriteEnv(userEnv, injected []corev1.EnvVar, vpod *corev1.Pod) []corev1.EnvVar {
	defined := make(map[string]struct{}, len(userEnv))
	for _, e := range userEnv {
		defined[e.Name] = struct{}{}
	}
	out := make([]corev1.EnvVar, 0, len(userEnv)+len(injected))
	for _, e := range injected {
		if _, ok := defined[e.Name]; !ok {
			out = append(out, e)
		}
	}
	for _, e := range userEnv {
		if e.ValueFrom != nil && e.ValueFrom.FieldRef != nil {
			switch e.ValueFrom.FieldRef.FieldPath {
			case "metadata.name":
				e = corev1.EnvVar{Name: e.Name, Value: vpod.Name}
			case "metadata.namespace":
				e = corev1.EnvVar{Name: e.Name, Value: vpod.Namespace}
			case "metadata.uid":
				e = corev1.EnvVar{Name: e.Name, Value: string(vpod.UID)}
			}
		}
		out = append(out, e)
	}
	return out
}

// applyDNS 7.3 第 5 步：Default/None 尊重用户；其余改为 None 指向虚拟 CoreDNS，
// 用户 dnsConfig 的额外项按 kubelet 规则合并在后面。对 hostNetwork Pod 同样生效。
func applyDNS(spec *corev1.PodSpec, vns, dnsIP string) error {
	switch spec.DNSPolicy {
	case corev1.DNSDefault, corev1.DNSNone:
		return nil
	}
	if dnsIP == "" {
		return &NotReadyError{What: "virtual kube-system/kube-dns ClusterIP"}
	}
	user := spec.DNSConfig
	cfg := &corev1.PodDNSConfig{
		Nameservers: []string{dnsIP},
		Searches: []string{
			vns + ".svc." + clusterDomain,
			"svc." + clusterDomain,
			clusterDomain,
		},
	}
	ndotsSet := false
	if user != nil {
		for _, ns := range user.Nameservers {
			if ns != dnsIP && len(cfg.Nameservers) < 3 {
				cfg.Nameservers = append(cfg.Nameservers, ns)
			}
		}
		for _, s := range user.Searches {
			if !containsString(cfg.Searches, s) {
				cfg.Searches = append(cfg.Searches, s)
			}
		}
		for _, o := range user.Options {
			if o.Name == "ndots" {
				ndotsSet = true
			}
			cfg.Options = append(cfg.Options, *o.DeepCopy())
		}
	}
	if !ndotsSet {
		cfg.Options = append([]corev1.PodDNSConfigOption{{Name: "ndots", Value: ptr.To("5")}}, cfg.Options...)
	}
	spec.DNSPolicy = corev1.DNSNone
	spec.DNSConfig = cfg
	return nil
}

func containsString(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// ApplyPodMutations 把期望宿主 Pod 中可变的字段应用到现有宿主 Pod 上（Pod 绝大多数字段不可变）：
// labels/annotations、容器镜像、activeDeadlineSeconds（只能缩短）、tolerations（只能追加）。返回是否有改动。
func ApplyPodMutations(desired, actual *corev1.Pod) bool {
	changed := MergeMeta(&actual.ObjectMeta, desired.ObjectMeta)
	changed = applyImages(desired.Spec.Containers, actual.Spec.Containers) || changed
	changed = applyImages(desired.Spec.InitContainers, actual.Spec.InitContainers) || changed
	if desired.Spec.ActiveDeadlineSeconds != nil &&
		(actual.Spec.ActiveDeadlineSeconds == nil || *actual.Spec.ActiveDeadlineSeconds > *desired.Spec.ActiveDeadlineSeconds) {
		actual.Spec.ActiveDeadlineSeconds = ptr.To(*desired.Spec.ActiveDeadlineSeconds)
		changed = true
	}
	for _, t := range desired.Spec.Tolerations {
		if !hasToleration(actual.Spec.Tolerations, t) {
			actual.Spec.Tolerations = append(actual.Spec.Tolerations, t)
			changed = true
		}
	}
	return changed
}

func applyImages(desired, actual []corev1.Container) bool {
	changed := false
	for i := range actual {
		for j := range desired {
			if desired[j].Name == actual[i].Name && desired[j].Image != actual[i].Image {
				actual[i].Image = desired[j].Image
				changed = true
			}
		}
	}
	return changed
}

func hasToleration(list []corev1.Toleration, t corev1.Toleration) bool {
	for _, x := range list {
		if x.Key == t.Key && x.Operator == t.Operator && x.Value == t.Value && x.Effect == t.Effect &&
			ptr.Deref(x.TolerationSeconds, -1) == ptr.Deref(t.TolerationSeconds, -1) {
			return true
		}
	}
	return false
}

// HostPodEverExisted 从虚拟 Pod 的 status 判断其宿主 Pod 是否曾经存在过（status 只会由 syncer 从宿主回写）。
// 用于区分「宿主 Pod 还没建」与「宿主 Pod 被外力删除」。
func HostPodEverExisted(vpod *corev1.Pod) bool {
	st := vpod.Status
	return len(st.Conditions) > 0 || st.HostIP != "" || st.PodIP != "" || st.StartTime != nil ||
		len(st.ContainerStatuses) > 0 || len(st.InitContainerStatuses) > 0 ||
		st.Phase == corev1.PodRunning || st.Phase == corev1.PodSucceeded || st.Phase == corev1.PodFailed
}

// IsTerminal Pod 是否已到终态。
func IsTerminal(pod *corev1.Pod) bool {
	return pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed
}

// EvictionGracePeriod 节点不再被允许时驱逐 Pod 所用的 gracePeriodSeconds：取 Pod 自身的
// terminationGracePeriodSeconds 与 names.PlacementEvictionMaxGracePeriodSeconds 的较小值，未设置时取上限。
func EvictionGracePeriod(pod *corev1.Pod) int64 {
	limit := names.PlacementEvictionMaxGracePeriodSeconds
	if pod.Spec.TerminationGracePeriodSeconds == nil || *pod.Spec.TerminationGracePeriodSeconds > limit {
		return limit
	}
	return *pod.Spec.TerminationGracePeriodSeconds
}

// Evictable Pod 是否应被驱逐：尚未在删除中且未到终态（终态 Pod 不占节点，删掉反而会让 Job 重跑）。
func Evictable(pod *corev1.Pod) bool {
	return pod.DeletionTimestamp == nil && !IsTerminal(pod)
}
