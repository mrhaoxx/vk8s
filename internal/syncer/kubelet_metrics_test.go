package syncer

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"vk8s.io/vk8s/internal/names"
	"vk8s.io/vk8s/internal/pki"
)

func TestNodeForIPMatchesAnyNodeAddress(t *testing.T) {
	n := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "m602"}, Status: corev1.NodeStatus{Addresses: []corev1.NodeAddress{
		{Type: corev1.NodeInternalIP, Address: "172.25.4.66"}, {Type: corev1.NodeHostName, Address: "m602"}}}}
	f := nodeForIP(fake.NewClientBuilder().WithObjects(n).Build())
	if name, ok := f(context.Background(), "172.25.4.66"); !ok || name != "m602" {
		t.Fatalf("got %q %v", name, ok)
	}
	if _, ok := f(context.Background(), "10.9.9.9"); ok {
		t.Fatal("unknown ip must not match")
	}
	if _, ok := f(context.Background(), ""); ok {
		t.Fatal("empty ip must not match")
	}
}

func TestHostPodLookupUsesVirtualNameLabels(t *testing.T) {
	p := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "vc-alice", Name: "web-x-demo", Labels: map[string]string{
		names.LabelManaged: "true", names.LabelVName: "web", names.LabelVNamespace: "demo"}}}
	f := hostPodLookup(fake.NewClientBuilder().WithObjects(p).Build(), "vc-alice")
	if ns, name, ok := f(context.Background(), "web-x-demo"); !ok || ns != "demo" || name != "web" {
		t.Fatalf("got %q %q %v", ns, name, ok)
	}
	if _, _, ok := f(context.Background(), "nope"); ok {
		t.Fatal("missing pod must not match")
	}
}

func TestManagerMetricsUsesClientCertAndPath(t *testing.T) {
	ca, _ := pki.NewCA("platform")
	server, _ := ca.Sign(pki.CertConfig{CommonName: "manager", IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, Usage: pki.UsageServer})
	clientCert, _ := ca.Sign(pki.CertConfig{CommonName: "vc-alice", Usage: pki.UsageClient})
	var gotCN, gotPath string
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotCN, gotPath = r.TLS.PeerCertificates[0].Subject.CommonName, r.URL.Path
		_, _ = io.WriteString(w, "machine_cpu_cores 8\n")
	}))
	sc, _ := tls.X509KeyPair(server.CertPEM, server.KeyPEM)
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(ca.CertPEM)
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{sc}, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: pool}
	srv.StartTLS()
	defer srv.Close()

	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, names.KeyWebhookClientCert), clientCert.CertPEM, 0o600)
	_ = os.WriteFile(filepath.Join(dir, names.KeyWebhookClientKey), clientCert.KeyPEM, 0o600)
	fetch, err := managerMetrics(srv.URL, "alice", dir, ca.CertPEM)
	if err != nil {
		t.Fatal(err)
	}
	body, code, err := fetch(context.Background(), "m602", "cadvisor")
	if err != nil || code != 200 {
		t.Fatalf("code=%d err=%v", code, err)
	}
	b, _ := io.ReadAll(body)
	body.Close()
	if gotCN != "vc-alice" || gotPath != "/kubelet/alice/nodes/m602/metrics/cadvisor" || string(b) != "machine_cpu_cores 8\n" {
		t.Fatalf("cn=%s path=%s body=%q", gotCN, gotPath, b)
	}
}
