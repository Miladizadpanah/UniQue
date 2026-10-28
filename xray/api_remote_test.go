package xray

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"testing"
	"time"
)

// makeSelfSignedCAPEM mints a throwaway CA so the verify branch of the
// credential builder can be exercised without shipping a fixture.
func makeSelfSignedCAPEM(t *testing.T) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate CA key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "unit-test-api-ca"},
		NotBefore:   time.Now().Add(-time.Hour),
		NotAfter:    time.Now().Add(time.Hour),
		IsCA:        true,
		KeyUsage:    x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create CA cert: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

// TestApiTlsCredentials locks the security decision of the remote write path:
// a mode must resolve to the right transport, and the dangerous combinations
// (unknown mode, verify without a CA, an unparseable CA) must fail at config
// time, not surface as a failed RPC on the first hot operation.
func TestApiTlsCredentials(t *testing.T) {
	for _, m := range []string{ApiTlsInsecure, ""} {
		c, err := apiTlsCredentials(m, nil)
		if err != nil {
			t.Fatalf("mode %q: %v", m, err)
		}
		if !c.insecure || c.tls != nil {
			t.Fatalf("mode %q: want plain-text, got %+v", m, c)
		}
	}

	c, err := apiTlsCredentials(ApiTlsSkip, nil)
	if err != nil {
		t.Fatalf("skip: %v", err)
	}
	if c.insecure || c.tls == nil || !c.tls.InsecureSkipVerify {
		t.Fatalf("skip: want InsecureSkipVerify TLS, got %+v", c)
	}

	if _, err := apiTlsCredentials("bogus", nil); err == nil {
		t.Fatal("unknown mode: want error")
	}
	if _, err := apiTlsCredentials(ApiTlsVerify, nil); err == nil {
		t.Fatal("verify with no CA: want error")
	}
	if _, err := apiTlsCredentials(ApiTlsVerify, []byte("not a certificate")); err == nil {
		t.Fatal("verify with junk CA: want error")
	}

	ca := makeSelfSignedCAPEM(t)
	c, err = apiTlsCredentials(ApiTlsVerify, ca)
	if err != nil {
		t.Fatalf("verify with CA: %v", err)
	}
	if c.insecure || c.tls == nil || c.tls.RootCAs == nil {
		t.Fatalf("verify: want pinned-CA TLS, got %+v", c)
	}
	if c.tls.MinVersion < tls.VersionTLS12 {
		t.Fatalf("verify: want MinVersion TLS1.2, got %v", c.tls.MinVersion)
	}
}

// TestRemoteAddr locks the gRPC target formatting the write path dials:
// host:port with localhost fallback and the empty-target case a read-only
// node must produce.
func TestRemoteAddr(t *testing.T) {
	if got := remoteAddr("", 0); got != "" {
		t.Fatalf("no host no port: want empty, got %q", got)
	}
	if got := remoteAddr("", 10085); got != "127.0.0.1:10085" {
		t.Fatalf("localhost fallback: got %q", got)
	}
	if got := remoteAddr("node2.example.com", 25500); got != "node2.example.com:25500" {
		t.Fatalf("explicit host: got %q", got)
	}
}
