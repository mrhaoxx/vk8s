package controllers

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"vk8s.io/vk8s/api/v1alpha1"
	"vk8s.io/vk8s/internal/addons"
	"vk8s.io/vk8s/internal/addons/runner"
	"vk8s.io/vk8s/internal/config"
	"vk8s.io/vk8s/internal/names"
)

// AddonInstall 失败原因（status.reason）。
const (
	AddonReasonRequiresUnmet   = "RequiresUnmet"
	AddonReasonTemplateMissing = "TemplateMissing"
	AddonReasonTemplateInvalid = "TemplateInvalid"
	AddonReasonClusterNotReady = "ClusterNotReady"
	AddonReasonJobFailed       = "JobFailed"
	AddonReasonTimeout         = "Timeout"

	labelAddonAction = "vk8s.io/addon-action"
	// Job 注解：属于哪一次运行（AddonInstall 的 generation）与装的版本
	annotationAddonGeneration = "vk8s.io/addon-generation"
	annotationAddonVersion    = "vk8s.io/addon-version"
	annotationAddonSpecHash   = "vk8s.io/addon-spec-hash"
	addonIndexTemplate        = "spec.template"
	addonIndexVCluster        = "spec.vcluster"
	addonReadyResync          = 5 * time.Minute
	addonUninstallMaxBO       = 5 * time.Minute
	addonMessageMax           = 2048
)

// VirtualClients 按集群取虚拟集群 client（管理 kubeconfig）。
type VirtualClients interface {
	For(ctx context.Context, vc string) (client.Client, error)
}

// AddonInstallReconciler 应用安装的状态机（设计 2026-10-25 §3.4）。
type AddonInstallReconciler struct {
	client.Client
	Scheme  *runtime.Scheme
	Config  *config.Config
	Clients VirtualClients
	// APIReader 直读 apiserver（不经缓存）：判断「是否已有 Job」必须看到刚建的 Job，否则缓存滞后会建出第二个。
	APIReader client.Reader
	Now       func() time.Time
	Suffix    func() string
}

func (r *AddonInstallReconciler) jobReader() client.Reader {
	if r.APIReader != nil {
		return r.APIReader
	}
	return r.Client
}

// SetupWithManager 注册：AddonInstall；所属 Job；模板与集群变化时入队引用者。
func (r *AddonInstallReconciler) SetupWithManager(mgr ctrl.Manager) error {
	idx := mgr.GetFieldIndexer()
	ctx := context.Background()
	if err := idx.IndexField(ctx, &v1alpha1.AddonInstall{}, addonIndexTemplate, func(o client.Object) []string {
		return []string{o.(*v1alpha1.AddonInstall).Spec.Template}
	}); err != nil {
		return err
	}
	if err := idx.IndexField(ctx, &v1alpha1.AddonInstall{}, addonIndexVCluster, func(o client.Object) []string {
		return []string{o.(*v1alpha1.AddonInstall).Spec.VCluster}
	}); err != nil {
		return err
	}
	by := func(field string) handler.EventHandler {
		return handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, o client.Object) []reconcile.Request {
			list := &v1alpha1.AddonInstallList{}
			if err := r.List(ctx, list, client.MatchingFields{field: o.GetName()}); err != nil {
				return nil
			}
			out := make([]reconcile.Request, 0, len(list.Items))
			for _, it := range list.Items {
				out = append(out, reconcile.Request{NamespacedName: client.ObjectKey{Name: it.Name}})
			}
			return out
		})
	}
	return ctrl.NewControllerManagedBy(mgr).
		Named("addoninstall").
		For(&v1alpha1.AddonInstall{}).
		Owns(&batchv1.Job{}).
		Watches(&v1alpha1.AddonTemplate{}, by(addonIndexTemplate)).
		Watches(&v1alpha1.VirtualCluster{}, by(addonIndexVCluster)).
		Complete(r)
}

func (r *AddonInstallReconciler) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *AddonInstallReconciler) suffix() string {
	if r.Suffix != nil {
		return r.Suffix()
	}
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, 5)
	for i := range b {
		n, _ := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
		b[i] = alphabet[n.Int64()]
	}
	return string(b)
}

