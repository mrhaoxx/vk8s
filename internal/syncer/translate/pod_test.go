package translate

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	"vk8s.io/vk8s/api/v1alpha1"
	"vk8s.io/vk8s/internal/names"
	"vk8s.io/vk8s/internal/syncer/placement"
)

func testEnv() PodEnv {
	return PodEnv{
		HostNamespace:       "vc-e2e",
		KubernetesServiceIP: "10.96.5.5",
		DNSServiceIP:        "10.96.7.7",
		NodeAllowed:         func(n string) bool { return n == "node-ok" },
	}
}

func basePod() *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "demo", UID: "uid-web"},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "c", Image: "nginx"}},
		},
	}
}

func mustTranslate(t *testing.T, pod *corev1.Pod, env PodEnv) *corev1.Pod {
	t.Helper()
	h, err := TranslatePod(pod, env)
	if err != nil {
		t.Fatalf("TranslatePod: %v", err)
	}
	return h
}

func envValue(c corev1.Container, name string) (string, bool) {
	for _, e := range c.Env {
		if e.Name == name {
			return e.Value, true
		}
	}
	return "", false
}

func TestTranslatePodBasics(t *testing.T) {
	h := mustTranslate(t, basePod(), testEnv())
	if h.Name != "web-x-demo" || h.Namespace != "vc-e2e" {
		t.Fatalf("name: %s/%s", h.Namespace, h.Name)
	}
	if h.Spec.ServiceAccountName != "default" || ptr.Deref(h.Spec.AutomountServiceAccountToken, true) {
		t.Fatalf("host SA must be default without automount: %+v", h.Spec)
	}
	if h.Spec.SchedulerName != DefaultSchedulerName || h.Spec.Hostname != "web" {
		t.Fatalf("scheduler/hostname: %s %s", h.Spec.SchedulerName, h.Spec.Hostname)
	}
	if ptr.Deref(h.Spec.EnableServiceLinks, true) {
		t.Fatalf("enableServiceLinks must be false")
	}
	if v, ok := envValue(h.Spec.Containers[0], "KUBERNETES_SERVICE_HOST"); !ok || v != "10.96.5.5" {
		t.Fatalf("KUBERNETES_SERVICE_HOST=%q", v)
	}
	if h.Spec.DNSPolicy != corev1.DNSNone || h.Spec.DNSConfig == nil || h.Spec.DNSConfig.Nameservers[0] != "10.96.7.7" {
		t.Fatalf("dns: %s %+v", h.Spec.DNSPolicy, h.Spec.DNSConfig)
	}
	if h.Spec.DNSConfig.Searches[0] != "demo.svc.cluster.local" {
		t.Fatalf("searches: %v", h.Spec.DNSConfig.Searches)
	}
	// SA token volume 注入并挂到容器
	var vol *corev1.Volume
	for i := range h.Spec.Volumes {
		if h.Spec.Volumes[i].Name == SATokenVolumeName {
			vol = &h.Spec.Volumes[i]
		}
	}
	if vol == nil || vol.Secret == nil || vol.Secret.SecretName != names.SATokenSecretName(h.Name) {
		t.Fatalf("sa token volume: %+v", vol)
	}
	found := false
	for _, m := range h.Spec.Containers[0].VolumeMounts {
		if m.MountPath == SATokenMountPath && m.ReadOnly {
			found = true
		}
	}
	if !found {
		t.Fatalf("sa token mount missing: %+v", h.Spec.Containers[0].VolumeMounts)
	}
}

func TestTranslatePodNoAutomount(t *testing.T) {
	p := basePod()
	p.Spec.AutomountServiceAccountToken = ptr.To(false)
	h := mustTranslate(t, p, testEnv())
	for _, v := range h.Spec.Volumes {
		if v.Name == SATokenVolumeName {
			t.Fatalf("token volume must not be injected when automount=false")
		}
	}
	if NeedsSAToken(p) {
		t.Fatalf("NeedsSAToken must be false")
	}
}

