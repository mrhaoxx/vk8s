package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"vk8s.io/vk8s/api/v1alpha1"
	"vk8s.io/vk8s/internal/audit"
	"vk8s.io/vk8s/internal/names"
)

type fixedClients struct{ c client.Client }

func (f fixedClients) For(context.Context, string) (client.Client, error) { return f.c, nil }

func apiTemplate(name, version string, ps v1alpha1.PodSecurityLevel) *v1alpha1.AddonTemplate {
	return &v1alpha1.AddonTemplate{ObjectMeta: metav1.ObjectMeta{Name: name}, Spec: v1alpha1.AddonTemplateSpec{
		DisplayName: strings.ToUpper(name), Chart: v1alpha1.AddonChart{Repository: "oci://r", Name: name, Version: version},
		Namespace: name, Release: name, Requires: v1alpha1.AddonRequires{PodSecurity: ps},
		Inputs: []v1alpha1.AddonInput{
			{Name: "webui", Type: "bool", Path: "webui.enabled"},
			{Name: "adminPassword", Type: "password", Path: "admin.password"},
		},
		Outputs: []v1alpha1.AddonOutput{{Name: "pw", Label: "密码", Kind: "secret", Secret: &v1alpha1.AddonSecretRef{Namespace: name, Name: "admin", Key: "password"}}},
	}}
}

func addonVC(name string, ps v1alpha1.PodSecurityLevel) *v1alpha1.VirtualCluster {
	return &v1alpha1.VirtualCluster{ObjectMeta: metav1.ObjectMeta{Name: name, UID: "uid-" + "x"},
		Spec: v1alpha1.VirtualClusterSpec{Owner: "alice", Members: []string{"bob"}, PodSecurity: ps}}
}

func addonServer(t *testing.T, objs ...client.Object) (*Server, client.Client, client.Client) {
	t.Helper()
	s, c := newAPITest(t, objs...)
	virtual := fake.NewClientBuilder().Build()
	s.VirtualClients = fixedClients{virtual}
	s.PodLogs = func(_ context.Context, ns, pod string, tail int64) (string, error) {
		return "log of " + ns + "/" + pod, nil
	}
	s.Config.SystemNamespace = "vk8s-system"
	return s, c, virtual
}

func call(t *testing.T, h func(http.ResponseWriter, *http.Request, *v1alpha1.User), me *v1alpha1.User, method, body string, pv map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, "/", strings.NewReader(body))
	for k, v := range pv {
		req.SetPathValue(k, v)
	}
	ctx, _ := audit.WithAnnotation(req.Context())
	rec := httptest.NewRecorder()
	h(rec, req.WithContext(ctx), me)
	return rec
}

var (
	owner  = apiUser("alice", false)
	member = apiUser("bob", false)
	other  = apiUser("carol", false)
	admin  = apiUser("root", true)
)

func TestListAddonCatalogHidesDeprecated(t *testing.T) {
	dep := apiTemplate("old", "1", "")
	dep.Spec.Deprecated = true
	s, _, _ := addonServer(t, apiTemplate("grafana", "10.5.15", ""), dep)
	rec := call(t, s.listAddonCatalog, other, "GET", "", nil)
	var got []AddonTemplateView
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if rec.Code != 200 || len(got) != 1 || got[0].Name != "grafana" || got[0].Version != "10.5.15" || len(got[0].Inputs) != 2 {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body)
	}
}

func TestInstallAddonPermissions(t *testing.T) {
	dep := apiTemplate("old", "1", "")
	dep.Spec.Deprecated = true
	s, c, _ := addonServer(t, addonVC("alice-dev", v1alpha1.PodSecurityPrivileged), apiTemplate("grafana", "1.0.0", ""), dep)
	pv := map[string]string{"name": "alice-dev"}
	body := `{"template":"grafana"}`
	if rec := call(t, s.installAddon, member, "POST", body, pv); rec.Code != 403 {
		t.Fatalf("member: %d", rec.Code)
	}
	if rec := call(t, s.installAddon, other, "POST", body, pv); rec.Code != 404 {
		t.Fatalf("non-member: %d", rec.Code)
	}
	if rec := call(t, s.installAddon, owner, "POST", body, pv); rec.Code != 201 {
		t.Fatalf("owner: %d %s", rec.Code, rec.Body)
	}
	in := &v1alpha1.AddonInstall{}
	if err := c.Get(context.Background(), client.ObjectKey{Name: "alice-dev-grafana"}, in); err != nil || in.Spec.Version != "1.0.0" || in.Spec.VCluster != "alice-dev" ||
		len(in.OwnerReferences) != 1 || in.OwnerReferences[0].Kind != "VirtualCluster" {
		t.Fatalf("install=%+v err=%v", in, err)
	}
	if rec := call(t, s.installAddon, admin, "POST", body, pv); rec.Code != 409 {
		t.Fatalf("duplicate: %d", rec.Code)
	}
	if rec := call(t, s.installAddon, owner, "POST", `{"template":"nope"}`, pv); rec.Code != 400 {
		t.Fatalf("unknown: %d", rec.Code)
	}
	if rec := call(t, s.installAddon, owner, "POST", `{"template":"old"}`, pv); rec.Code != 400 {
		t.Fatalf("deprecated: %d", rec.Code)
	}
}

