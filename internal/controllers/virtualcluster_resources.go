package controllers

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"vk8s.io/vk8s/api/v1alpha1"
	"vk8s.io/vk8s/internal/names"
)

const (
	labelPSAEnforce = "pod-security.kubernetes.io/enforce"
	labelPSAWarn    = "pod-security.kubernetes.io/warn"
	labelPSAAudit   = "pod-security.kubernetes.io/audit"
	labelNSName     = "kubernetes.io/metadata.name"

	resourceQuotaName = names.HostResourceQuotaName
	limitRangeName    = names.LimitRangeName
	networkPolicyName = "vk8s-isolation"

	volumeData  = "data"
	volumeCerts = "certs"
	volumeRun   = "run"
	volumeTmp   = "tmp"

	// 控制平面 pod 运行身份；与 distroless nonroot 镜像一致。
	runAsUser int64 = 65532

	// 控制平面 pod 中额外的 pod 数（控制平面自身 + CoreDNS）与 Service 数（vc-<name>）。
	quotaExtraPods     int64 = 2
	quotaExtraServices int64 = 1
)

// syncerResources syncer 容器的固定资源，计入 ResourceQuota。
func syncerResources() corev1.ResourceRequirements {
	return corev1.ResourceRequirements{
		Requests: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("100m"),
			corev1.ResourceMemory: resource.MustParse("128Mi"),
		},
		Limits: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("1"),
			corev1.ResourceMemory: resource.MustParse("1Gi"),
		},
	}
}

// controlPlaneResources 控制平面容器资源：spec 优先，缺省取平台默认。
func (r *VirtualClusterReconciler) controlPlaneResources(vc *v1alpha1.VirtualCluster) corev1.ResourceRequirements {
	res := vc.Spec.ControlPlane.Resources.DeepCopy()
	def := r.Config.Defaults.ControlPlane.Resources
	if res.Requests == nil {
		res.Requests = def.Requests.DeepCopy()
	}
	if res.Limits == nil {
		res.Limits = def.Limits.DeepCopy()
	}
	return *res
}

// storageSize 控制平面 PVC 大小：spec 优先，缺省取平台默认。
func (r *VirtualClusterReconciler) storageSize(vc *v1alpha1.VirtualCluster) resource.Quantity {
	if !vc.Spec.ControlPlane.StorageSize.IsZero() {
		return vc.Spec.ControlPlane.StorageSize
	}
	return r.Config.Defaults.ControlPlane.StorageSize
}

// storageClassName 控制平面 PVC 的 StorageClass；空表示宿主默认。
func (r *VirtualClusterReconciler) storageClassName(vc *v1alpha1.VirtualCluster) string {
	if vc.Spec.ControlPlane.StorageClassName != "" {
		return vc.Spec.ControlPlane.StorageClassName
	}
	return r.Config.Defaults.ControlPlane.StorageClassName
}

// ---- 3. Namespace ----

func (r *VirtualClusterReconciler) ensureNamespace(ctx context.Context, vc *v1alpha1.VirtualCluster) error {
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: names.HostNamespace(vc.Name)}}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, ns, func() error {
		if !ns.CreationTimestamp.IsZero() && ns.Labels[names.LabelVCluster] != "" && ns.Labels[names.LabelVCluster] != vc.Name {
			return fmt.Errorf("namespace %s belongs to another virtual cluster %q", ns.Name, ns.Labels[names.LabelVCluster])
		}
		if !ns.DeletionTimestamp.IsZero() {
			return fmt.Errorf("namespace %s is terminating", ns.Name)
		}
		mergeLabels(ns, commonLabels(vc))
		mergeLabels(ns, map[string]string{
			names.LabelOwner: vc.Spec.Owner,
			labelPSAEnforce:  string(EffectivePodSecurity(r.Config, vc)),
			labelPSAWarn:     string(v1alpha1.PodSecurityRestricted),
			labelPSAAudit:    string(v1alpha1.PodSecurityRestricted),
		})
		return r.setOwner(vc, ns)
	})
	return err
}

// ---- 4. ResourceQuota ----