func TestTranslatePodProjectedTokenReplaced(t *testing.T) {
	p := basePod()
	p.Spec.AutomountServiceAccountToken = ptr.To(false)
	p.Spec.Volumes = []corev1.Volume{{Name: "tok", VolumeSource: corev1.VolumeSource{Projected: &corev1.ProjectedVolumeSource{
		Sources: []corev1.VolumeProjection{
			{ServiceAccountToken: &corev1.ServiceAccountTokenProjection{Path: "my-token", Audience: "x"}},
			{ConfigMap: &corev1.ConfigMapProjection{LocalObjectReference: corev1.LocalObjectReference{Name: "cm"}}},
		}}}}}
	if !NeedsSAToken(p) {
		t.Fatalf("projected serviceAccountToken needs a syncer token")
	}
	h := mustTranslate(t, p, testEnv())
	src := h.Spec.Volumes[0].Projected.Sources
	if src[0].ServiceAccountToken != nil || src[0].Secret == nil || src[0].Secret.Items[0].Path != "my-token" {
		t.Fatalf("projected token not replaced: %+v", src[0])
	}
	if src[0].Secret.Name != names.SATokenSecretName(h.Name) || src[1].ConfigMap.Name != "cm-x-demo" {
		t.Fatalf("names: %+v %+v", src[0].Secret, src[1].ConfigMap)
	}
}

func TestTranslatePodReferencesRenamed(t *testing.T) {
	p := basePod()
	p.Spec.ImagePullSecrets = []corev1.LocalObjectReference{{Name: "reg"}}
	p.Spec.Volumes = []corev1.Volume{
		{Name: "a", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: "cm"}}}},
		{Name: "b", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: "sec"}}},
		{Name: "c", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "pvc"}}},
	}
	p.Spec.Containers[0].Env = []corev1.EnvVar{
		{Name: "K", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "sec"}, Key: "k"}}},
		{Name: "C", ValueFrom: &corev1.EnvVarSource{ConfigMapKeyRef: &corev1.ConfigMapKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "cm"}, Key: "k"}}},
		{Name: "POD_NAME", ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.name"}}},
		{Name: "POD_NS", ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.namespace"}}},
		{Name: "POD_IP", ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: "status.podIP"}}},
		{Name: "KUBERNETES_SERVICE_HOST", Value: "user-wins"},
	}
	p.Spec.Containers[0].EnvFrom = []corev1.EnvFromSource{{SecretRef: &corev1.SecretEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "sec"}}}}
	h := mustTranslate(t, p, testEnv())
	if h.Spec.ImagePullSecrets[0].Name != "reg-x-demo" {
		t.Fatalf("imagePullSecrets: %v", h.Spec.ImagePullSecrets)
	}
	if h.Spec.Volumes[0].ConfigMap.Name != "cm-x-demo" || h.Spec.Volumes[1].Secret.SecretName != "sec-x-demo" ||
		h.Spec.Volumes[2].PersistentVolumeClaim.ClaimName != "pvc-x-demo" {
		t.Fatalf("volumes: %+v", h.Spec.Volumes)
	}
	c := h.Spec.Containers[0]
	byName := map[string]corev1.EnvVar{}
	for _, e := range c.Env {
		byName[e.Name] = e
	}
	if byName["K"].ValueFrom.SecretKeyRef.Name != "sec-x-demo" || byName["C"].ValueFrom.ConfigMapKeyRef.Name != "cm-x-demo" {
		t.Fatalf("env refs: %+v", c.Env)
	}
	if byName["POD_NAME"].Value != "web" || byName["POD_NS"].Value != "demo" || byName["POD_NAME"].ValueFrom != nil {
		t.Fatalf("downwardAPI literal: %+v", c.Env)
	}
	if byName["POD_IP"].ValueFrom == nil || byName["POD_IP"].ValueFrom.FieldRef.FieldPath != "status.podIP" {
		t.Fatalf("status.podIP must stay a fieldRef: %+v", byName["POD_IP"])
	}
	if byName["KUBERNETES_SERVICE_HOST"].Value != "user-wins" {
		t.Fatalf("user env must win over injected: %+v", byName["KUBERNETES_SERVICE_HOST"])
	}
	n := 0
	for _, e := range c.Env {
		if e.Name == "KUBERNETES_SERVICE_HOST" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("duplicate KUBERNETES_SERVICE_HOST")
	}
	if c.EnvFrom[0].SecretRef.Name != "sec-x-demo" {
		t.Fatalf("envFrom: %+v", c.EnvFrom)
	}
}

