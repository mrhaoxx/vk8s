// Package kubeletmetrics 过滤与改写宿主 kubelet 的 cAdvisor / resource 指标（设计 2026-10-26）。
//
// Filter 在 manager 里做，是隔离边界：只留本集群宿主 ns 的序列与 machine_* 硬件规格。
// Rename 在 syncer 里做：把宿主 namespace / pod 名翻回虚拟集群里的名字。
package kubeletmetrics

import (
	"bytes"
	"fmt"
	"io"
	"sort"
	"strings"

	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"
)

// ContentType 返回的文本格式。
const ContentType = "text/plain; version=0.0.4; charset=utf-8"

func parse(r io.Reader) (map[string]*dto.MetricFamily, error) {
	p := expfmt.NewTextParser(model.UTF8Validation)
	fams, err := p.TextToMetricFamilies(r)
	if err != nil {
		return nil, fmt.Errorf("parse metrics: %w", err)
	}
	return fams, nil
}

func label(m *dto.Metric, name string) (string, bool) {
	for _, l := range m.GetLabel() {
		if l.GetName() == name {
			return l.GetValue(), true
		}
	}
	return "", false
}

func encode(fams map[string]*dto.MetricFamily) ([]byte, error) {
	names := make([]string, 0, len(fams))
	for n, f := range fams {
		if len(f.GetMetric()) > 0 {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	var b bytes.Buffer
	for _, n := range names {
		if _, err := expfmt.MetricFamilyToText(&b, fams[n]); err != nil {
			return nil, err
		}
	}
	return b.Bytes(), nil
}

// Filter 只留 namespace == hostNamespace 的样本；不带 namespace（或为空）的只留 machine_* 指标。
// 解析失败返回错误，调用方不得转出原文。
func Filter(r io.Reader, hostNamespace string) ([]byte, error) {
	fams, err := parse(r)
	if err != nil {
		return nil, err
	}
	for name, f := range fams {
		kept := f.Metric[:0]
		for _, m := range f.GetMetric() {
			ns, _ := label(m, "namespace")
			switch {
			case ns == hostNamespace && ns != "":
				kept = append(kept, m)
			case ns == "" && strings.HasPrefix(name, "machine_"):
				m.Label = stripLabels(m.Label, hostIdentityLabels)
				kept = append(kept, m)
			}
		}
		f.Metric = kept
	}
	return encode(fams)
}

// Rename 把 namespace == hostNamespace 的样本的 namespace / pod 换成虚拟名；lookup 找不到的样本丢弃。
func Rename(r io.Reader, hostNamespace string, lookup func(hostPod string) (vns, vname string, ok bool)) ([]byte, error) {
	fams, err := parse(r)
	if err != nil {
		return nil, err
	}
	for _, f := range fams {
		kept := f.Metric[:0]
		for _, m := range f.GetMetric() {
			ns, _ := label(m, "namespace")
			if ns != hostNamespace || ns == "" {
				kept = append(kept, m)
				continue
			}
			pod, _ := label(m, "pod")
			vns, vname, ok := lookup(pod)
			if !ok {
				continue
			}
			for _, l := range m.GetLabel() {
				switch l.GetName() {
				case "namespace":
					l.Value = &vns
				case "pod":
					l.Value = &vname
				}
			}
			kept = append(kept, m)
		}
		f.Metric = kept
	}
	return encode(fams)
}

// hostIdentityLabels machine_* 上标识宿主机器的标签：对看板无用，不给租户。
var hostIdentityLabels = map[string]bool{"machine_id": true, "system_uuid": true, "boot_id": true}

func stripLabels(ls []*dto.LabelPair, drop map[string]bool) []*dto.LabelPair {
	out := ls[:0]
	for _, l := range ls {
		if !drop[l.GetName()] {
			out = append(out, l)
		}
	}
	return out
}
