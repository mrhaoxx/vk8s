package controllers

import (
	"fmt"
	"time"

	"vk8s.io/vk8s/api/v1alpha1"
)

// bookingFacts 判定一个预约 phase 所需的全部外部事实，由 Reconcile 收集，decidePhase 只做纯计算。
type bookingFacts struct {
	Now        time.Time
	NodeExists bool
	Bookable   bool // 节点带 vk8s.io/bookable=true
	VCExists   bool
	// Others 同节点、非终态、不含自己的其他预约，用于重叠仲裁。
	Others []v1alpha1.NodeBooking
}

// phaseResult decidePhase 的结论。
type phaseResult struct {
	Phase   v1alpha1.NodeBookingPhase
	Reason  string
	Message string
}

// decidePhase 按设计文档第 4 节的顺序判定（首个命中即返回）：
// 已终态 → 原样；目标集群不存在 → Rejected；节点不存在 / 无标签 → Active 则 Revoked 否则 Rejected；
// 与更早创建的预约重叠 → Rejected/Overlap；然后按时刻分 Pending / Active / Completed。
func decidePhase(b *v1alpha1.NodeBooking, f bookingFacts) phaseResult {
	if b.IsTerminal() {
		return phaseResult{Phase: b.Status.Phase, Reason: b.Status.Reason, Message: b.Status.Message}
	}
	active := b.Status.Phase == v1alpha1.NodeBookingActive
	if !f.VCExists {
		return phaseResult{Phase: v1alpha1.NodeBookingRejected, Reason: v1alpha1.BookingReasonVClusterNotFound,
			Message: fmt.Sprintf("virtual cluster %q does not exist", b.Spec.VCluster)}
	}
	if !f.NodeExists {
		return phaseResult{Phase: rejectOrRevoke(active), Reason: v1alpha1.BookingReasonNodeNotFound,
			Message: fmt.Sprintf("node %q does not exist", b.Spec.Node)}
	}
	if !f.Bookable {
		return phaseResult{Phase: rejectOrRevoke(active), Reason: v1alpha1.BookingReasonNodeNotBookable,
			Message: fmt.Sprintf("node %q is not labeled bookable", b.Spec.Node)}
	}
	for i := range f.Others {
		o := &f.Others[i]
		if o.Name == b.Name || o.IsTerminal() || !b.Overlaps(o) {
			continue
		}
		if winsOver(o, b) {
			return phaseResult{Phase: v1alpha1.NodeBookingRejected, Reason: v1alpha1.BookingReasonOverlap,
				Message: fmt.Sprintf("overlaps booking %s (%s → %s)", o.Name, o.Spec.Start.UTC().Format(time.RFC3339), o.Spec.End.UTC().Format(time.RFC3339))}
		}
	}
	switch {
	case f.Now.Before(b.Spec.Start.Time):
		return phaseResult{Phase: v1alpha1.NodeBookingPending}
	case f.Now.Before(b.Spec.End.Time):
		return phaseResult{Phase: v1alpha1.NodeBookingActive}
	default:
		return phaseResult{Phase: v1alpha1.NodeBookingCompleted}
	}
}

func rejectOrRevoke(active bool) v1alpha1.NodeBookingPhase {
	if active {
		return v1alpha1.NodeBookingRevoked
	}
	return v1alpha1.NodeBookingRejected
}

// winsOver 重叠仲裁：创建更早者胜；同一时刻则名字字典序小者胜。
func winsOver(a, b *v1alpha1.NodeBooking) bool {
	at, bt := a.CreationTimestamp.Time, b.CreationTimestamp.Time
	if !at.Equal(bt) {
		return at.Before(bt)
	}
	return a.Name < b.Name
}

// nextBoundary 下一次需要重新判定的时刻：Pending → start；Active → end；终态 → releasedAt + retention（releasedAt 为空取 now）。
func nextBoundary(b *v1alpha1.NodeBooking, phase v1alpha1.NodeBookingPhase, now time.Time, retention time.Duration) time.Time {
	switch phase {
	case v1alpha1.NodeBookingPending:
		return b.Spec.Start.Time
	case v1alpha1.NodeBookingActive:
		return b.Spec.End.Time
	}
	base := now
	if b.Status.ReleasedAt != nil {
		base = b.Status.ReleasedAt.Time
	}
	return base.Add(retention)
}