func TestTranslatePodPlacement(t *testing.T) {
	env := testEnv()
	env.Placement = placement.Effective{Static: v1alpha1.NodePlacement{
		NodeSelector: map[string]string{"pool": "a"},
		NodeNames:    []string{"n2", "n1"},
		Tolerations:  []corev1.Toleration{{Key: "pool", Operator: corev1.TolerationOpEqual, Value: "a", Effect: corev1.TaintEffectNoSchedule}},
	}}
	p := basePod()
	p.Spec.Tolerations = []corev1.Toleration{{Key: "user", Operator: corev1.TolerationOpExists}}
	p.Spec.PriorityClassName = "system-node-critical"
	p.Spec.Priority = ptr.To[int32](100)
	p.Spec.SchedulerName = "my-scheduler"
	p.Spec.Affinity = &corev1.Affinity{NodeAffinity: &corev1.NodeAffinity{
		RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{NodeSelectorTerms: []corev1.NodeSelectorTerm{
			{MatchExpressions: []corev1.NodeSelectorRequirement{{Key: "gpu", Operator: corev1.NodeSelectorOpExists}}},
			{MatchExpressions: []corev1.NodeSelectorRequirement{{Key: "zone", Operator: corev1.NodeSelectorOpIn, Values: []string{"z1"}}}},
		}}}}
	h := mustTranslate(t, p, env)

	terms := h.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms
	// 2 条用户 term × 2 个 nodeName = 4 条，每条都带 pool In [a]、bookable NotIn [true] 与 metadata.name In [<one>]
	if len(terms) != 4 {
		t.Fatalf("want 4 terms, got %d: %+v", len(terms), terms)
	}
	for _, term := range terms {
		if len(term.MatchExpressions) != 3 || term.MatchExpressions[1].Key != "pool" || term.MatchExpressions[1].Values[0] != "a" {
			t.Fatalf("placement nodeSelector not appended: %+v", term)
		}
		if !hasNotInBookable(term) {
			t.Fatalf("static term lacks bookable NotIn: %+v", term)
		}
		if len(term.MatchFields) != 1 || term.MatchFields[0].Key != "metadata.name" || len(term.MatchFields[0].Values) != 1 {
			t.Fatalf("matchFields: %+v", term.MatchFields)
		}
	}
	if terms[0].MatchFields[0].Values[0] != "n1" || terms[1].MatchFields[0].Values[0] != "n2" {
		t.Fatalf("nodeNames must be sorted and expanded: %+v", terms)
	}
	if len(h.Spec.Tolerations) != 1 || h.Spec.Tolerations[0].Key != "pool" {
		t.Fatalf("user tolerations must be replaced: %+v", h.Spec.Tolerations)
	}
	if h.Spec.PriorityClassName != "" || h.Spec.Priority != nil || h.Spec.SchedulerName != DefaultSchedulerName {
		t.Fatalf("priority/scheduler not reset: %+v", h.Spec)
	}

	// 没有静态 placement 时仍注入一条只含 bookable NotIn 的 term：默认池集群也不能落到可预约节点
	h2 := mustTranslate(t, basePod(), testEnv())
	t2 := h2.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms
	if len(t2) != 1 || len(t2[0].MatchExpressions) != 1 || !hasNotInBookable(t2[0]) || len(t2[0].MatchFields) != 0 {
		t.Fatalf("empty placement must inject exactly one NotIn-only term: %+v", t2)
	}
}

