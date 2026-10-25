package auth

import (
	"context"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"vk8s.io/vk8s/api/v1alpha1"
)

// IndexUserOIDCSubject User 上按 OIDC subject 查找的 cache 索引名。
const IndexUserOIDCSubject = "spec.auth.oidc.subject"

// RegisterIndexes 在 controller-runtime cache 上建索引，必须在 manager 启动前调用。
func RegisterIndexes(ctx context.Context, indexer client.FieldIndexer) error {
	return indexer.IndexField(ctx, &v1alpha1.User{}, IndexUserOIDCSubject, func(obj client.Object) []string {
		u, ok := obj.(*v1alpha1.User)
		if !ok || u.Spec.Auth.OIDC == nil || u.Spec.Auth.OIDC.Subject == "" {
			return nil
		}
		return []string{u.Spec.Auth.OIDC.Subject}
	})
}
