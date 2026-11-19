package pki

import (
	"crypto/x509"
	"encoding/pem"
	"testing"
	"time"
)

func TestIdentityAndRenewal(t *testing.T) {
	t.Parallel()
	now := time.Now()
	ca, err := Authority("test", now)
	if err != nil {
		t.Fatal(err)
	}
	data, err := Issue(ca, []string{"db-1.db-instances.default.svc", "db-rw.default.svc"}, now)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := TLSConfig(data, "db-rw.default.svc")
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(data["tls.crt"])
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cert.Verify(x509.VerifyOptions{Roots: cfg.RootCAs, DNSName: cfg.ServerName, CurrentTime: now}); err != nil {
		t.Fatal(err)
	}
	if err := cert.VerifyHostname("unrelated.default.svc"); err == nil {
		t.Fatal("certificate accepted wrong server")
	}
	if NeedsRenewal(data, now) {
		t.Fatal("fresh certificate needs renewal")
	}
	if !NeedsRenewal(data, now.Add(61*24*time.Hour)) {
		t.Fatal("expiring certificate was not renewed")
	}
	if _, ok := data["ca.key"]; ok {
		t.Fatal("leaf secret exposes signing key")
	}
}
