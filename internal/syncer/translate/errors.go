// Package translate 实现虚拟集群对象与宿主集群对象之间的双向翻译。
//
// 本包只包含纯函数：不持有 client、不访问网络，输入虚拟对象与必要上下文，输出宿主对象
// （或反向）。所有需要查询集群的信息（ClusterIP、允许的节点集合等）由调用方查好后以
// 参数传入，便于表驱动单元测试。
package translate

import (
	"errors"
	"fmt"
)

// RejectError 表示翻译阶段的拒绝：对象本身不合规（nodeName 不在白名单、Ingress host 不合规等）。
// 调用方应在虚拟对象上写 Event `SyncRejected`，且在对象不变时不再重试。
type RejectError struct {
	Reason string
}

// Error 实现 error。
func (e *RejectError) Error() string { return e.Reason }

// Rejectf 构造 RejectError。
func Rejectf(format string, args ...any) error {
	return &RejectError{Reason: fmt.Sprintf(format, args...)}
}

// IsReject 判断 err 是否为（或包装了）RejectError。
func IsReject(err error) bool {
	var re *RejectError
	return errors.As(err, &re)
}

// NotReadyError 表示翻译所依赖的集群状态尚未就绪（例如 kube-dns 的 ClusterIP 还没分配）。
// 它不是对象的错，调用方应稍后重试，且不必写 Event。
type NotReadyError struct {
	What string
}

// Error 实现 error。
func (e *NotReadyError) Error() string { return e.What + " is not ready yet" }

// IsNotReady 判断 err 是否为 NotReadyError。
func IsNotReady(err error) bool {
	var ne *NotReadyError
	return errors.As(err, &ne)
}