func hasNotInBookable(t corev1.NodeSelectorTerm) bool {
	for _, e := range t.MatchExpressions {
		if e.Key == names.LabelBookable && e.Operator == corev1.NodeSelectorOpNotIn && len(e.Values) == 1 && e.Values[0] == names.BookableTrue {
			return true
		}
	}
	return false
}

func TestTranslatePodPlacementExcludesBookableNodes(t *testing.T) {
	env := testEnv()
	env.Placement.Static = v1alpha1.NodePlacement{NodeSelector: map[string]string{"pool": "a"}}
	got := mustTranslate(t, basePod(), env)
	terms := got.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms
	if len(terms) == 0 {
		t.Fatal("no terms injected")
	}
	for i, tm := range terms {
		if !hasNotInBookable(tm) {
			t.Errorf("term %d lacks bookable NotIn: %+v", i, tm)
		}
	}
}

func TestTranslatePodLeasedNodeTerms(t *testing.T) {
	env := testEnv()
	env.Placement.Static = v1alpha1.NodePlacement{NodeSelector: map[string]string{"pool": "a"}}
	env.Placement.Leased = []string{"gpu-08", "gpu-07"}
	pod := basePod()
	pod.Spec.Affinity = &corev1.Affinity{NodeAffinity: &corev1.NodeAffinity{RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
		NodeSelectorTerms: []corev1.NodeSelectorTerm{{MatchExpressions: []corev1.NodeSelectorRequirement{{Key: "gpu", Operator: corev1.NodeSelectorOpExists}}}},
	}}}
	got := mustTranslate(t, pod, env)
	terms := got.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms
	if len(terms) != 3 {
		t.Fatalf("want 1 static + 2 lease terms, got %d: %+v", len(terms), terms)
	}
	var lease []corev1.NodeSelectorTerm
	for _, tm := range terms {
		if hasNotInBookable(tm) {
			continue
		}
		lease = append(lease, tm)
	}
	if len(lease) != 2 {
		t.Fatalf("want 2 lease terms, got %d", len(lease))
	}
	for _, tm := range lease {
		if len(tm.MatchFields) != 1 || tm.MatchFields[0].Key != "metadata.name" {
			t.Errorf("lease term matchFields = %+v", tm.MatchFields)
		}
		if len(tm.MatchExpressions) != 1 || tm.MatchExpressions[0].Key != "gpu" {
			t.Errorf("lease term must keep only the user's own expressions, got %+v", tm.MatchExpressions)
		}
	}
	if lease[0].MatchFields[0].Values[0] != "gpu-07" || lease[1].MatchFields[0].Values[0] != "gpu-08" {
		t.Errorf("lease terms must be sorted by node: %+v", lease)
	}
}

func TestTranslatePodNodeNameOnLeasedNode(t *testing.T) {
	env := testEnv()
	env.Placement.Leased = []string{"gpu-07"}
	env.NodeAllowed = func(n string) bool { return n == "gpu-07" }
	pod := basePod()
	pod.Spec.NodeName = "gpu-07"
	if _, err := TranslatePod(pod, env); err != nil {
		t.Fatalf("leased node must pass the nodeName whitelist: %v", err)
	}
}

func TestTranslatePodNodeNameWhitelist(t *testing.T) {
	p := basePod()
	p.Spec.NodeName = "node-ok"
	if _, err := TranslatePod(p, testEnv()); err != nil {
		t.Fatalf("allowed node rejected: %v", err)
	}
	p.Spec.NodeName = "node-other"
	_, err := TranslatePod(p, testEnv())
	if !IsReject(err) {
		t.Fatalf("want RejectError, got %v", err)
	}
	env := testEnv()
	env.NodeAllowed = nil
	p.Spec.NodeName = "node-ok"
	if _, err := TranslatePod(p, env); !IsReject(err) {
		t.Fatalf("nil NodeAllowed must reject any nodeName, got %v", err)
	}
}

