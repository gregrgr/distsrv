package server

import (
	"bytes"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/crypto/pkcs12"

	"distsrv/internal/config"
)

var _ = x509.ParseCertificate // keep crypto/x509 used by callers

// profileSigningCertPaths returns the on-disk locations the admin web
// UI writes to / reads from. PEM format (cert may carry the chain).
func profileSigningCertPaths(dataDir string) (certPath, keyPath string) {
	return filepath.Join(dataDir, "profile-signing.crt"),
		filepath.Join(dataDir, "profile-signing.key")
}

// loadProfileSigningCertFromDataDir is the runtime/web-uploaded fallback
// of loadProfileSigningCert: if no profile_signing block is in
// config.toml, look for PEM files written by the admin UI.
func loadProfileSigningCertFromDataDir(dataDir string) (*tls.Certificate, error) {
	if dataDir == "" {
		return nil, nil
	}
	certPath, keyPath := profileSigningCertPaths(dataDir)
	if _, err := os.Stat(certPath); os.IsNotExist(err) {
		return nil, nil
	}
	if _, err := os.Stat(keyPath); os.IsNotExist(err) {
		return nil, nil
	}
	return loadProfileSigningCert(config.ProfileSigningConfig{
		CertFile: certPath,
		KeyFile:  keyPath,
	})
}

// decodePKCS12ToPEM unpacks an Apple Developer .p12 export into a
// CERTIFICATE chain PEM and a single PRIVATE KEY PEM, ready for
// tls.X509KeyPair or for writing to disk.
func decodePKCS12ToPEM(p12Data []byte, password string) (certPEM, keyPEM []byte, err error) {
	blocks, err := pkcs12.ToPEM(p12Data, password)
	if err != nil {
		return nil, nil, fmt.Errorf("decode pkcs12 (wrong password or corrupt file?): %w", err)
	}
	var certBuf, keyBuf bytes.Buffer
	for _, b := range blocks {
		pem.Encode(pickBuf(b.Type, &certBuf, &keyBuf), b)
	}
	if certBuf.Len() == 0 {
		return nil, nil, errors.New("pkcs12 contains no CERTIFICATE blocks")
	}
	if keyBuf.Len() == 0 {
		return nil, nil, errors.New("pkcs12 contains no PRIVATE KEY block")
	}
	// Sanity check: the result must form a valid keypair.
	if _, err := tls.X509KeyPair(certBuf.Bytes(), keyBuf.Bytes()); err != nil {
		return nil, nil, fmt.Errorf("decoded pkcs12 cert/key don't form a keypair: %w", err)
	}
	return certBuf.Bytes(), keyBuf.Bytes(), nil
}

// saveProfileSigningPEM writes the cert chain + key PEMs to data_dir
// at the canonical paths, with the key locked down to mode 0600.
func saveProfileSigningPEM(dataDir string, certPEM, keyPEM []byte) error {
	certPath, keyPath := profileSigningCertPaths(dataDir)
	tmpCert := certPath + ".tmp"
	tmpKey := keyPath + ".tmp"
	if err := os.WriteFile(tmpCert, certPEM, 0o600); err != nil {
		return err
	}
	if err := os.WriteFile(tmpKey, keyPEM, 0o600); err != nil {
		_ = os.Remove(tmpCert)
		return err
	}
	if err := os.Rename(tmpCert, certPath); err != nil {
		_ = os.Remove(tmpCert)
		_ = os.Remove(tmpKey)
		return err
	}
	if err := os.Rename(tmpKey, keyPath); err != nil {
		_ = os.Remove(tmpKey)
		return err
	}
	return nil
}