func TestInstallAddonValidatesRequiresAndInputs(t *testing.T) {
	s, _, _ := addonServer(t, addonVC("alice-dev", v1alpha1.PodSecurityBaseline), apiTemplate("devpods", "0.1.0", v1alpha1.PodSecurityPrivileged), apiTemplate("grafana", "1", ""))
	pv := map[string]string{"name": "alice-dev"}
	if rec := call(t, s.installAddon, owner, "POST", `{"template":"devpods"}`, pv); rec.Code != 400 || !strings.Contains(rec.Body.String(), "需要 privileged 档位") {
		t.Fatalf("requires: %d %s", rec.Code, rec.Body)
	}
	if rec := call(t, s.installAddon, owner, "POST", `{"template":"grafana","inputs":{"webui":"yes"}}`, pv); rec.Code != 400 {
		t.Fatalf("bad input: %d", rec.Code)
	}
}

func TestAddonViewsMaskPasswordInputs(t *testing.T) {
	s, _, _ := addonServer(t, addonVC("alice-dev", ""), apiTemplate("grafana", "1", ""))
	pv := map[string]string{"name": "alice-dev"}
	req := httptest.NewRequest("POST", "/", strings.NewReader(`{"template":"grafana","inputs":{"adminPassword":"s3cret"}}`))
	req.SetPathValue("name", "alice-dev")
	ctx, ann := audit.WithAnnotation(req.Context())
	rec := httptest.NewRecorder()
	s.installAddon(rec, req.WithContext(ctx), owner)
	if rec.Code != 201 || !ann.SkipBody || ann.Target != "alice-dev/grafana" || ann.VCluster != "alice-dev" {
		t.Fatalf("code=%d ann=%+v", rec.Code, ann)
	}
	if strings.Contains(rec.Body.String(), "s3cret") {
		t.Fatalf("response leaks password: %s", rec.Body)
	}
	list := call(t, s.listClusterAddons, member, "GET", "", pv)
	if list.Code != 200 || strings.Contains(list.Body.String(), "s3cret") || !strings.Contains(list.Body.String(), `"adminPassword":"***"`) {
		t.Fatalf("list: %d %s", list.Code, list.Body)
	}
}

func installed(vc, tpl, version string, phase v1alpha1.AddonPhase) *v1alpha1.AddonInstall {
	return &v1alpha1.AddonInstall{ObjectMeta: metav1.ObjectMeta{Name: vc + "-" + tpl, Finalizers: []string{names.AddonFinalizer}},
		Spec:   v1alpha1.AddonInstallSpec{VCluster: vc, Template: tpl, Version: version},
		Status: v1alpha1.AddonInstallStatus{Phase: phase, InstalledVersion: version, Job: "addon-job-1"}}
}

func TestUpgradeAddon(t *testing.T) {
	s, c, _ := addonServer(t, addonVC("alice-dev", ""), apiTemplate("grafana", "0.2.0", ""), installed("alice-dev", "grafana", "0.1.0", v1alpha1.AddonReady))
	pv := map[string]string{"name": "alice-dev", "template": "grafana"}
	if rec := call(t, s.upgradeAddon, owner, "POST", "", pv); rec.Code != 204 {
		t.Fatalf("upgrade: %d %s", rec.Code, rec.Body)
	}
	in := &v1alpha1.AddonInstall{}
	_ = c.Get(context.Background(), client.ObjectKey{Name: "alice-dev-grafana"}, in)
	if in.Spec.Version != "0.2.0" {
		t.Fatalf("version = %s", in.Spec.Version)
	}
	if rec := call(t, s.upgradeAddon, owner, "POST", "", pv); rec.Code != 204 {
		t.Fatalf("already latest: %d", rec.Code)
	}
	if rec := call(t, s.upgradeAddon, member, "POST", "", pv); rec.Code != 403 {
		t.Fatalf("member: %d", rec.Code)
	}
}

