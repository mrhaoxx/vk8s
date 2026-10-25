package resources

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"
)

// SetupAll 在虚拟 cache 上建索引并注册全部 reconciler。
func SetupAll(ctx context.Context, mgr ctrl.Manager, c *Context) error {
	if err := RegisterIndexes(ctx, mgr.GetFieldIndexer()); err != nil {
		return fmt.Errorf("register virtual indexes: %w", err)
	}
	steps := []struct {
		name string
		fn   func(ctrl.Manager, *Context) error
	}{
		{"pod", setupPod},
		{"service", setupService},
		{"endpoints", setupEndpoints},
		{"endpointslice", setupEndpointSlice},
		{"configmap", setupConfigMap},
		{"secret", setupSecret},
		{"persistentvolumeclaim", setupPVC},
		{"ingress", setupIngress},
		{"event", setupEvent},
		{"node", setupNode},
		{"storageclass", setupStorageClass},
		{"ingressclass", setupIngressClass},
		{"runtimeclass", setupRuntimeClass},
		{"limitrange", setupLimitRange},
	}
	for _, s := range steps {
		if err := s.fn(mgr, c); err != nil {
			return fmt.Errorf("setup %s controller: %w", s.name, err)
		}
	}
	return nil
}

func isAlreadyExists(err error) bool { return apierrors.IsAlreadyExists(err) }