func TestTranslatePodDNS(t *testing.T) {
	p := basePod()
	p.Spec.DNSPolicy = corev1.DNSDefault
	h := mustTranslate(t, p, testEnv())
	if h.Spec.DNSPolicy != corev1.DNSDefault || h.Spec.DNSConfig != nil {
		t.Fatalf("Default must be respected: %s %+v", h.Spec.DNSPolicy, h.Spec.DNSConfig)
	}

	p = basePod()
	p.Spec.HostNetwork = true
	p.Spec.DNSPolicy = corev1.DNSClusterFirstWithHostNet
	p.Spec.DNSConfig = &corev1.PodDNSConfig{Nameservers: []string{"1.1.1.1"}, Searches: []string{"corp.com"}, Options: []corev1.PodDNSConfigOption{{Name: "ndots", Value: ptr.To("2")}}}
	h = mustTranslate(t, p, testEnv())
	cfg := h.Spec.DNSConfig
	if h.Spec.DNSPolicy != corev1.DNSNone || cfg.Nameservers[0] != "10.96.7.7" || cfg.Nameservers[1] != "1.1.1.1" {
		t.Fatalf("hostNetwork dns: %+v", cfg)
	}
	if cfg.Searches[len(cfg.Searches)-1] != "corp.com" || len(cfg.Options) != 1 || *cfg.Options[0].Value != "2" {
		t.Fatalf("user searches/options not merged: %+v", cfg)
	}

	env := testEnv()
	env.DNSServiceIP = ""
	if _, err := TranslatePod(basePod(), env); !IsNotReady(err) {
		t.Fatalf("missing kube-dns IP must be NotReady, got %v", err)
	}
}

func TestTranslatePodAffinityScopedToNamespace(t *testing.T) {
	sel := func() *metav1.LabelSelector {
		return &metav1.LabelSelector{MatchLabels: map[string]string{"app": "web"}}
	}
	p := basePod()
	p.Spec.Affinity = &corev1.Affinity{
		PodAffinity: &corev1.PodAffinity{
			RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{
				{LabelSelector: sel(), TopologyKey: "kubernetes.io/hostname"},                           // 默认：本 ns
				{LabelSelector: sel(), TopologyKey: "zone", Namespaces: []string{"other", "demo"}},      // 显式 namespaces
				{LabelSelector: sel(), TopologyKey: "zone", NamespaceSelector: &metav1.LabelSelector{}}, // 全部 ns
				{LabelSelector: nil, TopologyKey: "zone"},                                               // nil selector 不动
			},
		},
		PodAntiAffinity: &corev1.PodAntiAffinity{
			PreferredDuringSchedulingIgnoredDuringExecution: []corev1.WeightedPodAffinityTerm{
				{Weight: 1, PodAffinityTerm: corev1.PodAffinityTerm{LabelSelector: sel(), TopologyKey: "zone"}},
			},
		},
	}
	p.Spec.TopologySpreadConstraints = []corev1.TopologySpreadConstraint{
		{MaxSkew: 1, TopologyKey: "zone", WhenUnsatisfiable: corev1.DoNotSchedule, LabelSelector: sel()},
	}
	h := mustTranslate(t, p, testEnv())

	vnsReq := func(sel *metav1.LabelSelector) *metav1.LabelSelectorRequirement {
		if sel == nil {
			return nil
		}
		for i := range sel.MatchExpressions {
			if sel.MatchExpressions[i].Key == names.LabelVNamespace {
				return &sel.MatchExpressions[i]
			}
		}
		return nil
	}
	req := h.Spec.Affinity.PodAffinity.RequiredDuringSchedulingIgnoredDuringExecution
	if r := vnsReq(req[0].LabelSelector); r == nil || len(r.Values) != 1 || r.Values[0] != "demo" {
		t.Fatalf("default term must be scoped to demo: %+v", req[0].LabelSelector)
	}
	if r := vnsReq(req[1].LabelSelector); r == nil || strings.Join(r.Values, ",") != "demo,other" || req[1].Namespaces != nil {
		t.Fatalf("explicit namespaces: %+v ns=%v", req[1].LabelSelector, req[1].Namespaces)
	}
	if r := vnsReq(req[2].LabelSelector); r != nil || req[2].NamespaceSelector != nil {
		t.Fatalf("empty namespaceSelector means all namespaces: %+v", req[2])
	}
	if req[3].LabelSelector != nil {
		t.Fatalf("nil labelSelector must stay nil")
	}
	pref := h.Spec.Affinity.PodAntiAffinity.PreferredDuringSchedulingIgnoredDuringExecution
	if r := vnsReq(pref[0].PodAffinityTerm.LabelSelector); r == nil || r.Values[0] != "demo" {
		t.Fatalf("preferred anti-affinity not scoped: %+v", pref[0])
	}
	if r := vnsReq(h.Spec.TopologySpreadConstraints[0].LabelSelector); r == nil || r.Values[0] != "demo" {
		t.Fatalf("topologySpread not scoped: %+v", h.Spec.TopologySpreadConstraints[0])
	}
	// 原对象不被修改
	if vnsReq(p.Spec.Affinity.PodAffinity.RequiredDuringSchedulingIgnoredDuringExecution[0].LabelSelector) != nil {
		t.Fatalf("virtual pod mutated")
	}

	// 非空 namespaceSelector 无法在宿主侧表达 → 拒绝
	p = basePod()
	p.Spec.Affinity = &corev1.Affinity{PodAffinity: &corev1.PodAffinity{
		RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{
			{LabelSelector: sel(), TopologyKey: "zone", NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"team": "a"}}},
		}}}
	if _, err := TranslatePod(p, testEnv()); !IsReject(err) {
		t.Fatalf("namespaceSelector must be rejected, got %v", err)
	}
}

