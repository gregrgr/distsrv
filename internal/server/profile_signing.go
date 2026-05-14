package server

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"

	"golang.org/x/crypto/pkcs12"

	"distsrv/internal/config"
)

// loadProfileSigningCert reads an Apple code-signing cert + private key
// from disk so handleMobileconfig can PKCS7-sign the UDID-collection
// .mobileconfig with a certificate iOS will actually trust for
// profile signing.
//
// Returns (nil, nil) when nothing is configured — caller falls back to
// the LE TLS cert path.
func loadProfileSigningCert(cfg config.ProfileSigningConfig) (*tls.Certificate, error) {
	// PKCS12 (.p12 / .pfx) path — Apple Developer Portal's default
	// export format.
	if cfg.PKCS12File != "" {
		data, err := os.ReadFile(cfg.PKCS12File)
		if err != nil {
			return nil, fmt.Errorf("read pkcs12 %s: %w", cfg.PKCS12File, err)
		}
		password := cfg.PKCS12Password
		if env := os.Getenv("DISTSRV_P12_PASSWORD"); env != "" {
			password = env
		}
		// x/crypto/pkcs12.ToPEM expands the .p12 to PEM blocks (private
		// key + 1..n CERTIFICATE blocks).
		blocks, err := pkcs12.ToPEM(data, password)
		if err != nil {
			return nil, fmt.Errorf("decode pkcs12 %s: %w (wrong password?)", cfg.PKCS12File, err)
		}
		var certPEM bytes.Buffer
		var keyPEM bytes.Buffer
		for _, b := range blocks {
			pem.Encode(pickBuf(b.Type, &certPEM, &keyPEM), b)
		}
		if certPEM.Len() == 0 {
			return nil, fmt.Errorf("pkcs12 %s contains no CERTIFICATE blocks", cfg.PKCS12File)
		}
		if keyPEM.Len() == 0 {
			return nil, fmt.Errorf("pkcs12 %s contains no PRIVATE KEY block", cfg.PKCS12File)
		}
		tlsCert, err := tls.X509KeyPair(certPEM.Bytes(), keyPEM.Bytes())
		if err != nil {
			return nil, fmt.Errorf("reassemble pkcs12 cert/key: %w", err)
		}
		if leaf, err := x509.ParseCertificate(tlsCert.Certificate[0]); err == nil {
			tlsCert.Leaf = leaf
		}
		return &tlsCert, nil
	}

	// PEM path — cert_file may include the leaf + any intermediates
	// concatenated; key_file holds the matching private key.
	if cfg.CertFile != "" && cfg.KeyFile != "" {
		certPEM, err := os.ReadFile(cfg.CertFile)
		if err != nil {
			return nil, fmt.Errorf("read cert %s: %w", cfg.CertFile, err)
		}
		keyPEM, err := os.ReadFile(cfg.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("read key %s: %w", cfg.KeyFile, err)
		}
		// tls.X509KeyPair handles concatenated cert chains in certPEM.
		tlsCert, err := tls.X509KeyPair(certPEM, keyPEM)
		if err != nil {
			return nil, fmt.Errorf("parse cert/key: %w", err)
		}
		// Populate Leaf for code that wants to inspect the cert.
		if len(tlsCert.Certificate) > 0 {
			if leaf, err := x509.ParseCertificate(tlsCert.Certificate[0]); err == nil {
				tlsCert.Leaf = leaf
			}
		}
		// Sanity-check: at least one PEM CERTIFICATE block.
		if !pemHasBlock(certPEM, "CERTIFICATE") {
			return nil, errors.New("cert_file has no CERTIFICATE PEM block")
		}
		return &tlsCert, nil
	}

	return nil, nil
}

func pemHasBlock(data []byte, want string) bool {
	for {
		block, rest := pem.Decode(data)
		if block == nil {
			return false
		}
		if block.Type == want {
			return true
		}
		data = rest
	}
}

// pickBuf routes a PEM block to the cert buffer or the key buffer based
// on its Type. PKCS12 -> PEM emits "CERTIFICATE" + one of the various
// key types ("PRIVATE KEY", "RSA PRIVATE KEY", "EC PRIVATE KEY").
func pickBuf(typ string, certBuf, keyBuf *bytes.Buffer) *bytes.Buffer {
	if typ == "CERTIFICATE" {
		return certBuf
	}
	return keyBuf
}