// Reconcile 实现状态机。
func (r *AddonInstallReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	in := &v1alpha1.AddonInstall{}
	if err := r.Get(ctx, req.NamespacedName, in); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	vc := &v1alpha1.VirtualCluster{}
	vcErr := r.Get(ctx, client.ObjectKey{Name: in.Spec.VCluster}, vc)
	if vcErr != nil && !apierrors.IsNotFound(vcErr) {
		return ctrl.Result{}, vcErr
	}
	vcGone := apierrors.IsNotFound(vcErr) || !vc.DeletionTimestamp.IsZero()

	tpl := &v1alpha1.AddonTemplate{}
	if err := r.Get(ctx, client.ObjectKey{Name: in.Spec.Template}, tpl); err != nil {
		if !apierrors.IsNotFound(err) {
			return ctrl.Result{}, err
		}
		tpl = nil
	}

	if !in.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, in, tpl, vc, vcGone)
	}
	if vcGone {
		return ctrl.Result{}, nil // 集群在删，级联会删掉本对象
	}
	if controllerutil.AddFinalizer(in, names.AddonFinalizer) {
		if err := r.Update(ctx, in); err != nil {
			return ctrl.Result{}, err
		}
	}

	job, err := r.currentJob(ctx, in)
	if err != nil {
		return ctrl.Result{}, err
	}
	if job, err = r.adoptJob(ctx, in, job); err != nil {
		return ctrl.Result{}, err
	}
	if job != nil && !jobFinished(job) {
		return ctrl.Result{}, nil // 同一时刻最多一个 Job；Job 变化会再入队
	}
	if job != nil && in.Status.Phase == v1alpha1.AddonInstalling &&
		(job.Labels[labelAddonAction] == "uninstall" || job.Annotations[annotationAddonGeneration] != strconv.FormatInt(in.Status.ObservedGeneration, 10)) {
		job = nil // 最新的 Job 不是本次运行的（本次的 Job 不见了）
	}
	if job != nil && in.Status.Phase == v1alpha1.AddonInstalling {
		return r.finishRun(ctx, in, tpl, vc, job)
	}
	if job == nil && in.Status.Phase == v1alpha1.AddonInstalling && in.Status.Job != "" {
		// 安装 Job 不见了（TTL 到期时 manager 不在、被手工删除）：重跑，避免永远「安装中」
		in.Status.ObservedGeneration = 0
	}

	// 用户重试：消费注解后重新安装
	if _, retry := in.Annotations[names.AnnotationAddonRetry]; retry {
		delete(in.Annotations, names.AnnotationAddonRetry)
		if err := r.Update(ctx, in); err != nil {
			return ctrl.Result{}, err
		}
		if in.Status.Phase == v1alpha1.AddonFailed || in.Status.Phase == v1alpha1.AddonReady {
			in.Status.ObservedGeneration = 0 // 强制重跑（Ready 时即按目录当前内容重装，用于「有更新」）
		}
	}

	if tpl == nil {
		if in.Status.InstalledVersion != "" {
			// 已装、目录里删了：保持现状，卸载仍可（status 记录了位置）
			in.Status.AvailableVersion = ""
			return ctrl.Result{}, r.Status().Update(ctx, in)
		}
		return ctrl.Result{}, r.setFailed(ctx, in, AddonReasonTemplateMissing, fmt.Sprintf("目录中没有应用 %q", in.Spec.Template))
	}
	if err := addons.ValidateTemplate(tpl); err != nil {
		if in.Status.InstalledVersion == "" {
			return ctrl.Result{}, r.setFailed(ctx, in, AddonReasonTemplateInvalid, "目录项配置无效："+err.Error())
		}
		return ctrl.Result{}, nil // 已装：保持现状，等管理员修好目录
	}
	level := vc.Spec.PodSecurity
	if level == "" {
		level = r.Config.Defaults.PodSecurity
	}
	// 档位只是安装前提：已装的应用在档位收紧后不改状态（运行中的 Pod 由宿主 PSA 决定）
	if ok, why := addons.RequiresMet(tpl, level); !ok && in.Status.InstalledVersion == "" {
		return ctrl.Result{}, r.setFailed(ctx, in, AddonReasonRequiresUnmet, why)
	}
	if vc.Status.Phase != v1alpha1.PhaseRunning {
		in.Status.Phase = v1alpha1.AddonPending
		in.Status.Reason = AddonReasonClusterNotReady
		in.Status.Message = fmt.Sprintf("集群尚未就绪（%s）", vc.Status.Phase)
		return ctrl.Result{}, r.Status().Update(ctx, in)
	}

	// 前置条件类的失败（模板缺失、档位不够）在条件满足后自动开始；Job 失败要用户重试
	preconditionFailed := in.Status.Reason == AddonReasonTemplateMissing || in.Status.Reason == AddonReasonRequiresUnmet ||
		in.Status.Reason == AddonReasonTemplateInvalid
	needRun := in.Status.InstalledVersion == "" && (in.Status.Phase != v1alpha1.AddonFailed || preconditionFailed) ||
		in.Status.ObservedGeneration != in.Generation
	if needRun {
		action := "install"
		if in.Status.InstalledVersion != "" {
			action = "upgrade"
		}
		if err := r.startRun(ctx, in, tpl, action); err != nil {
			r.reportStartError(ctx, in, err)
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}
	if in.Status.Phase == v1alpha1.AddonReady {
		return r.refreshReady(ctx, in, tpl, vc)
	}
	return ctrl.Result{}, nil
}

