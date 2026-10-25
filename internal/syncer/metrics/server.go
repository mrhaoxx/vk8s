// Package metrics 在 syncer 内实现 metrics.k8s.io/v1beta1（设计文档 7.8），供 kubectl top 与 HPA 使用。
//
// 虚拟 apiserver 的聚合层经 egress 隧道到 syncer 8443；syncer 调宿主 metrics-server 取宿主 ns 的
// PodMetrics 与允许节点的 NodeMetrics，按 label 翻回虚拟名。全员 cluster-admin，不做 SubjectAccessReview。
package metrics

import (
	"context"
	"encoding/json"
	"fmt"
	"mime"
	"net/http"
	"strings"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/client-go/rest"
	metricsv1beta1 "k8s.io/metrics/pkg/apis/metrics/v1beta1"
	metricsclient "k8s.io/metrics/pkg/client/clientset/versioned"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"vk8s.io/vk8s/internal/syncer/translate"
)

const (
	groupName   = "metrics.k8s.io"
	versionName = "v1beta1"
	basePath    = "/apis/" + groupName + "/" + versionName

	protobufMediaType = "application/vnd.kubernetes.protobuf"
	jsonMediaType     = "application/json"
)

var (
	podsResource  = schema.GroupResource{Group: groupName, Resource: "pods"}
	nodesResource = schema.GroupResource{Group: groupName, Resource: "nodes"}
)

// Server metrics.k8s.io 实现。
type Server struct {
	Log           logr.Logger
	HostNamespace string
	// metrics 宿主 metrics.k8s.io client。
	metrics metricsclient.Interface
	// Host 宿主 cache reader：按宿主 Pod 名反查虚拟键。
	Host client.Reader
	// Virtual 虚拟 cache reader：取虚拟 Pod/Node 的 labels 并确认对象仍存在（Node 存在即允许）。
	Virtual client.Reader

	codecs serializer.CodecFactory
}

// NewServer 构造。
func NewServer(log logr.Logger, hostCfg *rest.Config, hostNamespace string, host, virtual client.Reader) (*Server, error) {
	mc, err := metricsclient.NewForConfig(hostCfg)
	if err != nil {
		return nil, fmt.Errorf("build host metrics client: %w", err)
	}
	scheme := runtime.NewScheme()
	if err := metricsv1beta1.AddToScheme(scheme); err != nil {
		return nil, err
	}
	metav1.AddToGroupVersion(scheme, metricsv1beta1.SchemeGroupVersion)
	return &Server{
		Log:           log,
		HostNamespace: hostNamespace,
		metrics:       mc,
		Host:          host,
		Virtual:       virtual,
		codecs:        serializer.NewCodecFactory(scheme),
	}, nil
}

// Handler 路由：discovery 三件 + pods/nodes。
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	ok := func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}
	mux.HandleFunc("GET /healthz", ok)
	mux.HandleFunc("GET /readyz", ok)
	mux.HandleFunc("GET /livez", ok)
	// 聚合层的 OpenAPI 控制器会周期拉取 /openapi/v2 与 /openapi/v3；404 会被当作错误反复记录。
	// metrics.k8s.io 没有需要聚合的 schema，返回空文档即可。
	mux.HandleFunc("GET /openapi/v2", staticJSON(`{"swagger":"2.0","info":{"title":"metrics.k8s.io","version":"v1beta1"},"paths":{}}`))
	mux.HandleFunc("GET /openapi/v3", staticJSON(`{"paths":{}}`))
	mux.HandleFunc("GET /apis", s.apiGroupList)
	mux.HandleFunc("GET /apis/"+groupName, s.apiGroup)
	mux.HandleFunc("GET "+basePath, s.apiResourceList)
	mux.HandleFunc("GET "+basePath+"/pods", s.listPods)
	mux.HandleFunc("GET "+basePath+"/namespaces/{namespace}/pods", s.listPods)
	mux.HandleFunc("GET "+basePath+"/namespaces/{namespace}/pods/{name}", s.getPod)
	mux.HandleFunc("GET "+basePath+"/nodes", s.listNodes)
	mux.HandleFunc("GET "+basePath+"/nodes/{name}", s.getNode)
	return mux
}

// ---- discovery ----

func (s *Server) apiGroupList(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, &metav1.APIGroupList{
		TypeMeta: metav1.TypeMeta{Kind: "APIGroupList", APIVersion: "v1"},
		Groups:   []metav1.APIGroup{apiGroup()},
	})
}

func (s *Server) apiGroup(w http.ResponseWriter, _ *http.Request) {
	g := apiGroup()
	g.TypeMeta = metav1.TypeMeta{Kind: "APIGroup", APIVersion: "v1"}
	writeJSON(w, http.StatusOK, &g)
}

