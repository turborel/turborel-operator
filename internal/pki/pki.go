// Package pki issues the private CA and TLS identities of a Turborel cluster.
package pki

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"time"
)

// Authority creates a ten-year CA. CA replacement is an explicit administrative operation.
func Authority(name string, now time.Time) (map[string][]byte, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, err
	}
	cert := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: name},
		NotBefore: now.Add(-time.Hour), NotAfter: now.AddDate(10, 0, 0), IsCA: true,
		BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	rawKey, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, err
	}
	return map[string][]byte{"ca.crt": pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		"ca.key": pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: rawKey})}, nil
}

// Issue creates a ninety-day identity usable for server TLS, replica mTLS and operator health checks.
func Issue(ca map[string][]byte, names []string, now time.Time) (map[string][]byte, error) {
	pair, err := tls.X509KeyPair(ca["ca.crt"], ca["ca.key"])
	if err != nil {
		return nil, fmt.Errorf("read CA: %w", err)
	}
	parent, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return nil, err
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, err
	}
	cert := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: names[0]}, DNSNames: names,
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(90 * 24 * time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}}
	if cert.NotAfter.After(parent.NotAfter) {
		return nil, fmt.Errorf("CA expires before the next leaf certificate")
	}
	der, err := x509.CreateCertificate(rand.Reader, cert, parent, &key.PublicKey, pair.PrivateKey)
	if err != nil {
		return nil, err
	}
	rawKey, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, err
	}
	return map[string][]byte{"ca.crt": ca["ca.crt"], "tls.crt": pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		"tls.key": pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: rawKey})}, nil
}

func NeedsRenewal(data map[string][]byte, now time.Time) bool {
	block, _ := pem.Decode(data["tls.crt"])
	if block == nil {
		return true
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	return err != nil || now.Add(30*24*time.Hour).After(cert.NotAfter)
}

// TLSConfig verifies both the CA and server name. It never falls back to insecure TLS.
func TLSConfig(data map[string][]byte, serverName string) (*tls.Config, error) {
	pair, err := tls.X509KeyPair(data["tls.crt"], data["tls.key"])
	if err != nil {
		return nil, err
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(data["ca.crt"]) {
		return nil, fmt.Errorf("invalid CA certificate")
	}
	return &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: roots, ClientCAs: roots,
		Certificates: []tls.Certificate{pair}, ServerName: serverName}, nil
}
