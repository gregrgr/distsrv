package server

import (
	"crypto"
	"crypto/x509"
	"errors"
	"fmt"

	"go.mozilla.org/pkcs7"
)

// signMobileconfig wraps the raw unsigned XML in a CMS (PKCS#7) signed
// envelope.
//
// Only signs if the admin uploaded a dedicated profile-signing cert via
// /admin/signing-cert. We deliberately do NOT fall back to the LE TLS
// cert — iOS 16+/26 rejects mobileconfigs signed with TLS server certs
// ("Code Signing"-EKU and friends), so signing with the wrong cert is
// strictly worse than not signing at all (unsigned profiles install
// with a 'Not Verified' warning; mis-signed profiles get rejected
// outright as 'Invalid Profile').
func (s *Server) signMobileconfig(unsigned []byte) ([]byte, error) {
	tlsCert := s.getProfileSigningCert()
	if tlsCert == nil {
		return nil, errors.New("no profile-signing cert configured")
	}
	if len(tlsCert.Certificate) == 0 {
		return nil, errors.New("empty cert chain")
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
	// Most .p12 exports from Apple Developer Portal / Keychain Access
	// only contain the leaf + private key — the WWDR intermediate that
	// actually issued the leaf is left to be picked up from the system
	// trust store. iOS doesn't do that for mobileconfig validation, so
	// auto-attach the right Apple WWDR intermediate when missing.
	if ic := findAppleIntermediate(leaf); ic != nil {
		already := false
		for _, existing := range intermediates {
			if existing.Equal(ic) {
				already = true
				break
			}
		}
		if !already {
			intermediates = append(intermediates, ic)
		}
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
