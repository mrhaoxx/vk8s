package webhook

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/go-logr/logr"
	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

func serviceCreateReq(t *testing.T) admission.Request {
	t.Helper()
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "web"},
		Spec:       corev1.ServiceSpec{Type: corev1.ServiceTypeClusterIP, Ports: []corev1.ServicePort{{Name: "http", Port: 80}}, Selector: map[string]string{"app": "web"}},
	}
	raw, err := json.Marshal(svc)
	if err != nil {
		t.Fatal(err)
	}
	req := admission.Request{AdmissionRequest: admissionv1.AdmissionRequest{Operation: admissionv1.Create, Namespace: "default", Name: "web"}}
	req.Object.Raw = raw
	return req
}

// 宿主 ResourceQuota 拒绝 Service（services 计数超限）时，用户必须拿到 403 与配额文案，而不是 500。
func TestServiceMutatorMapsForbiddenToDenied(t *testing.T) {
	s := testScheme(t)
	quotaErr := apierrors.NewForbidden(schema.GroupResource{Resource: "services"}, "web-x-default",
		errors.New("exceeded quota: vk8s-quota, requested: services=1, used: services=21, limited: services=21"))
	host := fake.NewClientBuilder().WithScheme(s).WithInterceptorFuncs(interceptor.Funcs{
		Create: func(context.Context, client.WithWatch, client.Object, ...client.CreateOption) error { return quotaErr },
	}).Build()
	m := &ServiceMutator{Log: logr.Discard(), Host: host, HostNamespace: testHostNS}
	resp := m.Handle(context.Background(), serviceCreateReq(t))
	if resp.Allowed || resp.Result == nil || resp.Result.Code != 403 {
		t.Fatalf("response = %+v", resp)
	}
	if !strings.Contains(resp.Result.Message, "exceeded quota: vk8s-quota") {
		t.Fatalf("message must carry the host quota text, got %q", resp.Result.Message)
	}

	// 其他宿主错误（非配额、非 AlreadyExists）仍是拒绝并带原话，不是 500
	internal := apierrors.NewInternalError(errors.New("boom"))
	host = fake.NewClientBuilder().WithScheme(s).WithInterceptorFuncs(interceptor.Funcs{
		Create: func(context.Context, client.WithWatch, client.Object, ...client.CreateOption) error { return internal },
	}).Build()
	m = &ServiceMutator{Log: logr.Discard(), Host: host, HostNamespace: testHostNS}
	if resp := m.Handle(context.Background(), serviceCreateReq(t)); resp.Allowed || !strings.Contains(resp.Result.Message, "boom") {
		t.Fatalf("response = %+v", resp)
	}
}
