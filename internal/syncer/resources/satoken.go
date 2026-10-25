package resources

import (
	"context"
	"fmt"
	"time"

	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"vk8s.io/vk8s/internal/names"
	"vk8s.io/vk8s/internal/syncer/translate"
)

const (
	// LabelSATokenPod 宿主 SA token Secret 上记录所属宿主 Pod 名；带此标签的 Secret 不属于 Secret reconciler。
	LabelSATokenPod = names.LabelPrefix + "sa-token-pod"
	// AnnotationTokenIssuedAt / AnnotationTokenExpiresAt token 签发与到期时间（RFC3339），用于计算续签时机。
	AnnotationTokenIssuedAt  = names.LabelPrefix + "token-issued-at"
	AnnotationTokenExpiresAt = names.LabelPrefix + "token-expires-at"

	// minRenewRequeue 续签检查的最短重入间隔。
	minRenewRequeue = time.Minute
)

// syncPodIdentityData 把平台 CA 与虚拟 Pod 的 namespace / name / uid 写进 token Secret 的数据，
// 供 SA 目录与 downwardAPI 卷读取（宿主 kubelet 只知道宿主 Pod 的值）。返回是否有改动。
func syncPodIdentityData(data map[string][]byte, vpod *corev1.Pod, ca []byte) bool {
	want := map[string][]byte{
		translate.SATokenKeyCA:        ca,
		translate.SATokenKeyNamespace: []byte(vpod.Namespace),
		translate.SATokenKeyPodName:   []byte(vpod.Name),
		translate.SATokenKeyPodUID:    []byte(vpod.UID),
	}
	changed := false
	for k, v := range want {
		if cur, ok := data[k]; !ok || string(cur) != string(v) {
			data[k] = v
			changed = true
		}
	}
	return changed
}

// IsSATokenSecret 是否为 syncer 签发的 SA token Secret。
func IsSATokenSecret(s *corev1.Secret) bool {
	return s.Labels[LabelSATokenPod] != ""
}

// ensureSAToken 保证宿主上存在承载虚拟 SA token 的 Secret，并在有效期过半时续签。
// 返回下次需要检查续签的间隔。hpod 非 nil 时给 Secret 挂 ownerReference。
func (r *podReconciler) ensureSAToken(ctx context.Context, vpod *corev1.Pod, hostPodName string, hpod *corev1.Pod) (time.Duration, error) {
	name := names.SATokenSecretName(hostPodName)
	now := time.Now()

	secret := &corev1.Secret{}
	err := r.c.Host.Get(ctx, r.c.HostKey(name), secret)
	if err != nil && !isNotFound(err) {
		return 0, err
	}

	if isNotFound(err) {
		token, exp, err := r.requestToken(ctx, vpod)
		if err != nil {
			SATokenRenewFailures.Inc()
			return 0, fmt.Errorf("request service account token: %w", err)
		}
		secret = &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: r.c.HostNamespace,
				Labels:    saTokenSecretLabels(vpod, hostPodName),
				Annotations: map[string]string{
					AnnotationTokenIssuedAt:  now.UTC().Format(time.RFC3339),
					AnnotationTokenExpiresAt: exp.UTC().Format(time.RFC3339),
				},
			},
			Type: corev1.SecretTypeOpaque,
			Data: map[string][]byte{translate.SATokenKeyToken: []byte(token)},
		}
		syncPodIdentityData(secret.Data, vpod, r.c.PlatformCA)
		if hpod != nil {
			secret.OwnerReferences = []metav1.OwnerReference{podSecretOwner(hpod)}
		}
		if err := r.c.Host.Create(ctx, secret); err != nil {
			return 0, err
		}
		return renewAfter(now, exp, now), nil
	}

	if !IsSATokenSecret(secret) {
		return 0, fmt.Errorf("host secret %s exists but is not a vk8s service account token secret", name)
	}

	changed := false
	if hpod != nil && !hasOwner(secret, hpod.UID) {
		secret.OwnerReferences = []metav1.OwnerReference{podSecretOwner(hpod)}
		changed = true
	}
	// 平台 CA 轮换或虚拟 Pod 身份字段缺失时补齐（旧版本签发的 Secret 没有 pod-name / pod-uid）
	if secret.Data == nil {
		secret.Data = map[string][]byte{}
	}
	if syncPodIdentityData(secret.Data, vpod, r.c.PlatformCA) {
		changed = true
	}

	issued, exp := parseTokenTimes(secret)
	if now.After(renewAt(issued, exp)) || len(secret.Data[translate.SATokenKeyToken]) == 0 {
		token, newExp, err := r.requestToken(ctx, vpod)
		if err != nil {
			SATokenRenewFailures.Inc()
			return 0, fmt.Errorf("renew service account token: %w", err)
		}
		if secret.Data == nil {
			secret.Data = map[string][]byte{}
		}
		secret.Data[translate.SATokenKeyToken] = []byte(token)
		if secret.Annotations == nil {
			secret.Annotations = map[string]string{}
		}
		secret.Annotations[AnnotationTokenIssuedAt] = now.UTC().Format(time.RFC3339)
		secret.Annotations[AnnotationTokenExpiresAt] = newExp.UTC().Format(time.RFC3339)
		issued, exp = now, newExp
		changed = true
	}
	if changed {
		if err := r.c.Host.Update(ctx, secret); err != nil {
			return 0, err
		}
	}
	return renewAfter(issued, exp, now), nil
}