func (r *VirtualClusterReconciler) ensureResourceQuota(ctx context.Context, vc *v1alpha1.VirtualCluster) error {
	rq := &corev1.ResourceQuota{ObjectMeta: metav1.ObjectMeta{Name: resourceQuotaName, Namespace: names.HostNamespace(vc.Name)}}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, rq, func() error {
		mergeLabels(rq, commonLabels(vc))
		rq.Spec.Hard = r.quotaHard(vc)
		// syncer 的配额 webhook 据此把 hard / used 换算成用户视角（扣掉控制平面开销）
		overhead, err := json.Marshal(r.quotaOverhead(vc))
		if err != nil {
			return err
		}
		if rq.Annotations == nil {
			rq.Annotations = map[string]string{}
		}
		rq.Annotations[names.AnnotationQuotaOverhead] = string(overhead)
		return r.setOwner(vc, rq)
	})
	return err
}

// quotaOverhead hard 里除用户配额之外的部分：控制平面与 syncer 容器的 requests、控制平面与 CoreDNS 两个 Pod、
// 对齐用的 Service 一个、控制平面 PVC 的容量。键与 hard 相同（requests.cpu / requests.memory / pods / services / requests.storage），
// 为零的项不写；扩展资源没有开销。
func (r *VirtualClusterReconciler) quotaOverhead(vc *v1alpha1.VirtualCluster) corev1.ResourceList {
	overhead := corev1.ResourceList{}
	for _, rl := range []corev1.ResourceList{r.controlPlaneResources(vc).Requests, syncerResources().Requests} {
		for name, qty := range rl {
			key := corev1.ResourceName("requests." + string(name))
			cur := overhead[key]
			cur.Add(qty)
			overhead[key] = cur
		}
	}
	overhead[corev1.ResourcePods] = *resource.NewQuantity(quotaExtraPods, resource.DecimalSI)
	overhead[corev1.ResourceServices] = *resource.NewQuantity(quotaExtraServices, resource.DecimalSI)
	if s := r.storageSize(vc); !s.IsZero() {
		overhead[corev1.ResourceRequestsStorage] = s.DeepCopy()
	}
	for name, qty := range overhead {
		if qty.IsZero() {
			delete(overhead, name)
		}
	}
	return overhead
}

// quotaHard spec.quota + 控制平面开销（quotaOverhead）。为零的用户配额项不写（不限制）。
func (r *VirtualClusterReconciler) quotaHard(vc *v1alpha1.VirtualCluster) corev1.ResourceList {
	q := vc.Spec.Quota
	overhead := r.quotaOverhead(vc)
	hard := corev1.ResourceList{}
	add := func(name corev1.ResourceName, user resource.Quantity) {
		v := user.DeepCopy()
		if o, ok := overhead[name]; ok {
			v.Add(o)
		}
		hard[name] = v
	}
	if !q.CPU.IsZero() {
		add(corev1.ResourceRequestsCPU, q.CPU)
	}
	if !q.Memory.IsZero() {
		add(corev1.ResourceRequestsMemory, q.Memory)
	}
	if q.Pods > 0 {
		add(corev1.ResourcePods, *resource.NewQuantity(int64(q.Pods), resource.DecimalSI))
	}
	if !q.Storage.IsZero() {
		add(corev1.ResourceRequestsStorage, q.Storage)
	}
	if q.Services > 0 {
		add(corev1.ResourceServices, *resource.NewQuantity(int64(q.Services), resource.DecimalSI))
	}
	for name, qty := range q.Extended {
		hard[corev1.ResourceName("requests."+name)] = qty.DeepCopy()
	}
	return hard
}

// ---- 5. LimitRange ----

func (r *VirtualClusterReconciler) ensureLimitRange(ctx context.Context, vc *v1alpha1.VirtualCluster) error {
	lr := &corev1.LimitRange{ObjectMeta: metav1.ObjectMeta{Name: limitRangeName, Namespace: names.HostNamespace(vc.Name)}}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, lr, func() error {
		mergeLabels(lr, commonLabels(vc))
		lr.Spec.Limits = []corev1.LimitRangeItem{{
			Type:           corev1.LimitTypeContainer,
			DefaultRequest: r.Config.LimitRange.DefaultRequest.DeepCopy(),
			Default:        r.Config.LimitRange.Default.DeepCopy(),
		}}
		return r.setOwner(vc, lr)
	})
	return err
}

// ---- 6. NetworkPolicy ----

