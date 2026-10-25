package addons

import (
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"vk8s.io/vk8s/api/v1alpha1"
	"vk8s.io/vk8s/internal/names"
)

func install() *v1alpha1.AddonInstall {
	in := &v1alpha1.AddonInstall{ObjectMeta: metav1.ObjectMeta{Name: "alice-dev-devpods", UID: "uid-1"},
		Spec: v1alpha1.AddonInstallSpec{VCluster: "alice-dev", Template: "devpods", Version: "0.0.9", Inputs: map[string]string{"webui": "true"}}}
	return in
}

func TestBuildJobSpec(t *testing.T) {
	cfg := JobConfig{Namespace: "vk8s-system", Image: "img:1", Timeout: 10 * time.Minute}
	j := BuildJob(install(), devpodsTemplate(), cfg, "ab12c")
	if j.Name != "addon-alice-dev-devpods-ab12c" || j.Namespace != "vk8s-system" || j.Labels[names.LabelAddonInstall] != "alice-dev-devpods" {
		t.Fatalf("meta = %+v", j.ObjectMeta)
	}
	if len(j.OwnerReferences) != 1 || j.OwnerReferences[0].Kind != "AddonInstall" || j.OwnerReferences[0].UID != "uid-1" {
		t.Fatalf("owner = %+v", j.OwnerReferences)
	}
	s := j.Spec
	if *s.BackoffLimit != 0 || *s.ActiveDeadlineSeconds != 660 || *s.TTLSecondsAfterFinished != 3600 {
		t.Fatalf("job spec = %+v", s)
	}
	p := s.Template.Spec
	if p.ServiceAccountName != "vk8s-addon-runner" || p.RestartPolicy != corev1.RestartPolicyNever || len(p.Containers) != 1 {
		t.Fatalf("pod = %+v", p)
	}
	c := p.Containers[0]
	if c.Image != "img:1" || strings.Join(c.Args, " ") != "addon run" || c.TerminationMessagePolicy != corev1.TerminationMessageFallbackToLogsOnError {
		t.Fatalf("container = %+v", c)
	}
	env := map[string]string{}
	for _, e := range c.Env {
		env[e.Name] = e.Value
	}
	if env["HELM_CACHE_HOME"] != "/tmp/helm/cache" || env["HELM_CONFIG_HOME"] != "/tmp/helm/config" || env["HELM_DATA_HOME"] != "/tmp/helm/data" {
		t.Fatalf("env = %v", env)
	}
	vols := map[string]corev1.Volume{}
	for _, v := range p.Volumes {
		vols[v.Name] = v
	}
	if vols["kubeconfig"].Secret == nil || vols["kubeconfig"].Secret.SecretName != "vc-alice-dev-admin-kubeconfig" ||
		vols["spec"].ConfigMap == nil || vols["spec"].ConfigMap.Name != "addon-alice-dev-devpods-spec" || vols["tmp"].EmptyDir == nil {
		t.Fatalf("volumes = %+v", p.Volumes)
	}
	if _, ok := vols["registry"]; ok {
		t.Fatal("no registry volume without pullSecretRef")
	}
	mounts := map[string]string{}
	for _, m := range c.VolumeMounts {
		mounts[m.Name] = m.MountPath
	}
	if mounts["kubeconfig"] != "/run/vk8s/kubeconfig" || mounts["spec"] != "/run/vk8s/addon" || mounts["tmp"] != "/tmp" {
		t.Fatalf("mounts = %v", mounts)
	}

	tpl := devpodsTemplate()
	tpl.Spec.Chart.PullSecretRef = &corev1.LocalObjectReference{Name: "harbor-robot"}
	j = BuildJob(install(), tpl, cfg, "ab12c")
	found := false
	for _, v := range j.Spec.Template.Spec.Volumes {
		if v.Name == "registry" && v.Secret != nil && v.Secret.SecretName == "harbor-robot" && v.Secret.Items[0].Key == ".dockerconfigjson" {
			found = true
		}
	}
	if !found {
		t.Fatal("pullSecretRef must mount the registry secret")
	}
}

func TestBuildRunSpecUsesInstallVersionAndMergedValues(t *testing.T) {
	spec, err := BuildRunSpec(devpodsTemplate(), install(), "install", 10*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := MergeValues(devpodsTemplate(), install().Spec.Inputs)
	if spec.Chart.Version != "0.0.9" || spec.Values != want || spec.Timeout != "10m0s" || spec.Release != "devpod" || spec.Namespace != "devpod-system" || spec.Action != "install" {
		t.Fatalf("spec = %+v", spec)
	}
	tpl := devpodsTemplate()
	tpl.Spec.Chart.PullSecretRef = &corev1.LocalObjectReference{Name: "harbor-robot"}
	spec, _ = BuildRunSpec(tpl, install(), "upgrade", time.Minute)
	if spec.RegistryConfig != "/run/vk8s/registry/.dockerconfigjson" {
		t.Fatalf("registryConfig = %q", spec.RegistryConfig)
	}
}

func TestBuildRunSpecUninstallWithoutTemplate(t *testing.T) {
	in := install()
	in.Status.Release, in.Status.Namespace = "devpod", "devpod-system"
	spec, err := BuildRunSpec(nil, in, "uninstall", time.Minute)
	if err != nil || spec.Action != "uninstall" || spec.Release != "devpod" || spec.Namespace != "devpod-system" {
		t.Fatalf("spec=%+v err=%v", spec, err)
	}
}

func TestBuildSpecConfigMap(t *testing.T) {
	spec, _ := BuildRunSpec(devpodsTemplate(), install(), "install", time.Minute)
	cm, err := BuildSpecConfigMap(install(), spec, "vk8s-system")
	if err != nil || cm.Name != "addon-alice-dev-devpods-spec" || cm.Namespace != "vk8s-system" || !strings.Contains(cm.Data["spec.json"], `"action":"install"`) ||
		len(cm.OwnerReferences) != 1 {
		t.Fatalf("cm=%+v err=%v", cm, err)
	}
}
