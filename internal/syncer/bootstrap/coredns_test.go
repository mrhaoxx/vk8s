package bootstrap

import (
	"strings"
	"testing"
)

// Service 上声明了 prometheus.io/port=9153，Corefile 必须真的在 9153 暴露指标，否则 Prometheus 抓到 up=0。
func TestCorefileExposesMetrics(t *testing.T) {
	if !strings.Contains(corefile("172.27.0.10"), "prometheus :9153") {
		t.Fatalf("Corefile must enable the prometheus plugin on :9153:\n%s", corefile("172.27.0.10"))
	}
}