func apiGroup() metav1.APIGroup {
	gv := metav1.GroupVersionForDiscovery{GroupVersion: groupName + "/" + versionName, Version: versionName}
	return metav1.APIGroup{
		Name:             groupName,
		Versions:         []metav1.GroupVersionForDiscovery{gv},
		PreferredVersion: gv,
	}
}

func (s *Server) apiResourceList(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, &metav1.APIResourceList{
		TypeMeta:     metav1.TypeMeta{Kind: "APIResourceList", APIVersion: "v1"},
		GroupVersion: groupName + "/" + versionName,
		APIResources: []metav1.APIResource{
			{Name: "nodes", SingularName: "", Namespaced: false, Kind: "NodeMetrics", Verbs: metav1.Verbs{"get", "list"}},
			{Name: "pods", SingularName: "", Namespaced: true, Kind: "PodMetrics", Verbs: metav1.Verbs{"get", "list"}},
		},
	})
}

// ---- pods ----

func (s *Server) listPods(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	vns := r.PathValue("namespace")
	selector, err := parseSelector(r)
	if err != nil {
		s.writeError(w, r, apierrors.NewBadRequest(err.Error()))
		return
	}
	hostList, err := s.metrics.MetricsV1beta1().PodMetricses(s.HostNamespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		s.writeError(w, r, hostUnavailable(err))
		return
	}
	out := &metricsv1beta1.PodMetricsList{
		TypeMeta: metav1.TypeMeta{Kind: "PodMetricsList", APIVersion: groupName + "/" + versionName},
		ListMeta: metav1.ListMeta{ResourceVersion: hostList.ResourceVersion},
	}
	for i := range hostList.Items {
		vm, ok := s.translatePodMetrics(ctx, &hostList.Items[i], vns, selector)
		if ok {
			out.Items = append(out.Items, *vm)
		}
	}
	s.write(w, r, out, http.StatusOK)
}

func (s *Server) getPod(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	vns, name := r.PathValue("namespace"), r.PathValue("name")
	hostName := translate.HostName(name, vns)
	pm, err := s.metrics.MetricsV1beta1().PodMetricses(s.HostNamespace).Get(ctx, hostName, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			s.writeError(w, r, apierrors.NewNotFound(podsResource, name))
			return
		}
		s.writeError(w, r, hostUnavailable(err))
		return
	}
	vm, ok := s.translatePodMetrics(ctx, pm, vns, nil)
	if !ok || vm.Name != name {
		s.writeError(w, r, apierrors.NewNotFound(podsResource, name))
		return
	}
	s.write(w, r, vm, http.StatusOK)
}

// translatePodMetrics 宿主 PodMetrics → 虚拟：经宿主 Pod 的 label 反查虚拟键，再以虚拟 Pod 的 labels 过滤。
func (s *Server) translatePodMetrics(ctx context.Context, pm *metricsv1beta1.PodMetrics, vns string, selector labels.Selector) (*metricsv1beta1.PodMetrics, bool) {
	hostPod := &corev1.Pod{}
	if err := s.Host.Get(ctx, client.ObjectKey{Namespace: s.HostNamespace, Name: pm.Name}, hostPod); err != nil {
		return nil, false
	}
	key, ok := translate.VirtualKey(hostPod)
	if !ok || (vns != "" && key.Namespace != vns) {
		return nil, false
	}
	vpod := &corev1.Pod{}
	if err := s.Virtual.Get(ctx, key, vpod); err != nil {
		return nil, false
	}
	if selector != nil && !selector.Matches(labels.Set(vpod.Labels)) {
		return nil, false
	}
	out := pm.DeepCopy()
	out.TypeMeta = metav1.TypeMeta{Kind: "PodMetrics", APIVersion: groupName + "/" + versionName}
	out.ObjectMeta = metav1.ObjectMeta{
		Name:              key.Name,
		Namespace:         key.Namespace,
		Labels:            vpod.Labels,
		CreationTimestamp: pm.CreationTimestamp,
	}
	return out, true
}

// ---- nodes ----

func (s *Server) listNodes(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	selector, err := parseSelector(r)
	if err != nil {
		s.writeError(w, r, apierrors.NewBadRequest(err.Error()))
		return
	}
	hostList, err := s.metrics.MetricsV1beta1().NodeMetricses().List(ctx, metav1.ListOptions{})
	if err != nil {
		s.writeError(w, r, hostUnavailable(err))
		return
	}
	out := &metricsv1beta1.NodeMetricsList{
		TypeMeta: metav1.TypeMeta{Kind: "NodeMetricsList", APIVersion: groupName + "/" + versionName},
		ListMeta: metav1.ListMeta{ResourceVersion: hostList.ResourceVersion},
	}
	for i := range hostList.Items {
		vm, ok := s.translateNodeMetrics(ctx, &hostList.Items[i], selector)
		if ok {
			out.Items = append(out.Items, *vm)
		}
	}
	s.write(w, r, out, http.StatusOK)
}

