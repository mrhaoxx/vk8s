package addons

import (
	"encoding/json"
	"fmt"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	"vk8s.io/vk8s/api/v1alpha1"
	"vk8s.io/vk8s/internal/addons/runner"
	"vk8s.io/vk8s/internal/names"
)

// 安装 Job 内的固定路径（设计 §3.3）。
const (
	MountKubeconfig    = "/run/vk8s/kubeconfig"
	MountSpec          = "/run/vk8s/addon"
	MountRegistry      = "/run/vk8s/registry"
	RegistryConfigPath = MountRegistry + "/.dockerconfigjson"
	specKey            = "spec.json"
	kubeconfigKey      = "kubeconfig"
)

// JobConfig 平台侧的 Job 参数。
type JobConfig struct {
	Namespace                string
	Image                    string
	Timeout                  time.Duration
	Resources                corev1.ResourceRequirements
	PodSecurityContext       *corev1.PodSecurityContext
	ContainerSecurityContext *corev1.SecurityContext
}

// BuildRunSpec 一次动作的输入。install / upgrade 需要模板；uninstall 可无模板，release / namespace 取 status。
func BuildRunSpec(t *v1alpha1.AddonTemplate, in *v1alpha1.AddonInstall, action string, timeout time.Duration) (runner.RunSpec, error) {
	spec := runner.RunSpec{Action: action, Timeout: timeout.String()}
	if action == "uninstall" {
		spec.Release, spec.Namespace = in.Status.Release, in.Status.Namespace
		if t != nil && spec.Release == "" {
			spec.Release, spec.Namespace = t.Spec.Release, t.Spec.Namespace
		}
		if spec.Release == "" {
			return spec, fmt.Errorf("unknown release location for %s", in.Name)
		}
		return spec, nil
	}
	if t == nil {
		return spec, fmt.Errorf("template %q is required for %s", in.Spec.Template, action)
	}
	values, err := MergeValues(t, in.Spec.Inputs)
	if err != nil {
		return spec, err
	}
	spec.Chart = t.Spec.Chart
	spec.Chart.Version = in.Spec.Version
	spec.Chart.PullSecretRef = nil
	spec.Release, spec.Namespace, spec.Values = t.Spec.Release, t.Spec.Namespace, values
	if t.Spec.Chart.PullSecretRef != nil {
		spec.RegistryConfig = RegistryConfigPath
	}
	return spec, nil
}

func ownerRef(in *v1alpha1.AddonInstall) metav1.OwnerReference {
	return metav1.OwnerReference{APIVersion: v1alpha1.GroupVersion.String(), Kind: "AddonInstall", Name: in.Name, UID: in.UID,
		Controller: ptr.To(true), BlockOwnerDeletion: ptr.To(true)}
}

// BuildSpecConfigMap 承载 spec.json 的 ConfigMap（含 password input 的明文，只在 vk8s-system 内）。
func BuildSpecConfigMap(in *v1alpha1.AddonInstall, spec runner.RunSpec, ns string) (*corev1.ConfigMap, error) {
	b, err := json.Marshal(spec)
	if err != nil {
		return nil, err
	}
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: names.AddonSpecConfigMap(in.Name), Namespace: ns,
			Labels: map[string]string{names.LabelAddonInstall: in.Name}, OwnerReferences: []metav1.OwnerReference{ownerRef(in)}},
		Data: map[string]string{specKey: string(b)},
	}, nil
}

// BuildJob 安装 Job；t 可为 nil（卸载时模板可能已删除）。
func BuildJob(in *v1alpha1.AddonInstall, t *v1alpha1.AddonTemplate, cfg JobConfig, suffix string) *batchv1.Job {
	vols := []corev1.Volume{
		{Name: "kubeconfig", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: names.ManagerAdminKubeconfigSecret(in.Spec.VCluster)}}},
		{Name: "spec", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: names.AddonSpecConfigMap(in.Name)}}}},
		{Name: "tmp", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
	}
	mounts := []corev1.VolumeMount{
		{Name: "kubeconfig", MountPath: MountKubeconfig, ReadOnly: true},
		{Name: "spec", MountPath: MountSpec, ReadOnly: true},
		{Name: "tmp", MountPath: "/tmp"},
	}
	if t != nil && t.Spec.Chart.PullSecretRef != nil {
		vols = append(vols, corev1.Volume{Name: "registry", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{
			SecretName: t.Spec.Chart.PullSecretRef.Name, Items: []corev1.KeyToPath{{Key: ".dockerconfigjson", Path: ".dockerconfigjson"}}}}})
		mounts = append(mounts, corev1.VolumeMount{Name: "registry", MountPath: MountRegistry, ReadOnly: true})
	}
	labels := map[string]string{names.LabelAddonInstall: in.Name}
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("addon-%s-%s", in.Name, suffix), Namespace: cfg.Namespace, Labels: labels,
			OwnerReferences: []metav1.OwnerReference{ownerRef(in)}},
		Spec: batchv1.JobSpec{
			BackoffLimit:            ptr.To[int32](0),
			ActiveDeadlineSeconds:   ptr.To(int64((cfg.Timeout + time.Minute).Seconds())),
			TTLSecondsAfterFinished: ptr.To[int32](3600),
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{
					ServiceAccountName:           names.AddonRunnerServiceAccount,
					AutomountServiceAccountToken: ptr.To(false),
					RestartPolicy:                corev1.RestartPolicyNever,
					SecurityContext:              cfg.PodSecurityContext,
					Volumes:                      vols,
					Containers: []corev1.Container{{
						Name:  "addon",
						Image: cfg.Image,
						Args:  []string{"addon", "run"},
						Env: []corev1.EnvVar{
							{Name: "HELM_CACHE_HOME", Value: "/tmp/helm/cache"},
							{Name: "HELM_CONFIG_HOME", Value: "/tmp/helm/config"},
							{Name: "HELM_DATA_HOME", Value: "/tmp/helm/data"},
						},
						Resources:                cfg.Resources,
						SecurityContext:          cfg.ContainerSecurityContext,
						VolumeMounts:             mounts,
						TerminationMessagePolicy: corev1.TerminationMessageFallbackToLogsOnError,
					}},
				},
			},
		},
	}
}
