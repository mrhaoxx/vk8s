package controllers

import (
	"fmt"

	ctrl "sigs.k8s.io/controller-runtime"

	"vk8s.io/vk8s/internal/config"
	"vk8s.io/vk8s/internal/pki"
)

// Setup 注册 manager 进程内的全部 reconciler。
func Setup(mgr ctrl.Manager, cfg *config.Config, platformCA *pki.CA) error {
	vcr := &VirtualClusterReconciler{
		Client:     mgr.GetClient(),
		Scheme:     mgr.GetScheme(),
		Config:     cfg,
		PlatformCA: platformCA,
		Recorder:   mgr.GetEventRecorderFor(ManagedByValue),
	}
	if err := vcr.SetupWithManager(mgr); err != nil {
		return fmt.Errorf("setup virtualcluster controller: %w", err)
	}
	gc := &AccessTokenGCReconciler{
		Client:    mgr.GetClient(),
		Retention: DefaultTokenRetention,
	}
	if err := gc.SetupWithManager(mgr); err != nil {
		return fmt.Errorf("setup accesstoken gc controller: %w", err)
	}
	nb := &NodeBookingReconciler{
		Client:    mgr.GetClient(),
		Scheme:    mgr.GetScheme(),
		Recorder:  mgr.GetEventRecorderFor("vk8s-booking"),
		Retention: cfg.Booking.Retention.Duration,
	}
	if err := nb.SetupWithManager(mgr); err != nil {
		return fmt.Errorf("setup nodebooking controller: %w", err)
	}
	ai := &AddonInstallReconciler{
		Client:    mgr.GetClient(),
		Scheme:    mgr.GetScheme(),
		Config:    cfg,
		APIReader: mgr.GetAPIReader(),
		Clients: &AdminKubeconfigClients{
			Reader: mgr.GetAPIReader(), Scheme: mgr.GetScheme(), SystemNamespace: cfg.SystemNamespace,
		},
	}
	if err := ai.SetupWithManager(mgr); err != nil {
		return fmt.Errorf("setup addoninstall controller: %w", err)
	}
	return nil
}