func (r *VirtualClusterReconciler) ensureNetworkPolicy(ctx context.Context, vc *v1alpha1.VirtualCluster) error {
	np := &networkingv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{Name: networkPolicyName, Namespace: names.HostNamespace(vc.Name)}}
	if EffectivePodSecurity(r.Config, vc) == v1alpha1.PodSecurityPrivileged {
		// privileged 不下发：hostNetwork Pod 的源 IP 是节点 IP，策略既约束不了也会误伤。
		err := r.Delete(ctx, np)
		if err != nil && !apierrors.IsNotFound(err) {
			return err
		}
		return nil
	}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, np, func() error {
		mergeLabels(np, commonLabels(vc))
		np.Spec = r.networkPolicySpec()
		return r.setOwner(vc, np)
	})
	return err
}

func (r *VirtualClusterReconciler) networkPolicySpec() networkingv1.NetworkPolicySpec {
	tcp := ptr.To(corev1.ProtocolTCP)
	udp := ptr.To(corev1.ProtocolUDP)
	nsSelector := func(name string) networkingv1.NetworkPolicyPeer {
		return networkingv1.NetworkPolicyPeer{
			NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{labelNSName: name}},
		}
	}
	sameNS := networkingv1.NetworkPolicyPeer{PodSelector: &metav1.LabelSelector{}}

	ingress := []networkingv1.NetworkPolicyIngressRule{
		{From: []networkingv1.NetworkPolicyPeer{sameNS}},
		// vk8s-system：gateway → 6443，manager 修复用 admin kubeconfig。
		{From: []networkingv1.NetworkPolicyPeer{nsSelector(r.Config.SystemNamespace)}},
	}
	if len(r.Config.IngressNamespaceSelector) > 0 {
		ingress = append(ingress, networkingv1.NetworkPolicyIngressRule{
			From: []networkingv1.NetworkPolicyPeer{{
				NamespaceSelector: &metav1.LabelSelector{MatchLabels: r.Config.IngressNamespaceSelector},
			}},
		})
	}

	external := networkingv1.IPBlock{CIDR: "0.0.0.0/0"}
	if r.Config.HostPodCIDR != "" {
		external.Except = []string{r.Config.HostPodCIDR}
	}
	egress := []networkingv1.NetworkPolicyEgressRule{
		{To: []networkingv1.NetworkPolicyPeer{sameNS}},
		{
			To: []networkingv1.NetworkPolicyPeer{nsSelector("kube-system")},
			Ports: []networkingv1.NetworkPolicyPort{
				{Protocol: udp, Port: ptr.To(intstr.FromInt32(53))},
				{Protocol: tcp, Port: ptr.To(intstr.FromInt32(53))},
			},
		},
		{
			To:    []networkingv1.NetworkPolicyPeer{nsSelector(r.Config.SystemNamespace)},
			Ports: []networkingv1.NetworkPolicyPort{{Protocol: tcp, Port: ptr.To(intstr.FromInt32(names.ManagerTLSPort))}},
		},
		{To: []networkingv1.NetworkPolicyPeer{{IPBlock: &external}}},
	}
	return networkingv1.NetworkPolicySpec{
		PodSelector: metav1.LabelSelector{},
		PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress, networkingv1.PolicyTypeEgress},
		Ingress:     ingress,
		Egress:      egress,
	}
}

// ---- 7. Service ----

func (r *VirtualClusterReconciler) ensureService(ctx context.Context, vc *v1alpha1.VirtualCluster) (string, error) {
	svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: names.ServiceName(vc.Name), Namespace: names.HostNamespace(vc.Name)}}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, svc, func() error {
		mergeLabels(svc, commonLabels(vc))
		svc.Spec.Type = corev1.ServiceTypeClusterIP
		svc.Spec.Selector = podSelectorLabels(vc)
		svc.Spec.Ports = []corev1.ServicePort{{
			Name:       "https",
			Protocol:   corev1.ProtocolTCP,
			Port:       names.ServicePort,
			TargetPort: intstr.FromInt32(names.APIServerPort),
		}}
		return r.setOwner(vc, svc)
	})
	if err != nil {
		return "", err
	}
	if svc.Spec.ClusterIP == "" || svc.Spec.ClusterIP == corev1.ClusterIPNone {
		return "", fmt.Errorf("service %s/%s has no ClusterIP yet", svc.Namespace, svc.Name)
	}
	return svc.Spec.ClusterIP, nil
}

