// Package tlsutil creates a local certificate authority and a server certificate signed by it,
// so a school can serve DoExam over HTTPS on its LAN without a public domain
// (DOCS/PLAN/19-LIHAT-LAYAR-KAMERA.md §7). Browsers allow the camera, screen sharing and the
// Keyboard Lock API only on secure origins, and an IP address like https://192.168.1.10 can
// only be trusted through a CA installed on the lab PCs.
package tlsutil

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// File names inside the TLS directory.
const (
	CAFile         = "ca.crt"
	caKeyFile      = "ca.key"
	CertFile       = "server.crt"
	KeyFile        = "server.key"
	caValidity     = 10 * 365 * 24 * time.Hour
	serverValidity = 397 * 24 * time.Hour // the longest validity browsers accept for a leaf
)

// Result describes what Init wrote.
type Result struct {
	Dir       string
	CAPath    string
	CertPath  string
	KeyPath   string
	CACreated bool
	Hosts     []string
	NotAfter  time.Time
	// CAFingerprint is the SHA-256 of the CA certificate, to compare on the lab PCs.
	CAFingerprint string
}

// Init makes sure dir holds a CA (kept when it exists, so installed PCs keep trusting the
// server) and writes a fresh server certificate for hosts (DNS names or IP addresses).
func Init(dir string, hosts []string, now time.Time) (Result, error) {
	hosts = normalizeHosts(hosts)
	if len(hosts) == 0 {
		return Result{}, errors.New("at least one host name or IP address is needed")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return Result{}, fmt.Errorf("create %s: %w", dir, err)
	}
	res := Result{
		Dir: dir, CAPath: filepath.Join(dir, CAFile), CertPath: filepath.Join(dir, CertFile),
		KeyPath: filepath.Join(dir, KeyFile), Hosts: hosts,
	}

	caCert, caKey, err := loadCA(dir)
	if errors.Is(err, os.ErrNotExist) {
		caCert, caKey, err = createCA(dir, now)
		res.CACreated = true
	}
	if err != nil {
		return Result{}, err
	}
	sum := sha256.Sum256(caCert.Raw)
	res.CAFingerprint = fingerprint(sum[:])

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return Result{}, fmt.Errorf("generate server key: %w", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial(),
		Subject:      pkix.Name{CommonName: hosts[0], Organization: []string{"DoExam"}},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(serverValidity),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	for _, h := range hosts {
		if ip := net.ParseIP(h); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		} else {
			tmpl.DNSNames = append(tmpl.DNSNames, h)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, caCert, &key.PublicKey, caKey)
	if err != nil {
		return Result{}, fmt.Errorf("sign server certificate: %w", err)
	}
	if err := writeKey(res.KeyPath, key); err != nil {
		return Result{}, err
	}
	// the chain (server + CA) lets clients that only trust the CA verify the server
	chain := append(pemBlock("CERTIFICATE", der), pemBlock("CERTIFICATE", caCert.Raw)...)
	if err := os.WriteFile(res.CertPath, chain, 0o644); err != nil {
		return Result{}, fmt.Errorf("write %s: %w", res.CertPath, err)
	}
	res.NotAfter = tmpl.NotAfter
	return res, nil
}

// DefaultHosts lists the names the server is likely reached by: localhost, the machine's
// host name, and every non-loopback IPv4/IPv6 address of its network interfaces.
func DefaultHosts() []string {
	hosts := []string{"localhost", "127.0.0.1", "::1"}
	if h, err := os.Hostname(); err == nil && h != "" {
		hosts = append(hosts, h)
	}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return hosts
	}
	for _, a := range addrs {
		ipn, ok := a.(*net.IPNet)
		if !ok || ipn.IP.IsLoopback() || ipn.IP.IsLinkLocalUnicast() {
			continue
		}
		hosts = append(hosts, ipn.IP.String())
	}
	return hosts
}

func createCA(dir string, now time.Time) (*x509.Certificate, *ecdsa.PrivateKey, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("generate CA key: %w", err)
	}
	name, _ := os.Hostname()
	tmpl := &x509.Certificate{
		SerialNumber:          serial(),
		Subject:               pkix.Name{CommonName: strings.TrimSpace("DoExam local CA " + name), Organization: []string{"DoExam"}},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(caValidity),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLenZero:        true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, fmt.Errorf("create CA: %w", err)
	}
	if err := writeKey(filepath.Join(dir, caKeyFile), key); err != nil {
		return nil, nil, err
	}
	if err := os.WriteFile(filepath.Join(dir, CAFile), pemBlock("CERTIFICATE", der), 0o644); err != nil {
		return nil, nil, fmt.Errorf("write CA: %w", err)
	}
	cert, err := x509.ParseCertificate(der)
	return cert, key, err
}

func loadCA(dir string) (*x509.Certificate, *ecdsa.PrivateKey, error) {
	certPEM, err := os.ReadFile(filepath.Join(dir, CAFile))
	if err != nil {
		return nil, nil, err
	}
	keyPEM, err := os.ReadFile(filepath.Join(dir, caKeyFile))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil, fmt.Errorf("%s exists but %s is missing: restore it, or move %s away to create a new CA", CAFile, caKeyFile, CAFile)
		}
		return nil, nil, err
	}
	cb, _ := pem.Decode(certPEM)
	kb, _ := pem.Decode(keyPEM)
	if cb == nil || kb == nil {
		return nil, nil, fmt.Errorf("unreadable CA files in %s", dir)
	}
	cert, err := x509.ParseCertificate(cb.Bytes)
	if err != nil {
		return nil, nil, fmt.Errorf("parse CA: %w", err)
	}
	key, err := x509.ParseECPrivateKey(kb.Bytes)
	if err != nil {
		return nil, nil, fmt.Errorf("parse CA key: %w", err)
	}
	return cert, key, nil
}

func writeKey(path string, key *ecdsa.PrivateKey) error {
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return fmt.Errorf("encode key: %w", err)
	}
	if err := os.WriteFile(path, pemBlock("EC PRIVATE KEY", der), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

func pemBlock(typ string, der []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: der})
}

func serial() *big.Int {
	n, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return big.NewInt(time.Now().UnixNano())
	}
	return n
}

func normalizeHosts(in []string) []string {
	out := make([]string, 0, len(in))
	for _, h := range in {
		h = strings.ToLower(strings.TrimSpace(h))
		if ip := net.ParseIP(h); ip != nil {
			h = ip.String()
		}
		if h != "" && !slices.Contains(out, h) {
			out = append(out, h)
		}
	}
	return out
}

func fingerprint(sum []byte) string {
	s := strings.ToUpper(hex.EncodeToString(sum))
	parts := make([]string, 0, len(s)/2)
	for i := 0; i < len(s); i += 2 {
		parts = append(parts, s[i:i+2])
	}
	return strings.Join(parts, ":")
}
