package tlsutil

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestInit(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	res, err := Init(dir, []string{"192.168.1.10", "Exam.Lab", "exam.lab", " "}, now)
	if err != nil {
		t.Fatal(err)
	}
	if !res.CACreated || len(res.Hosts) != 2 {
		t.Fatalf("result %+v", res)
	}

	pair, err := tls.LoadX509KeyPair(res.CertPath, res.KeyPath)
	if err != nil {
		t.Fatalf("load pair: %v", err)
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	caPEM, _ := os.ReadFile(res.CAPath)
	pool.AppendCertsFromPEM(caPEM)
	for _, host := range []string{"192.168.1.10", "exam.lab"} {
		if _, err := leaf.Verify(x509.VerifyOptions{Roots: pool, DNSName: host, CurrentTime: now}); err != nil {
			t.Errorf("verify %s: %v", host, err)
		}
	}
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: pool, DNSName: "other.lab", CurrentTime: now}); err == nil {
		t.Error("certificate must not be valid for other hosts")
	}
	if leaf.NotAfter.Sub(now) > 398*24*time.Hour {
		t.Errorf("leaf valid until %v: browsers reject more than 398 days", leaf.NotAfter)
	}
	if st, _ := os.Stat(filepath.Join(dir, caKeyFile)); st.Mode().Perm() != 0o600 {
		t.Errorf("CA key mode %v", st.Mode().Perm())
	}

	// a second run keeps the CA (lab PCs already trust it) and issues a new leaf
	again, err := Init(dir, []string{"10.0.0.5"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if again.CACreated || again.CAFingerprint != res.CAFingerprint {
		t.Fatalf("CA was replaced: %+v", again)
	}
	pair2, _ := tls.LoadX509KeyPair(again.CertPath, again.KeyPath)
	leaf2, _ := x509.ParseCertificate(pair2.Certificate[0])
	if _, err := leaf2.Verify(x509.VerifyOptions{Roots: pool, DNSName: "10.0.0.5", CurrentTime: now}); err != nil {
		t.Errorf("new leaf with old CA: %v", err)
	}
	// the chain carries the CA
	chain, _ := os.ReadFile(again.CertPath)
	n := 0
	for b, rest := pem.Decode(chain); b != nil; b, rest = pem.Decode(rest) {
		n++
	}
	if n != 2 {
		t.Errorf("chain has %d certificates, want 2", n)
	}
}

func TestInitErrors(t *testing.T) {
	if _, err := Init(t.TempDir(), nil, time.Now()); err == nil {
		t.Error("no hosts must fail")
	}
	dir := t.TempDir()
	if _, err := Init(dir, []string{"localhost"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, caKeyFile)); err != nil {
		t.Fatal(err)
	}
	if _, err := Init(dir, []string{"localhost"}, time.Now()); err == nil {
		t.Error("a CA without its key must not be silently replaced")
	}
}

func TestDefaultHosts(t *testing.T) {
	hosts := DefaultHosts()
	if len(hosts) < 3 || hosts[0] != "localhost" {
		t.Fatalf("hosts %v", hosts)
	}
}