func (s *Server) getNode(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	name := r.PathValue("name")
	nm, err := s.metrics.MetricsV1beta1().NodeMetricses().Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			s.writeError(w, r, apierrors.NewNotFound(nodesResource, name))
			return
		}
		s.writeError(w, r, hostUnavailable(err))
		return
	}
	vm, ok := s.translateNodeMetrics(ctx, nm, nil)
	if !ok {
		s.writeError(w, r, apierrors.NewNotFound(nodesResource, name))
		return
	}
	s.write(w, r, vm, http.StatusOK)
}

// translateNodeMetrics 节点只在虚拟集群里存在镜像（即 nodePlacement 允许）时可见。
func (s *Server) translateNodeMetrics(ctx context.Context, nm *metricsv1beta1.NodeMetrics, selector labels.Selector) (*metricsv1beta1.NodeMetrics, bool) {
	vnode := &corev1.Node{}
	if err := s.Virtual.Get(ctx, client.ObjectKey{Name: nm.Name}, vnode); err != nil || !translate.IsManaged(vnode) {
		return nil, false
	}
	if selector != nil && !selector.Matches(labels.Set(vnode.Labels)) {
		return nil, false
	}
	out := nm.DeepCopy()
	out.TypeMeta = metav1.TypeMeta{Kind: "NodeMetrics", APIVersion: groupName + "/" + versionName}
	out.ObjectMeta = metav1.ObjectMeta{
		Name:              nm.Name,
		Labels:            vnode.Labels,
		CreationTimestamp: nm.CreationTimestamp,
	}
	return out, true
}

// ---- helpers ----

func parseSelector(r *http.Request) (labels.Selector, error) {
	raw := r.URL.Query().Get("labelSelector")
	if raw == "" {
		return nil, nil
	}
	sel, err := labels.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid labelSelector %q: %w", raw, err)
	}
	return sel, nil
}

// hostUnavailable 宿主 metrics API 不可用（未装 metrics-server 等）→ 503，带原因。
func hostUnavailable(err error) *apierrors.StatusError {
	return apierrors.NewServiceUnavailable(fmt.Sprintf("host metrics API unavailable: %v", err))
}

// write 按 Accept 协商 JSON / protobuf 编码 metrics 对象。
func (s *Server) write(w http.ResponseWriter, r *http.Request, obj runtime.Object, code int) {
	mediaType := jsonMediaType
	if acceptsProtobuf(r.Header.Get("Accept")) {
		mediaType = protobufMediaType
	}
	info, ok := runtime.SerializerInfoForMediaType(s.codecs.SupportedMediaTypes(), mediaType)
	if !ok {
		info, _ = runtime.SerializerInfoForMediaType(s.codecs.SupportedMediaTypes(), jsonMediaType)
	}
	encoder := s.codecs.EncoderForVersion(info.Serializer, schema.GroupVersion{Group: groupName, Version: versionName})
	w.Header().Set("Content-Type", info.MediaType)
	w.WriteHeader(code)
	if err := encoder.Encode(obj, w); err != nil {
		s.Log.V(1).Info("encode metrics response", "error", err.Error())
	}
}

// acceptsProtobuf 客户端 Accept 的首选是否为 protobuf（kcm 的 HPA 控制器会这样要求）。
func acceptsProtobuf(accept string) bool {
	for _, part := range strings.Split(accept, ",") {
		mt, _, err := mime.ParseMediaType(strings.TrimSpace(part))
		if err != nil {
			continue
		}
		switch mt {
		case protobufMediaType:
			return true
		case jsonMediaType, "*/*", "application/*":
			return false
		}
	}
	return false
}

// writeError 以 metav1.Status 返回错误。
func (s *Server) writeError(w http.ResponseWriter, _ *http.Request, err *apierrors.StatusError) {
	status := err.Status()
	status.TypeMeta = metav1.TypeMeta{Kind: "Status", APIVersion: "v1"}
	writeJSON(w, int(status.Code), &status)
}

func writeJSON(w http.ResponseWriter, code int, obj any) {
	w.Header().Set("Content-Type", jsonMediaType)
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(obj)
}

// staticJSON 返回固定 JSON 文档的 handler。
func staticJSON(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}
}