func TestMissingEphemeralContainers(t *testing.T) {
	desired := &corev1.Pod{Spec: corev1.PodSpec{EphemeralContainers: []corev1.EphemeralContainer{
		{EphemeralContainerCommon: corev1.EphemeralContainerCommon{Name: "dbg"}},
		{EphemeralContainerCommon: corev1.EphemeralContainerCommon{Name: "dbg2"}},
	}}}
	actual := &corev1.Pod{Spec: corev1.PodSpec{EphemeralContainers: []corev1.EphemeralContainer{
		{EphemeralContainerCommon: corev1.EphemeralContainerCommon{Name: "dbg"}},
	}}}
	got := MissingEphemeralContainers(desired, actual)
	if len(got) != 1 || got[0].Name != "dbg2" {
		t.Fatalf("got %+v", got)
	}
	if MissingEphemeralContainers(actual, desired) != nil {
		t.Fatalf("nothing missing expected")
	}
	// 翻译后的 ephemeral 容器也带 KUBERNETES_* 与 SA token 挂载
	p := basePod()
	p.Spec.EphemeralContainers = []corev1.EphemeralContainer{{EphemeralContainerCommon: corev1.EphemeralContainerCommon{Name: "dbg", Image: "busybox"}}}
	h := mustTranslate(t, p, testEnv())
	ec := h.Spec.EphemeralContainers[0]
	if _, ok := envValue(corev1.Container{Env: ec.Env}, "KUBERNETES_SERVICE_HOST"); !ok {
		t.Fatalf("ephemeral env: %+v", ec.Env)
	}
	if len(ec.VolumeMounts) != 1 || ec.VolumeMounts[0].MountPath != SATokenMountPath {
		t.Fatalf("ephemeral mounts: %+v", ec.VolumeMounts)
	}
}