// reportStartError 起 Job 之前的错误（读 kubeconfig、建 Secret / ConfigMap / Job）写进 message，界面能看到原因；err 仍返回以退避重试。
func (r *AddonInstallReconciler) reportStartError(ctx context.Context, in *v1alpha1.AddonInstall, err error) {
	cur := &v1alpha1.AddonInstall{}
	if r.Get(ctx, client.ObjectKeyFromObject(in), cur) != nil {
		return
	}
	cur.Status.Message = "尚未开始，正在重试：" + err.Error()
	_ = r.Status().Update(ctx, cur)
}

func (r *AddonInstallReconciler) setFailed(ctx context.Context, in *v1alpha1.AddonInstall, reason, msg string) error {
	in.Status.Phase, in.Status.Reason, in.Status.Message = v1alpha1.AddonFailed, reason, msg
	in.Status.ObservedGeneration = in.Generation
	return r.Status().Update(ctx, in)
}

func (r *AddonInstallReconciler) jobConfig() addons.JobConfig {
	return addons.JobConfig{
		Namespace: r.Config.SystemNamespace,
		Image:     r.Config.Image,
		Timeout:   r.Config.Addons.JobTimeout.Duration,
		Resources: r.Config.Addons.JobResources,
		PodSecurityContext: &corev1.PodSecurityContext{
			RunAsNonRoot: ptr.To(true), RunAsUser: ptr.To(runAsUser), RunAsGroup: ptr.To(runAsUser),
			SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
		},
		ContainerSecurityContext: &corev1.SecurityContext{
			AllowPrivilegeEscalation: ptr.To(false), ReadOnlyRootFilesystem: ptr.To(true),
			Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
		},
	}
}

// startRun 写 spec ConfigMap 并建 Job；install / upgrade 前先建 generate 的 Secret。
func (r *AddonInstallReconciler) startRun(ctx context.Context, in *v1alpha1.AddonInstall, tpl *v1alpha1.AddonTemplate, action string) error {
	if action != "uninstall" && len(tpl.Spec.Generate) > 0 {
		vcl, err := r.Clients.For(ctx, in.Spec.VCluster)
		if err != nil {
			return err
		}
		if err := addons.EnsureGenerated(ctx, vcl, tpl); err != nil {
			return err
		}
	}
	timeout := r.Config.Addons.JobTimeout.Duration
	spec, err := addons.BuildRunSpec(tpl, in, action, timeout)
	if err != nil {
		return err
	}
	cm, err := addons.BuildSpecConfigMap(in, spec, r.Config.SystemNamespace)
	if err != nil {
		return err
	}
	existing := &corev1.ConfigMap{}
	switch err := r.Get(ctx, client.ObjectKeyFromObject(cm), existing); {
	case apierrors.IsNotFound(err):
		if err := r.Create(ctx, cm); err != nil {
			return err
		}
	case err != nil:
		return err
	default:
		existing.Data = cm.Data
		if err := r.Update(ctx, existing); err != nil {
			return err
		}
	}
	cfg := r.jobConfig()
	job := addons.BuildJob(in, tpl, cfg, r.suffix())
	job.Labels[labelAddonAction] = action
	job.Annotations = map[string]string{annotationAddonGeneration: strconv.FormatInt(in.Generation, 10), annotationAddonVersion: in.Spec.Version,
		annotationAddonSpecHash: runSpecHash(spec)}
	if err := r.Create(ctx, job); err != nil {
		return err
	}
	if action == "uninstall" {
		in.Status.Phase = v1alpha1.AddonUninstalling
	} else {
		in.Status.Phase = v1alpha1.AddonInstalling
		in.Status.Release, in.Status.Namespace = spec.Release, spec.Namespace
		in.Status.ObservedGeneration = in.Generation
	}
	in.Status.Reason, in.Status.Message, in.Status.Job = "", "", job.Name
	return r.Status().Update(ctx, in)
}

