package kubeconfig

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

// fakeAPIServer answers /readyz and /version like a Kubernetes API server.
// It reports not ready for the first notReady requests to /readyz.
func fakeAPIServer(t *testing.T, notReady int32, wantToken string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+wantToken {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"kind":"Status","apiVersion":"v1","status":"Failure","message":"Unauthorized","reason":"Unauthorized","code":401}`))
			return
		}
		switch r.URL.Path {
		case "/readyz":
			if calls.Add(1) <= notReady {
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = w.Write([]byte("[-]etcd failed: not ready yet"))
				return
			}
			_, _ = w.Write([]byte("ok"))
		case "/version":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"major":"1","minor":"34","gitVersion":"v1.34.1-gke.1200"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

func configFor(srv *httptest.Server, token string) *clientcmdapi.Config {
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	cfg := clientcmdapi.NewConfig()
	cfg.Clusters["tuggy-dev"] = &clientcmdapi.Cluster{Server: srv.URL, CertificateAuthorityData: ca}
	cfg.AuthInfos["tuggy-dev"] = &clientcmdapi.AuthInfo{Token: token}
	cfg.Contexts["tuggy-dev"] = &clientcmdapi.Context{Cluster: "tuggy-dev", AuthInfo: "tuggy-dev"}
	cfg.CurrentContext = "tuggy-dev"
	return cfg
}

func TestWaitReadyImmediately(t *testing.T) {
	srv, _ := fakeAPIServer(t, 0, "good")
	version, err := WaitReady(context.Background(), configFor(srv, "good"), ReadyOptions{Interval: 10 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if version != "v1.34.1-gke.1200" {
		t.Errorf("version = %q", version)
	}
}

func TestWaitReadyAfterRetries(t *testing.T) {
	srv, calls := fakeAPIServer(t, 3, "good")
	var attempts []int
	version, err := WaitReady(context.Background(), configFor(srv, "good"), ReadyOptions{
		Interval:  10 * time.Millisecond,
		OnAttempt: func(n int, _ error) { attempts = append(attempts, n) },
	})
	if err != nil || version == "" {
		t.Fatalf("version %q, err %v", version, err)
	}
	if len(attempts) != 3 || calls.Load() != 4 {
		t.Errorf("failed attempts reported %v, /readyz calls %d; want 3 failures then success", attempts, calls.Load())
	}
}

func TestWaitReadyTimesOut(t *testing.T) {
	srv, _ := fakeAPIServer(t, 1_000_000, "good")
	start := time.Now()
	_, err := WaitReady(context.Background(), configFor(srv, "good"), ReadyOptions{Timeout: 300 * time.Millisecond, Interval: 50 * time.Millisecond})
	if err == nil || !strings.Contains(err.Error(), "did not become ready within 300ms") || !strings.Contains(err.Error(), "/readyz") {
		t.Errorf("err = %v", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Errorf("took %s", time.Since(start))
	}
}

func TestWaitReadyUnauthorized(t *testing.T) {
	srv, _ := fakeAPIServer(t, 0, "good")
	_, err := WaitReady(context.Background(), configFor(srv, "wrong"), ReadyOptions{Timeout: 200 * time.Millisecond, Interval: 50 * time.Millisecond})
	if err == nil || !strings.Contains(err.Error(), "rejected the credentials (Unauthorized)") {
		t.Errorf("err = %v, want the authorization failure explained", err)
	}
}

func TestWaitReadyCancelled(t *testing.T) {
	srv, _ := fakeAPIServer(t, 1_000_000, "good")
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(100*time.Millisecond, cancel)
	_, err := WaitReady(ctx, configFor(srv, "good"), ReadyOptions{Interval: 20 * time.Millisecond})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

func TestWaitReadyRejectsUnknownCA(t *testing.T) {
	srv, _ := fakeAPIServer(t, 0, "good")
	cfg := configFor(srv, "good")
	cfg.Clusters["tuggy-dev"].CertificateAuthorityData = otherCA(t)

	_, err := WaitReady(context.Background(), cfg, ReadyOptions{Timeout: 200 * time.Millisecond, Interval: 50 * time.Millisecond})
	if err == nil || !strings.Contains(err.Error(), "certificate") {
		t.Errorf("err = %v, want a certificate error: the CA from the cluster must be checked", err)
	}
}

// otherCA returns a freshly generated CA certificate that did not sign the
// test server's certificate.
func otherCA(t *testing.T) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "some other CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}