func TestApplyPodMutations(t *testing.T) {
	desired := basePod()
	desired.Spec.Containers[0].Image = "nginx:2"
	desired.Spec.ActiveDeadlineSeconds = ptr.To[int64](10)
	desired.Spec.Tolerations = []corev1.Toleration{{Key: "a", Operator: corev1.TolerationOpExists}}
	actual := basePod()
	actual.Spec.ActiveDeadlineSeconds = ptr.To[int64](100)
	actual.Spec.Tolerations = []corev1.Toleration{{Key: "node.kubernetes.io/not-ready", Operator: corev1.TolerationOpExists}}
	if !ApplyPodMutations(desired, actual) {
		t.Fatalf("expected change")
	}
	if actual.Spec.Containers[0].Image != "nginx:2" || *actual.Spec.ActiveDeadlineSeconds != 10 || len(actual.Spec.Tolerations) != 2 {
		t.Fatalf("mutations: %+v", actual.Spec)
	}
	if ApplyPodMutations(desired, actual) {
		t.Fatalf("second apply must be a no-op")
	}
	// activeDeadlineSeconds 只能缩短
	desired.Spec.ActiveDeadlineSeconds = ptr.To[int64](50)
	if ApplyPodMutations(desired, actual) || *actual.Spec.ActiveDeadlineSeconds != 10 {
		t.Fatalf("activeDeadlineSeconds must not grow")
	}
}

func TestHostPodEverExisted(t *testing.T) {
	p := basePod()
	if HostPodEverExisted(p) {
		t.Fatalf("fresh pod")
	}
	p.Status.PodIP = "10.0.0.1"
	if !HostPodEverExisted(p) {
		t.Fatalf("pod with podIP")
	}
}