// currentJob 最新的那个 Job：按标签直读 apiserver 取创建时间最新者。status.job 可能因缓存滞后为空或过时，
// 以实际存在的 Job 为准，并把它记回 status（同一时刻最多一个活动 Job 的不变量靠这里保证）。
func (r *AddonInstallReconciler) currentJob(ctx context.Context, in *v1alpha1.AddonInstall) (*batchv1.Job, error) {
	list := &batchv1.JobList{}
	if err := r.jobReader().List(ctx, list, client.InNamespace(r.Config.SystemNamespace),
		client.MatchingLabels{names.LabelAddonInstall: in.Name}); err != nil {
		return nil, err
	}
	var newest *batchv1.Job
	for i := range list.Items {
		j := &list.Items[i]
		if !j.DeletionTimestamp.IsZero() || !metav1.IsControlledBy(j, in) {
			continue // 同名重建前的上一代留下的 Job 不认
		}
		if newest == nil || j.CreationTimestamp.After(newest.CreationTimestamp.Time) ||
			(j.CreationTimestamp.Equal(&newest.CreationTimestamp) && j.Name == in.Status.Job) {
			newest = j
		}
	}
	return newest, nil
}

// adoptJob status.job 与实际最新 Job 不一致时改正；Job 还在跑则 phase 跟着它的动作。
// 只认领「status 里还没记 Job」或「仍在运行」的 Job：status 记着的 Job 不见了、只剩更早的已完成 Job 时，
// 那个旧 Job 不是本次运行的结果，返回 nil 让调用方按「本次 Job 不见了」处理。
func (r *AddonInstallReconciler) adoptJob(ctx context.Context, in *v1alpha1.AddonInstall, j *batchv1.Job) (*batchv1.Job, error) {
	if j == nil || in.Status.Job == j.Name {
		return j, nil
	}
	if in.Status.Job != "" && jobFinished(j) {
		return nil, nil
	}
	in.Status.Job = j.Name
	if g, err := strconv.ParseInt(j.Annotations[annotationAddonGeneration], 10, 64); err == nil && j.Labels[labelAddonAction] != "uninstall" {
		in.Status.ObservedGeneration = g // 这次运行属于哪个 generation，回填后完成时才认得出
	}
	if !jobFinished(j) {
		if j.Labels[labelAddonAction] == "uninstall" {
			in.Status.Phase = v1alpha1.AddonUninstalling
		} else {
			in.Status.Phase = v1alpha1.AddonInstalling
		}
	}
	return j, r.Status().Update(ctx, in)
}

func jobFinished(j *batchv1.Job) bool {
	_, finished := jobOutcome(j)
	return finished
}

// jobOutcome (成功?, 已结束?)；失败时 reason 由 jobFailReason 取。
func jobOutcome(j *batchv1.Job) (bool, bool) {
	for _, c := range j.Status.Conditions {
		if c.Status != corev1.ConditionTrue {
			continue
		}
		switch c.Type {
		case batchv1.JobComplete:
			return true, true
		case batchv1.JobFailed:
			return false, true
		}
	}
	return false, false
}

func jobFailReason(j *batchv1.Job) string {
	for _, c := range j.Status.Conditions {
		if c.Type == batchv1.JobFailed && c.Status == corev1.ConditionTrue && c.Reason == batchv1.JobReasonDeadlineExceeded {
			return AddonReasonTimeout
		}
	}
	return AddonReasonJobFailed
}