func TestRetryAddon(t *testing.T) {
	s, c, _ := addonServer(t, addonVC("alice-dev", ""), apiTemplate("grafana", "1", ""), installed("alice-dev", "grafana", "1", v1alpha1.AddonFailed),
		installed("alice-dev", "ok", "1", v1alpha1.AddonReady))
	if rec := call(t, s.retryAddon, owner, "POST", "", map[string]string{"name": "alice-dev", "template": "grafana"}); rec.Code != 204 {
		t.Fatalf("retry: %d %s", rec.Code, rec.Body)
	}
	in := &v1alpha1.AddonInstall{}
	_ = c.Get(context.Background(), client.ObjectKey{Name: "alice-dev-grafana"}, in)
	if in.Annotations[names.AnnotationAddonRetry] == "" {
		t.Fatal("retry annotation missing")
	}
	if rec := call(t, s.retryAddon, owner, "POST", "", map[string]string{"name": "alice-dev", "template": "ok"}); rec.Code != 409 {
		t.Fatalf("not failed: %d", rec.Code)
	}
}

func TestUninstallAddon(t *testing.T) {
	s, c, _ := addonServer(t, addonVC("alice-dev", ""), installed("alice-dev", "grafana", "1", v1alpha1.AddonReady))
	if rec := call(t, s.uninstallAddon, owner, "DELETE", "", map[string]string{"name": "alice-dev", "template": "grafana"}); rec.Code != 204 {
		t.Fatalf("uninstall: %d", rec.Code)
	}
	in := &v1alpha1.AddonInstall{}
	if err := c.Get(context.Background(), client.ObjectKey{Name: "alice-dev-grafana"}, in); err != nil || in.DeletionTimestamp.IsZero() {
		t.Fatalf("install=%+v err=%v", in, err)
	}
	if rec := call(t, s.uninstallAddon, owner, "DELETE", "", map[string]string{"name": "alice-dev", "template": "nope"}); rec.Code != 404 {
		t.Fatalf("missing: %d", rec.Code)
	}
}

func TestAddonOutputsSecretOnlyForOwner(t *testing.T) {
	s, _, virtual := addonServer(t, addonVC("alice-dev", ""), apiTemplate("grafana", "1", ""), installed("alice-dev", "grafana", "1", v1alpha1.AddonReady))
	_ = virtual.Create(context.Background(), &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "grafana", Name: "admin"}, Data: map[string][]byte{"password": []byte("pw-123")}})
	pv := map[string]string{"name": "alice-dev", "template": "grafana"}
	rec := call(t, s.getAddonOutputs, owner, "GET", "", pv)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "pw-123") {
		t.Fatalf("owner: %d %s", rec.Code, rec.Body)
	}
	if rec := call(t, s.getAddonOutputs, member, "GET", "", pv); rec.Code != 403 {
		t.Fatalf("member: %d", rec.Code)
	}
}

func TestAddonLogs(t *testing.T) {
	s, c, _ := addonServer(t, addonVC("alice-dev", ""), installed("alice-dev", "grafana", "1", v1alpha1.AddonFailed))
	_ = c.Create(context.Background(), &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "vk8s-system", Name: "addon-job-1-abcde", Labels: map[string]string{"job-name": "addon-job-1"}}})
	rec := call(t, s.getAddonLogs, member, "GET", "", map[string]string{"name": "alice-dev", "template": "grafana"})
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "log of vk8s-system/addon-job-1-abcde") {
		t.Fatalf("logs: %d %s", rec.Code, rec.Body)
	}
}