func TestEvictionGracePeriodIsCapped(t *testing.T) {
	cap := names.PlacementEvictionMaxGracePeriodSeconds
	cases := []struct {
		name string
		spec *int64
		want int64
	}{
		{"unset uses cap", nil, cap},
		{"shorter than cap is kept", ptr.To[int64](5), 5},
		{"zero is kept", ptr.To[int64](0), 0},
		{"equal to cap is kept", ptr.To(cap), cap},
		{"longer than cap is capped", ptr.To[int64](3600), cap},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := basePod()
			p.Spec.TerminationGracePeriodSeconds = tc.spec
			if got := EvictionGracePeriod(p); got != tc.want {
				t.Fatalf("EvictionGracePeriod = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestEvictable(t *testing.T) {
	now := metav1.Now()
	cases := []struct {
		name   string
		mutate func(p *corev1.Pod)
		want   bool
	}{
		{"running", func(p *corev1.Pod) { p.Status.Phase = corev1.PodRunning }, true},
		{"pending but bound", func(p *corev1.Pod) { p.Status.Phase = corev1.PodPending }, true},
		{"already deleting", func(p *corev1.Pod) { p.Status.Phase = corev1.PodRunning; p.DeletionTimestamp = &now }, false},
		{"succeeded", func(p *corev1.Pod) { p.Status.Phase = corev1.PodSucceeded }, false},
		{"failed", func(p *corev1.Pod) { p.Status.Phase = corev1.PodFailed }, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := basePod()
			p.Spec.NodeName = "node-x"
			tc.mutate(p)
			if got := Evictable(p); got != tc.want {
				t.Fatalf("Evictable = %v, want %v", got, tc.want)
			}
		})
	}
}

// 虚拟 apiserver 给每个 Pod 加的 kube-api-access 卷：token + ca + downwardAPI namespace。
// 宿主 kubelet 会把 metadata.namespace 填成宿主命名空间，必须改由 token Secret 提供虚拟值。
func TestTranslatePodDownwardFieldsFromTokenSecret(t *testing.T) {
	p := basePod()
	p.Spec.Volumes = []corev1.Volume{
		{Name: "kube-api-access", VolumeSource: corev1.VolumeSource{Projected: &corev1.ProjectedVolumeSource{
			DefaultMode: ptr.To[int32](0o644),
			Sources: []corev1.VolumeProjection{
				{ServiceAccountToken: &corev1.ServiceAccountTokenProjection{Path: "token"}},
				{ConfigMap: &corev1.ConfigMapProjection{LocalObjectReference: corev1.LocalObjectReference{Name: "kube-root-ca.crt"}}},
				{DownwardAPI: &corev1.DownwardAPIProjection{Items: []corev1.DownwardAPIVolumeFile{
					{Path: "namespace", FieldRef: &corev1.ObjectFieldSelector{APIVersion: "v1", FieldPath: "metadata.namespace"}},
				}}},
			}}}},
		{Name: "podinfo", VolumeSource: corev1.VolumeSource{DownwardAPI: &corev1.DownwardAPIVolumeSource{
			DefaultMode: ptr.To[int32](0o600),
			Items: []corev1.DownwardAPIVolumeFile{
				{Path: "name", FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.name"}},
				{Path: "uid", FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.uid"}, Mode: ptr.To[int32](0o400)},
				{Path: "labels", FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.labels"}},
			}}}},
	}
	h := mustTranslate(t, p, testEnv())
	secret := names.SATokenSecretName(h.Name)

	src := h.Spec.Volumes[0].Projected.Sources
	for _, s := range src {
		if s.DownwardAPI != nil {
			t.Fatalf("namespace-only downwardAPI source must be dropped, got %+v", s.DownwardAPI)
		}
	}
	if got := secretItems(src, secret); len(got) != 2 || got[1] != (corev1.KeyToPath{Key: SATokenKeyNamespace, Path: "namespace"}) {
		t.Fatalf("namespace must come from the token Secret: %+v", got)
	}

	v := h.Spec.Volumes[1]
	if v.DownwardAPI != nil || v.Projected == nil || ptr.Deref(v.Projected.DefaultMode, 0) != 0o600 {
		t.Fatalf("downwardAPI volume with virtual fields must become projected keeping defaultMode: %+v", v.VolumeSource)
	}
	var keep []string
	for _, s := range v.Projected.Sources {
		if s.DownwardAPI != nil {
			for _, it := range s.DownwardAPI.Items {
				keep = append(keep, it.Path)
			}
		}
	}
	if len(keep) != 1 || keep[0] != "labels" {
		t.Fatalf("other downward fields stay with kubelet: %v", keep)
	}
	got := secretItems(v.Projected.Sources, secret)
	want := []corev1.KeyToPath{{Key: SATokenKeyPodName, Path: "name"}, {Key: SATokenKeyPodUID, Path: "uid", Mode: ptr.To[int32](0o400)}}
	if len(got) != 2 || got[0] != want[0] || got[1].Key != want[1].Key || got[1].Path != want[1].Path || ptr.Deref(got[1].Mode, 0) != 0o400 {
		t.Fatalf("name/uid from token Secret: %+v", got)
	}
}

// 没有 token Secret（关闭自动挂载）时无处取虚拟值，卷保持原样。
func TestTranslatePodDownwardFieldsWithoutToken(t *testing.T) {
	p := basePod()
	p.Spec.AutomountServiceAccountToken = ptr.To(false)
	p.Spec.Volumes = []corev1.Volume{{Name: "podinfo", VolumeSource: corev1.VolumeSource{DownwardAPI: &corev1.DownwardAPIVolumeSource{
		Items: []corev1.DownwardAPIVolumeFile{{Path: "ns", FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.namespace"}}},
	}}}}
	h := mustTranslate(t, p, testEnv())
	if h.Spec.Volumes[0].DownwardAPI == nil {
		t.Fatalf("without a token Secret the volume is left as is: %+v", h.Spec.Volumes[0].VolumeSource)
	}
}

func secretItems(src []corev1.VolumeProjection, name string) []corev1.KeyToPath {
	var out []corev1.KeyToPath
	for _, s := range src {
		if s.Secret != nil && s.Secret.Name == name {
			out = append(out, s.Secret.Items...)
		}
	}
	return out
}