// jobMessage Pod 的 termination message：能解析为 runner.Result 则拼 message 与日志末尾，否则原文截断。
func (r *AddonInstallReconciler) jobMessage(ctx context.Context, j *batchv1.Job) string {
	pods := &corev1.PodList{}
	if err := r.List(ctx, pods, client.InNamespace(j.Namespace), client.MatchingLabels{"job-name": j.Name}); err != nil || len(pods.Items) == 0 {
		return ""
	}
	sort.Slice(pods.Items, func(a, b int) bool {
		return pods.Items[a].CreationTimestamp.After(pods.Items[b].CreationTimestamp.Time)
	})
	for _, cs := range pods.Items[0].Status.ContainerStatuses {
		if cs.State.Terminated == nil {
			continue
		}
		raw := cs.State.Terminated.Message
		var res runner.Result
		if json.Unmarshal([]byte(raw), &res) == nil && (res.Message != "" || res.LogTail != "") {
			raw = res.Message
			if res.LogTail != "" {
				raw += "\n" + res.LogTail
			}
		}
		if len(raw) > addonMessageMax {
			raw = raw[len(raw)-addonMessageMax:]
		}
		return raw
	}
	return ""
}

func (r *AddonInstallReconciler) finishRun(ctx context.Context, in *v1alpha1.AddonInstall, tpl *v1alpha1.AddonTemplate, vc *v1alpha1.VirtualCluster, j *batchv1.Job) (ctrl.Result, error) {
	ok, _ := jobOutcome(j)
	if !ok {
		in.Status.Phase = v1alpha1.AddonFailed
		in.Status.Reason = jobFailReason(j)
		in.Status.Message = r.jobMessage(ctx, j)
		if in.Status.Reason == AddonReasonTimeout && in.Status.Message == "" {
			in.Status.Message = "安装超时"
		}
		return ctrl.Result{}, r.Status().Update(ctx, in)
	}
	in.Status.Phase, in.Status.Reason, in.Status.Message = v1alpha1.AddonReady, "", ""
	in.Status.InstalledVersion = in.Spec.Version
	if v := j.Annotations[annotationAddonVersion]; v != "" {
		in.Status.InstalledVersion = v // 以 Job 实际装的版本为准（Installing 中改过 spec.version 时不记错）
	}
	in.Status.InstalledSpecHash = j.Annotations[annotationAddonSpecHash]
	if err := r.Status().Update(ctx, in); err != nil || tpl == nil {
		return ctrl.Result{}, err
	}
	return r.refreshReady(ctx, in, tpl, vc)
}

// refreshReady 刷新 outputs 与 availableVersion；变化才写。
func (r *AddonInstallReconciler) refreshReady(ctx context.Context, in *v1alpha1.AddonInstall, tpl *v1alpha1.AddonTemplate, vc *v1alpha1.VirtualCluster) (ctrl.Result, error) {
	before, _ := json.Marshal(in.Status)
	in.Status.AvailableVersion = ""
	if tpl.Spec.Chart.Version != in.Status.InstalledVersion {
		in.Status.AvailableVersion = tpl.Spec.Chart.Version
	}
	// 版本没变但内容（values、release 位置）变了：标「有更新」，不自动装
	in.Status.UpdateAvailable = false
	if in.Status.InstalledSpecHash != "" && in.Status.AvailableVersion == "" {
		if spec, err := addons.BuildRunSpec(tpl, in, "upgrade", r.Config.Addons.JobTimeout.Duration); err == nil {
			in.Status.UpdateAvailable = runSpecHash(spec) != in.Status.InstalledSpecHash
		}
	}
	if vcl, err := r.Clients.For(ctx, in.Spec.VCluster); err == nil {
		if outs, err := addons.ResolveOutputs(ctx, vcl, tpl, addons.ClusterInfo{Name: vc.Name, AppsDomain: vc.Status.AppsDomain}); err == nil {
			in.Status.Outputs = outs
		} else {
			logf.FromContext(ctx).Info("resolve addon outputs failed", "install", in.Name, "err", err.Error())
		}
	}
	after, _ := json.Marshal(in.Status)
	if string(before) != string(after) {
		if err := r.Status().Update(ctx, in); err != nil {
			return ctrl.Result{}, err
		}
	}
	return ctrl.Result{RequeueAfter: addonReadyResync}, nil
}