// requestToken 对虚拟 SA 调 TokenRequest：默认 audience、1 小时有效、绑定到虚拟 Pod（Pod 删除即失效，与 kubelet 行为一致）。
func (r *podReconciler) requestToken(ctx context.Context, vpod *corev1.Pod) (string, time.Time, error) {
	tr := &authenticationv1.TokenRequest{
		Spec: authenticationv1.TokenRequestSpec{
			ExpirationSeconds: ptr.To[int64](names.ServiceAccountTokenTTLSeconds),
			BoundObjectRef: &authenticationv1.BoundObjectReference{
				Kind:       "Pod",
				APIVersion: "v1",
				Name:       vpod.Name,
				UID:        vpod.UID,
			},
		},
	}
	res, err := r.c.VirtualClientset.CoreV1().ServiceAccounts(vpod.Namespace).
		CreateToken(ctx, translate.ServiceAccountNameOf(vpod), tr, metav1.CreateOptions{})
	if err != nil {
		return "", time.Time{}, err
	}
	exp := res.Status.ExpirationTimestamp.Time
	if exp.IsZero() {
		exp = time.Now().Add(time.Duration(names.ServiceAccountTokenTTLSeconds) * time.Second)
	}
	return res.Status.Token, exp, nil
}

// deleteSATokenSecret 删除宿主 Pod 对应的 token Secret（ownerReference 也会兜底，这里让清理更及时）。
func (r *podReconciler) deleteSATokenSecret(ctx context.Context, hostPodName string) error {
	secret := &corev1.Secret{}
	if err := r.c.Host.Get(ctx, r.c.HostKey(names.SATokenSecretName(hostPodName)), secret); err != nil {
		return client.IgnoreNotFound(err)
	}
	if !IsSATokenSecret(secret) {
		return nil
	}
	return client.IgnoreNotFound(r.c.Host.Delete(ctx, secret))
}

func parseTokenTimes(secret *corev1.Secret) (issued, exp time.Time) {
	issued, _ = time.Parse(time.RFC3339, secret.Annotations[AnnotationTokenIssuedAt])
	exp, _ = time.Parse(time.RFC3339, secret.Annotations[AnnotationTokenExpiresAt])
	return issued, exp
}

// renewAt 到期前 50% 续签；时间戳缺失或不合理时立即续签。
func renewAt(issued, exp time.Time) time.Time {
	if issued.IsZero() || exp.IsZero() || !exp.After(issued) {
		return time.Time{}
	}
	return issued.Add(exp.Sub(issued) / 2)
}

// renewAfter 距下次续签检查的间隔，下限 1 分钟。
func renewAfter(issued, exp, now time.Time) time.Duration {
	d := renewAt(issued, exp).Sub(now)
	if d < minRenewRequeue {
		return minRenewRequeue
	}
	return d
}

func hasOwner(obj metav1.Object, uid types.UID) bool {
	for _, o := range obj.GetOwnerReferences() {
		if o.UID == uid {
			return true
		}
	}
	return false
}
