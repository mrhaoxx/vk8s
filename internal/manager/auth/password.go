package auth

import (
	"context"
	"errors"
	"fmt"

	"golang.org/x/crypto/bcrypt"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"vk8s.io/vk8s/api/v1alpha1"
	"vk8s.io/vk8s/internal/names"
)

// PasswordSecretKey 静态用户密码 Secret 中存放 bcrypt 哈希的 key。
const PasswordSecretKey = "bcrypt"

// MinPasswordLength 新设密码的最小长度。
const MinPasswordLength = 8

// HashPassword 生成 bcrypt 哈希。
func HashPassword(password string) ([]byte, error) {
	if len(password) < MinPasswordLength {
		return nil, fmt.Errorf("password must be at least %d characters", MinPasswordLength)
	}
	if len(password) > 72 {
		// bcrypt 只处理前 72 字节，超出部分会被静默截断。
		return nil, errors.New("password must be at most 72 characters")
	}
	return bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
}

// CheckPassword 比对 bcrypt 哈希。
func CheckPassword(hash []byte, password string) bool {
	return bcrypt.CompareHashAndPassword(hash, []byte(password)) == nil
}

// dummyHash 用于用户不存在时的恒定耗时比对，避免通过响应时间枚举用户名。
var dummyHash = func() []byte {
	h, err := bcrypt.GenerateFromPassword([]byte("vk8s-dummy-password"), bcrypt.DefaultCost)
	if err != nil {
		panic(err)
	}
	return h
}()

// WritePasswordSecret 写（或覆盖）用户的 bcrypt 密码 Secret，ownerReference → User。
// Secret 名取 spec.auth.static.passwordSecretRef，未设置时用 user-<name>。
func WritePasswordSecret(ctx context.Context, c client.Client, sysNS string, user *v1alpha1.User, hash []byte) error {
	name := names.UserPasswordSecret(user.Name)
	if user.Spec.Auth.Static != nil && user.Spec.Auth.Static.PasswordSecretRef.Name != "" {
		name = user.Spec.Auth.Static.PasswordSecretRef.Name
	}
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: sysNS}}
	_, err := controllerutil.CreateOrUpdate(ctx, c, secret, func() error {
		if secret.Labels == nil {
			secret.Labels = map[string]string{}
		}
		secret.Labels[names.LabelOwner] = user.Name
		secret.Type = corev1.SecretTypeOpaque
		secret.Data = map[string][]byte{PasswordSecretKey: hash}
		secret.OwnerReferences = []metav1.OwnerReference{{
			APIVersion:         v1alpha1.GroupVersion.String(),
			Kind:               "User",
			Name:               user.Name,
			UID:                user.UID,
			BlockOwnerDeletion: ptr.To(false),
		}}
		return nil
	})
	if err != nil {
		return fmt.Errorf("write password secret %s/%s: %w", sysNS, name, err)
	}
	return nil
}