// ---- 泛证书副本 ----

func (r *VirtualClusterReconciler) ensureAppsTLS(ctx context.Context, vc *v1alpha1.VirtualCluster) error {
	if r.Config.AppsTLSSecret == "" {
		return nil
	}
	srcNS, srcName := r.Config.AppsTLSSecret.Split()
	if srcNS == "" {
		srcNS = r.Config.SystemNamespace
	}
	src := &corev1.Secret{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: srcNS, Name: srcName}, src); err != nil {
		return fmt.Errorf("read appsTLSSecret %s/%s: %w", srcNS, srcName, err)
	}
	dst := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: names.AppsTLSSecretName, Namespace: names.HostNamespace(vc.Name)}}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, dst, func() error {
		mergeLabels(dst, commonLabels(vc))
		if dst.CreationTimestamp.IsZero() {
			dst.Type = src.Type
		}
		dst.Data = map[string][]byte{}
		for k, v := range src.Data {
			dst.Data[k] = append([]byte(nil), v...)
		}
		return r.setOwner(vc, dst)
	})
	return err
}

// ---- 9. syncer RBAC ----

func (r *VirtualClusterReconciler) ensureRBAC(ctx context.Context, vc *v1alpha1.VirtualCluster) error {
	hostNS := names.HostNamespace(vc.Name)
	saName := names.SyncerServiceAccount(vc.Name)

	sa := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: saName, Namespace: hostNS}}
	if _, err := controllerutil.CreateOrUpdate(ctx, r.Client, sa, func() error {
		mergeLabels(sa, commonLabels(vc))
		return r.setOwner(vc, sa)
	}); err != nil {
		return fmt.Errorf("serviceaccount: %w", err)
	}

	all := []string{"get", "list", "watch", "create", "update", "patch", "delete", "deletecollection"}
	readOnly := []string{"get", "list", "watch"}

	role := &rbacv1.Role{ObjectMeta: metav1.ObjectMeta{Name: saName, Namespace: hostNS}}
	if _, err := controllerutil.CreateOrUpdate(ctx, r.Client, role, func() error {
		mergeLabels(role, commonLabels(vc))
		role.Rules = []rbacv1.PolicyRule{
			{
				APIGroups: []string{""},
				Resources: []string{"pods", "pods/status", "services", "services/status", "endpoints", "configmaps", "secrets", "persistentvolumeclaims", "persistentvolumeclaims/status", "events"},
				Verbs:     all,
			},
			{APIGroups: []string{""}, Resources: []string{"pods/log"}, Verbs: []string{"get", "list"}},
			{APIGroups: []string{""}, Resources: []string{"pods/exec", "pods/attach", "pods/portforward"}, Verbs: []string{"get", "create"}},
			// kubectl debug：ephemeral 容器只能经子资源追加
			{APIGroups: []string{""}, Resources: []string{"pods/ephemeralcontainers"}, Verbs: []string{"get", "update", "patch"}},
			// 镜像到每个虚拟 namespace
			{APIGroups: []string{""}, Resources: []string{"limitranges"}, Verbs: readOnly},
			// 配额 webhook 读 hard / used
			{APIGroups: []string{""}, Resources: []string{"resourcequotas"}, Verbs: readOnly},
			{APIGroups: []string{"discovery.k8s.io"}, Resources: []string{"endpointslices"}, Verbs: all},
			{APIGroups: []string{"networking.k8s.io"}, Resources: []string{"ingresses", "ingresses/status"}, Verbs: all},
			{APIGroups: []string{"metrics.k8s.io"}, Resources: []string{"pods"}, Verbs: []string{"get", "list"}},
		}
		return r.setOwner(vc, role)
	}); err != nil {
		return fmt.Errorf("role: %w", err)
	}

	rb := &rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Name: saName, Namespace: hostNS}}
	if _, err := controllerutil.CreateOrUpdate(ctx, r.Client, rb, func() error {
		mergeLabels(rb, commonLabels(vc))
		if rb.CreationTimestamp.IsZero() {
			// roleRef 不可变。
			rb.RoleRef = rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: saName}
		}
		rb.Subjects = []rbacv1.Subject{{Kind: rbacv1.ServiceAccountKind, Name: saName, Namespace: hostNS}}
		return r.setOwner(vc, rb)
	}); err != nil {
		return fmt.Errorf("rolebinding: %w", err)
	}

	crName := names.SyncerClusterRole(vc.Name)
	cr := &rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: crName}}
	if _, err := controllerutil.CreateOrUpdate(ctx, r.Client, cr, func() error {
		mergeLabels(cr, commonLabels(vc))
		cr.Rules = []rbacv1.PolicyRule{
			{APIGroups: []string{""}, Resources: []string{"nodes"}, Verbs: readOnly},
			{APIGroups: []string{"storage.k8s.io"}, Resources: []string{"storageclasses"}, Verbs: readOnly},
			{APIGroups: []string{"networking.k8s.io"}, Resources: []string{"ingressclasses"}, Verbs: readOnly},
			{APIGroups: []string{"node.k8s.io"}, Resources: []string{"runtimeclasses"}, Verbs: readOnly},
			{APIGroups: []string{"metrics.k8s.io"}, Resources: []string{"nodes"}, Verbs: []string{"get", "list"}},
			// syncer 读取自己的 nodePlacement；list/watch 需带 fieldSelector metadata.name=<vc>。
			{APIGroups: []string{v1alpha1.GroupVersion.Group}, Resources: []string{"virtualclusters"}, ResourceNames: []string{vc.Name}, Verbs: readOnly},
		}
		return r.setOwner(vc, cr)
	}); err != nil {
		return fmt.Errorf("clusterrole: %w", err)
	}

	crb := &rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: crName}}
	if _, err := controllerutil.CreateOrUpdate(ctx, r.Client, crb, func() error {
		mergeLabels(crb, commonLabels(vc))
		if crb.CreationTimestamp.IsZero() {
			crb.RoleRef = rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: crName}
		}
		crb.Subjects = []rbacv1.Subject{{Kind: rbacv1.ServiceAccountKind, Name: saName, Namespace: hostNS}}
		return r.setOwner(vc, crb)
	}); err != nil {
		return fmt.Errorf("clusterrolebinding: %w", err)
	}
	return nil
}

