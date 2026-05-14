package server

import (
	"crypto"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"

	"go.mozilla.org/pkcs7"
)

// signMobileconfig wraps the raw unsigned XML in a CMS (PKCS#7) signed
// envelope using the live TLS certificate fetched via the autocert manager.
//
// iOS 16+ rejects unsigned PayloadType=Profile Service profiles as
// "无效的描述文件" (invalid profile), so this step is mandatory in
// production. In dev mode (no autocert) the caller should fall back
// to unsigned XML and accept that the profile won't install on iOS.
func (s *Server) signMobileconfig(unsigned []byte) ([]byte, error) {
	if s.autocert == nil {
		return nil, errors.New("autocert not initialized (dev mode?)")
	}

	chi := &tls.ClientHelloInfo{ServerName: s.cfg.Server.Domain}
	tlsCert, err := s.autocert.GetCertificate(chi)
	if err != nil {
		return nil, fmt.Errorf("get tls cert: %w", err)
	}
	if len(tlsCert.Certificate) == 0 {
		return nil, errors.New("empty cert chain from autocert")
	}

	// First entry is the leaf; subsequent are intermediates.
	leaf, err := x509.ParseCertificate(tlsCert.Certificate[0])
	if err != nil {
		return nil, fmt.Errorf("parse leaf cert: %w", err)
	}
	var intermediates []*x509.Certificate
	for _, der := range tlsCert.Certificate[1:] {
		c, err := x509.ParseCertificate(der)
		if err != nil {
			return nil, fmt.Errorf("parse intermediate: %w", err)
		}
		intermediates = append(intermediates, c)
	}

	key, ok := tlsCert.PrivateKey.(crypto.Signer)
	if !ok {
		return nil, errors.New("tls private key does not implement crypto.Signer")
	}

	sd, err := pkcs7.NewSignedData(unsigned)
	if err != nil {
		return nil, fmt.Errorf("pkcs7 new signed data: %w", err)
	}
	// SHA-256 is universally supported and what iOS expects today.
	sd.SetDigestAlgorithm(pkcs7.OIDDigestAlgorithmSHA256)

	if err := sd.AddSigner(leaf, key, pkcs7.SignerInfoConfig{}); err != nil {
		return nil, fmt.Errorf("add signer: %w", err)
	}
	for _, ic := range intermediates {
		sd.AddCertificate(ic)
	}

	out, err := sd.Finish()
	if err != nil {
		return nil, fmt.Errorf("pkcs7 finish: %w", err)
	}
	return out, nil
}