func TestCreateVClusterWithAddonsValidatesFirst(t *testing.T) {
	s, c, _ := addonServer(t, apiTemplate("devpods", "0.1.0", v1alpha1.PodSecurityPrivileged))
	s.Config.Defaults.PodSecurity = v1alpha1.PodSecurityBaseline
	s.Config.Limits.MaxVClustersPerUser = 10
	rec := call(t, s.createVCluster, admin, "POST", `{"name":"new","podSecurity":"baseline","addons":[{"template":"devpods"}]}`, nil)
	if rec.Code != 400 {
		t.Fatalf("baseline+devpods: %d %s", rec.Code, rec.Body)
	}
	if err := c.Get(context.Background(), client.ObjectKey{Name: "new"}, &v1alpha1.VirtualCluster{}); err == nil {
		t.Fatal("cluster must not be created when an addon is invalid")
	}
	rec = call(t, s.createVCluster, admin, "POST", `{"name":"new","podSecurity":"privileged","addons":[{"template":"devpods"}]}`, nil)
	if rec.Code != 201 {
		t.Fatalf("privileged+devpods: %d %s", rec.Code, rec.Body)
	}
	in := &v1alpha1.AddonInstall{}
	if err := c.Get(context.Background(), client.ObjectKey{Name: "new-devpods"}, in); err != nil || in.Spec.Version != "0.1.0" || in.OwnerReferences[0].Name != "new" {
		t.Fatalf("install=%+v err=%v", in, err)
	}
}

func TestConfigViewAddonsEnabled(t *testing.T) {
	s, _, _ := addonServer(t)
	if s.addonsEnabled(context.Background()) {
		t.Fatal("no templates → false")
	}
	s, _, _ = addonServer(t, apiTemplate("grafana", "1", ""))
	if !s.addonsEnabled(context.Background()) {
		t.Fatal("template → true")
	}
}

// 校验失败（400）时请求体也不能进审计：SkipBody 必须在校验之前置位。
func TestInstallAddonSkipsAuditBodyEvenWhenRejected(t *testing.T) {
	s, _, _ := addonServer(t, addonVC("alice-dev", ""), apiTemplate("grafana", "1", ""))
	for _, body := range []string{
		`{"template":"grafana","inputs":{"adminPassword":"s3cret","webui":"yes"}}`,
		`{"template":"missing","inputs":{"adminPassword":"s3cret"}}`,
	} {
		req := httptest.NewRequest("POST", "/", strings.NewReader(body))
		req.SetPathValue("name", "alice-dev")
		ctx, ann := audit.WithAnnotation(req.Context())
		rec := httptest.NewRecorder()
		s.installAddon(rec, req.WithContext(ctx), owner)
		if rec.Code != 400 || !ann.SkipBody {
			t.Fatalf("body %s: code=%d skip=%v", body, rec.Code, ann.SkipBody)
		}
	}
	req := httptest.NewRequest("POST", "/", strings.NewReader(`{"name":"x","addons":[{"template":"missing","inputs":{"adminPassword":"s3cret"}}]}`))
	ctx, ann := audit.WithAnnotation(req.Context())
	rec := httptest.NewRecorder()
	s.createVCluster(rec, req.WithContext(ctx), admin)
	if rec.Code != 400 || !ann.SkipBody {
		t.Fatalf("create: code=%d skip=%v", rec.Code, ann.SkipBody)
	}
}

func TestInvalidTemplatesAreHiddenAndRejected(t *testing.T) {
	bad := apiTemplate("broken", "1", "")
	bad.Spec.Chart.Repository = "http://insecure"
	s, _, _ := addonServer(t, addonVC("alice-dev", ""), bad, apiTemplate("grafana", "1", ""))
	rec := call(t, s.listAddonCatalog, owner, "GET", "", nil)
	if strings.Contains(rec.Body.String(), "broken") {
		t.Fatalf("invalid template listed: %s", rec.Body)
	}
	if rec := call(t, s.installAddon, owner, "POST", `{"template":"broken"}`, map[string]string{"name": "alice-dev"}); rec.Code != 400 {
		t.Fatalf("install invalid: %d", rec.Code)
	}
}

func TestUpgradeAddonAppliesValuesOnlyUpdate(t *testing.T) {
	in := installed("alice-dev", "grafana", "1", v1alpha1.AddonReady)
	in.Status.UpdateAvailable = true
	s, c, _ := addonServer(t, addonVC("alice-dev", ""), apiTemplate("grafana", "1", ""), in)
	if rec := call(t, s.upgradeAddon, owner, "POST", "", map[string]string{"name": "alice-dev", "template": "grafana"}); rec.Code != 204 {
		t.Fatalf("upgrade: %d", rec.Code)
	}
	got := &v1alpha1.AddonInstall{}
	_ = c.Get(context.Background(), client.ObjectKey{Name: "alice-dev-grafana"}, got)
	if got.Annotations[names.AnnotationAddonRetry] == "" {
		t.Fatal("values-only update must be applied via the retry annotation")
	}
}