// ---- 10. StatefulSet ----

func (r *VirtualClusterReconciler) ensureStatefulSet(ctx context.Context, vc *v1alpha1.VirtualCluster, certsHash string) (*appsv1.StatefulSet, error) {
	hostNS := names.HostNamespace(vc.Name)
	sts := &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: names.StatefulSetName(vc.Name), Namespace: hostNS}}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, sts, func() error {
		mergeLabels(sts, commonLabels(vc))
		creating := sts.CreationTimestamp.IsZero()
		if creating {
			// selector / serviceName / volumeClaimTemplates 创建后不可变。
			sts.Spec.Selector = &metav1.LabelSelector{MatchLabels: podSelectorLabels(vc)}
			sts.Spec.ServiceName = names.ServiceName(vc.Name)
			sts.Spec.VolumeClaimTemplates = []corev1.PersistentVolumeClaim{r.dataClaimTemplate(vc)}
		}
		sts.Spec.Replicas = ptr.To[int32](1)
		sts.Spec.PodManagementPolicy = appsv1.OrderedReadyPodManagement
		sts.Spec.UpdateStrategy = appsv1.StatefulSetUpdateStrategy{
			Type:          appsv1.RollingUpdateStatefulSetStrategyType,
			RollingUpdate: &appsv1.RollingUpdateStatefulSetStrategy{Partition: ptr.To[int32](0)},
		}
		sts.Spec.RevisionHistoryLimit = ptr.To[int32](5)
		sts.Spec.PersistentVolumeClaimRetentionPolicy = &appsv1.StatefulSetPersistentVolumeClaimRetentionPolicy{
			WhenDeleted: appsv1.RetainPersistentVolumeClaimRetentionPolicyType,
			WhenScaled:  appsv1.RetainPersistentVolumeClaimRetentionPolicyType,
		}
		sts.Spec.Template = r.podTemplate(vc, certsHash)
		return r.setOwner(vc, sts)
	})
	if err != nil {
		return nil, err
	}
	return sts, nil
}

func (r *VirtualClusterReconciler) dataClaimTemplate(vc *v1alpha1.VirtualCluster) corev1.PersistentVolumeClaim {
	pvc := corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: volumeData, Labels: commonLabels(vc)},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceStorage: r.storageSize(vc)},
			},
		},
	}
	if sc := r.storageClassName(vc); sc != "" {
		pvc.Spec.StorageClassName = ptr.To(sc)
	}
	return pvc
}