// removeProfileSigningCert removes the on-disk cert + key (admin clicked
// "delete"). Idempotent: not-found is not an error.
func removeProfileSigningCert(dataDir string) error {
	certPath, keyPath := profileSigningCertPaths(dataDir)
	for _, p := range []string{certPath, keyPath} {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

// describeCert pulls the human-readable bits the admin UI shows.
type ProfileSigningCertInfo struct {
	SubjectCN string
	IssuerCN  string
	SubjectO  string
	IssuerO   string
	NotBefore string
	NotAfter  string
	Serial    string
	SHA256Hex string // colon-separated thumbprint
	ChainLen  int
	// EKU shows the human-readable list of Extended Key Usages
	// (e.g. ["Code Signing"], ["Email Protection"], or both).
	EKU []string
	// Suitable is true if this cert is plausibly usable for signing
	// mobileconfig profiles (has Email Protection EKU or no EKU at all).
	// Apple Distribution / Apple Development certs have
	// EKU=criticalCodeSigning which iOS 16+ rejects for profile signing.
	Suitable bool
	// UnsuitableReason carries the explanation when Suitable is false.
	UnsuitableReason string
}

func describeCert(cert *tls.Certificate) *ProfileSigningCertInfo {
	if cert == nil || cert.Leaf == nil {
		return nil
	}
	leaf := cert.Leaf
	sum := sha256Sum(leaf.Raw)
	info := &ProfileSigningCertInfo{
		SubjectCN: leaf.Subject.CommonName,
		IssuerCN:  leaf.Issuer.CommonName,
		NotBefore: leaf.NotBefore.Format("2006-01-02"),
		NotAfter:  leaf.NotAfter.Format("2006-01-02"),
		Serial:    leaf.SerialNumber.String(),
		SHA256Hex: hexColons(sum),
		ChainLen:  len(cert.Certificate),
		EKU:       ekuNames(leaf),
	}
	if len(leaf.Subject.Organization) > 0 {
		info.SubjectO = leaf.Subject.Organization[0]
	}
	if len(leaf.Issuer.Organization) > 0 {
		info.IssuerO = leaf.Issuer.Organization[0]
	}
	info.Suitable, info.UnsuitableReason = checkMobileconfigSuitable(leaf)
	return info
}

// ekuNames renders the cert's ExtKeyUsage list as user-friendly names.
func ekuNames(c *x509.Certificate) []string {
	out := make([]string, 0, len(c.ExtKeyUsage))
	for _, eku := range c.ExtKeyUsage {
		switch eku {
		case x509.ExtKeyUsageAny:
			out = append(out, "Any")
		case x509.ExtKeyUsageServerAuth:
			out = append(out, "TLS Server Authentication")
		case x509.ExtKeyUsageClientAuth:
			out = append(out, "TLS Client Authentication")
		case x509.ExtKeyUsageCodeSigning:
			out = append(out, "Code Signing")
		case x509.ExtKeyUsageEmailProtection:
			out = append(out, "Email Protection (S/MIME)")
		case x509.ExtKeyUsageTimeStamping:
			out = append(out, "Time Stamping")
		case x509.ExtKeyUsageOCSPSigning:
			out = append(out, "OCSP Signing")
		default:
			out = append(out, fmt.Sprintf("ExtKeyUsage(%d)", eku))
		}
	}
	return out
}

// checkMobileconfigSuitable inspects the leaf cert's EKU and returns
// whether iOS is likely to accept it as a mobileconfig signer.
//
// The blocker we keep hitting in the wild: Apple Distribution certs
// have EKU=Code Signing marked critical, and iOS 16+ refuses to use
// such a cert for Profile-type CMS signatures — the install sheet
// shows '无效的描述文件' / 'Invalid Profile' even after the chain
// validates and the signature verifies in openssl.
//
// What works: a cert that has Email Protection in its EKU set (any
// S/MIME signing cert — e.g. Actalis's free 1-year S/MIME, DigiCert,
// Sectigo), or a cert with no EKU restrictions at all.
func checkMobileconfigSuitable(leaf *x509.Certificate) (bool, string) {
	hasCodeSigning := false
	hasEmailProtection := false
	for _, eku := range leaf.ExtKeyUsage {
		switch eku {
		case x509.ExtKeyUsageEmailProtection:
			hasEmailProtection = true
		case x509.ExtKeyUsageCodeSigning:
			hasCodeSigning = true
		case x509.ExtKeyUsageAny:
			return true, ""
		}
	}
	if hasEmailProtection {
		return true, ""
	}
	// No EKU at all (some CAs leave it off) — usually fine.
	if len(leaf.ExtKeyUsage) == 0 && len(leaf.UnknownExtKeyUsage) == 0 {
		return true, ""
	}
	// Apple Distribution / Apple Development / generic code-signing
	// certs land here.
	if hasCodeSigning {
		return false, "证书的 Extended Key Usage 限定为 Code Signing（典型的 Apple Distribution / Apple Development 证书）。iOS 16 及更新版本不接受此类证书签 mobileconfig — 装到 iPhone 上会显示\"无效的描述文件\"。建议改用 S/MIME (Email Protection) 证书。"
	}
	return false, fmt.Sprintf("证书的 Extended Key Usage 不包含 Email Protection（当前包含：%v）。iOS 可能不接受此证书签 mobileconfig。", ekuNames(leaf))
}

func sha256Sum(b []byte) []byte {
	h := sha256.New()
	h.Write(b)
	return h.Sum(nil)
}

func hexColons(b []byte) string {
	const hex = "0123456789abcdef"
	out := make([]byte, 0, len(b)*3)
	for i, v := range b {
		if i > 0 {
			out = append(out, ':')
		}
		out = append(out, hex[v>>4], hex[v&0x0f])
	}
	return string(out)
}


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