// reconcileDelete 卸载；集群已不在时直接摘 finalizer。
func (r *AddonInstallReconciler) reconcileDelete(ctx context.Context, in *v1alpha1.AddonInstall, tpl *v1alpha1.AddonTemplate, vc *v1alpha1.VirtualCluster, vcGone bool) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(in, names.AddonFinalizer) {
		return ctrl.Result{}, nil
	}
	drop := func() (ctrl.Result, error) {
		controllerutil.RemoveFinalizer(in, names.AddonFinalizer)
		return ctrl.Result{}, client.IgnoreNotFound(r.Update(ctx, in))
	}
	if vcGone || (in.Status.Release == "" && in.Status.InstalledVersion == "") {
		return drop()
	}
	if vc.Status.Phase != v1alpha1.PhaseRunning {
		// 集群不在运行：卸载 Job 连不上 apiserver，等它恢复（或集群被删时直接摘 finalizer）
		msg := fmt.Sprintf("集群不在运行中（%s），恢复后再卸载", vc.Status.Phase)
		if in.Status.Message != msg || in.Status.Phase != v1alpha1.AddonUninstalling {
			in.Status.Phase, in.Status.Message = v1alpha1.AddonUninstalling, msg
			if err := r.Status().Update(ctx, in); err != nil {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{RequeueAfter: time.Minute}, nil
	}
	job, err := r.currentJob(ctx, in)
	if err != nil {
		return ctrl.Result{}, err
	}
	if job, err = r.adoptJob(ctx, in, job); err != nil {
		return ctrl.Result{}, err
	}
	if job != nil && job.Labels[labelAddonAction] != "uninstall" {
		if !jobFinished(job) {
			// 安装还在跑：停掉它再卸载
			if err := r.Delete(ctx, job, client.PropagationPolicy("Background")); client.IgnoreNotFound(err) != nil {
				return ctrl.Result{}, err
			}
		}
		job = nil
	}
	if job != nil && !jobFinished(job) {
		return ctrl.Result{}, nil
	}
	if job != nil {
		if ok, _ := jobOutcome(job); ok {
			return drop()
		}
		// 卸载失败：指数退避后重来
		failed, err := r.failedUninstallJobs(ctx, in)
		if err != nil {
			return ctrl.Result{}, err
		}
		wait := 10 * time.Second << min(failed-1, 5)
		if wait > addonUninstallMaxBO {
			wait = addonUninstallMaxBO
		}
		done := job.CreationTimestamp.Time
		if job.Status.CompletionTime != nil {
			done = job.Status.CompletionTime.Time
		}
		for _, c := range job.Status.Conditions {
			if c.Type == batchv1.JobFailed {
				done = c.LastTransitionTime.Time
			}
		}
		if remaining := wait - r.now().Sub(done); remaining > 0 {
			msg := r.jobMessage(ctx, job)
			if in.Status.Message != msg {
				in.Status.Message = msg
				if err := r.Status().Update(ctx, in); err != nil {
					return ctrl.Result{}, err
				}
			}
			return ctrl.Result{RequeueAfter: remaining}, nil
		}
	}
	if err := r.startRun(ctx, in, tpl, "uninstall"); err != nil {
		r.reportStartError(ctx, in, err)
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

func (r *AddonInstallReconciler) failedUninstallJobs(ctx context.Context, in *v1alpha1.AddonInstall) (int, error) {
	list := &batchv1.JobList{}
	if err := r.List(ctx, list, client.InNamespace(r.Config.SystemNamespace),
		client.MatchingLabels{names.LabelAddonInstall: in.Name, labelAddonAction: "uninstall"}); err != nil {
		return 0, err
	}
	n := 0
	for i := range list.Items {
		if ok, done := jobOutcome(&list.Items[i]); done && !ok {
			n++
		}
	}
	if n < 1 {
		n = 1
	}
	return n, nil
}

// runSpecHash 一次安装真正决定结果的内容：chart、版本、release、namespace、合并后的 values。
func runSpecHash(s runner.RunSpec) string {
	h := sha256.Sum256([]byte(strings.Join([]string{s.Chart.Repository, s.Chart.Name, s.Chart.Version, s.Release, s.Namespace, s.Values}, "\x00")))
	return hex.EncodeToString(h[:8])
}