// podTemplate 两个容器的 pod 模板。容器参数是与 control-plane / syncer 子命令的契约，勿改。
func (r *VirtualClusterReconciler) podTemplate(vc *v1alpha1.VirtualCluster, certsHash string) corev1.PodTemplateSpec {
	hostNS := names.HostNamespace(vc.Name)
	labels := commonLabels(vc)
	for k, v := range podSelectorLabels(vc) {
		labels[k] = v
	}

	appsTLS := ""
	if r.Config.AppsTLSSecret != "" {
		appsTLS = names.AppsTLSSecretName
	}

	containerSC := &corev1.SecurityContext{
		AllowPrivilegeEscalation: ptr.To(false),
		Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
		RunAsNonRoot:             ptr.To(true),
		RunAsUser:                ptr.To(runAsUser),
		ReadOnlyRootFilesystem:   ptr.To(true),
		SeccompProfile:           &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
	}
	podIPEnv := corev1.EnvVar{
		Name:      "POD_IP",
		ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{APIVersion: "v1", FieldPath: "status.podIP"}},
	}
	podNameEnv := corev1.EnvVar{
		Name:      "POD_NAME",
		ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{APIVersion: "v1", FieldPath: "metadata.name"}},
	}
	probe := func(path string, port int32, scheme corev1.URIScheme) *corev1.Probe {
		return &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				HTTPGet: &corev1.HTTPGetAction{Path: path, Port: intstr.FromInt32(port), Scheme: scheme},
			},
			InitialDelaySeconds: 5,
			PeriodSeconds:       10,
			TimeoutSeconds:      5,
			SuccessThreshold:    1,
			FailureThreshold:    3,
		}
	}
	startup := func(path string, port int32, scheme corev1.URIScheme) *corev1.Probe {
		p := probe(path, port, scheme)
		p.InitialDelaySeconds = 0
		p.PeriodSeconds = 5
		p.FailureThreshold = 60 // 最多等 5 分钟，首次启动 sqlite 初始化与 apiserver 启动较慢
		return p
	}

	controlPlane := corev1.Container{
		Name:            names.ContainerControlPlane,
		Image:           r.Config.Image,
		ImagePullPolicy: corev1.PullIfNotPresent,
		Args: append([]string{
			"control-plane",
			"--vcluster-name=" + vc.Name,
			"--certs-dir=" + names.CertsDir,
			"--data-dir=" + names.DataDir,
			"--run-dir=" + names.RunDir,
			"--service-cluster-ip-range=" + r.Config.HostServiceCIDR,
		}, auditControlPlaneArgs(r.Config.Audit.Enabled)...),
		Env:   []corev1.EnvVar{podIPEnv},
		Ports: []corev1.ContainerPort{{Name: "https", ContainerPort: names.APIServerPort, Protocol: corev1.ProtocolTCP}},
		VolumeMounts: []corev1.VolumeMount{
			{Name: volumeCerts, MountPath: names.CertsDir, ReadOnly: true},
			{Name: volumeData, MountPath: names.DataDir},
			{Name: volumeRun, MountPath: names.RunDir},
			{Name: volumeTmp, MountPath: "/tmp"},
		},
		Resources:                r.controlPlaneResources(vc),
		StartupProbe:             startup("/readyz", names.APIServerPort, corev1.URISchemeHTTPS),
		ReadinessProbe:           probe("/readyz", names.APIServerPort, corev1.URISchemeHTTPS),
		LivenessProbe:            probe("/livez", names.APIServerPort, corev1.URISchemeHTTPS),
		SecurityContext:          containerSC.DeepCopy(),
		TerminationMessagePath:   corev1.TerminationMessagePathDefault,
		TerminationMessagePolicy: corev1.TerminationMessageReadFile,
	}

	syncer := corev1.Container{
		Name:            names.ContainerSyncer,
		Image:           r.Config.Image,
		ImagePullPolicy: corev1.PullIfNotPresent,
		Args: []string{
			"syncer",
			"--vcluster-name=" + vc.Name,
			"--host-namespace=" + hostNS,
			"--certs-dir=" + names.CertsDir,
			"--run-dir=" + names.RunDir,
			"--api-domain=" + r.Config.APIDomain,
			"--apps-domain=" + r.Config.AppsDomain,
			"--apps-tls-secret=" + appsTLS,
			"--host-dns-service-ip=" + r.Config.HostDNSServiceIP,
			fmt.Sprintf("--health-listen=:%d", names.SyncerHealthPort),
			fmt.Sprintf("--webhook-listen=:%d", names.SyncerWebhookPort),
			fmt.Sprintf("--metrics-api-listen=:%d", names.SyncerMetricsAPIPort),
			fmt.Sprintf("--manager-url=https://%s.%s.svc:%d", ManagerServiceName, r.Config.SystemNamespace, names.ManagerTLSPort),
		},
		Env: []corev1.EnvVar{podIPEnv, podNameEnv},
		Ports: []corev1.ContainerPort{
			{Name: "health", ContainerPort: names.SyncerHealthPort, Protocol: corev1.ProtocolTCP},
			{Name: "webhook", ContainerPort: names.SyncerWebhookPort, Protocol: corev1.ProtocolTCP},
			{Name: "metrics-api", ContainerPort: names.SyncerMetricsAPIPort, Protocol: corev1.ProtocolTCP},
		},
		VolumeMounts: []corev1.VolumeMount{
			{Name: volumeCerts, MountPath: names.CertsDir, ReadOnly: true},
			{Name: volumeRun, MountPath: names.RunDir},
			{Name: volumeTmp, MountPath: "/tmp"},
		},
		Resources:                syncerResources(),
		StartupProbe:             startup("/readyz", names.SyncerHealthPort, corev1.URISchemeHTTP),
		ReadinessProbe:           probe("/readyz", names.SyncerHealthPort, corev1.URISchemeHTTP),
		SecurityContext:          containerSC.DeepCopy(),
		TerminationMessagePath:   corev1.TerminationMessagePathDefault,
		TerminationMessagePolicy: corev1.TerminationMessageReadFile,
	}

	return corev1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{
			Labels:      labels,
			Annotations: map[string]string{AnnotationCertsHash: certsHash},
		},
		Spec: corev1.PodSpec{
			ServiceAccountName:           names.SyncerServiceAccount(vc.Name),
			AutomountServiceAccountToken: ptr.To(true),
			RestartPolicy:                corev1.RestartPolicyAlways,
			DNSPolicy:                    corev1.DNSClusterFirst,
			SchedulerName:                corev1.DefaultSchedulerName,
			// apiserver 优雅退出最坏情况 60s（--request-timeout），control-plane 进程自身等 70s，这里再留余量。
			TerminationGracePeriodSeconds: ptr.To(names.ControlPlaneTerminationGracePeriodSeconds),
			EnableServiceLinks:            ptr.To(false),
			NodeSelector:                  copyMap(r.Config.ControlPlanePlacement.NodeSelector),
			Tolerations:                   copyTolerations(r.Config.ControlPlanePlacement.Tolerations),
			SecurityContext: &corev1.PodSecurityContext{
				RunAsNonRoot:   ptr.To(true),
				RunAsUser:      ptr.To(runAsUser),
				RunAsGroup:     ptr.To(runAsUser),
				FSGroup:        ptr.To(runAsUser),
				SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
			},
			Containers: []corev1.Container{controlPlane, syncer},
			Volumes: []corev1.Volume{
				{
					Name: volumeCerts,
					VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{
						SecretName:  names.CertsSecretName(vc.Name),
						DefaultMode: ptr.To[int32](0o440),
					}},
				},
				{Name: volumeRun, VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
				{Name: volumeTmp, VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
			},
		},
	}
}

func copyMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func copyTolerations(in []corev1.Toleration) []corev1.Toleration {
	if len(in) == 0 {
		return nil
	}
	out := make([]corev1.Toleration, len(in))
	for i := range in {
		in[i].DeepCopyInto(&out[i])
	}
	return out
}

// auditControlPlaneArgs 审计启用时让控制平面给 apiserver 加审计参数。
func auditControlPlaneArgs(enabled bool) []string {
	if !enabled {
		return nil
	}
	return []string{"--audit-webhook-kubeconfig=" + filepath.Join(names.CertsDir, names.KeyAuditWebhookKubeconfig)}
}
