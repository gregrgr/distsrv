package server

import (
	"crypto/x509"
	"io/fs"
	"log"
	"strings"
)

// appleChainStore is built once at process start: it maps an
// intermediate certificate's Subject DN (in RFC 2253 form) to the cert
// itself. When we PKCS7-sign a mobileconfig with a leaf cert issued by
// Apple WWDR, we look up the matching G3/G4/G5/G6 intermediate by the
// leaf's Issuer DN and embed it in the SignedData chain — without that,
// iOS can't walk back to Apple Root CA and shows "尚未验证".
var appleChainStore = mustLoadAppleIntermediates()

func mustLoadAppleIntermediates() map[string]*x509.Certificate {
	out := map[string]*x509.Certificate{}
	entries, err := fs.ReadDir(appleIntermediates, "certs/apple")
	if err != nil {
		log.Printf("apple intermediates dir missing: %v", err)
		return out
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".cer") {
			continue
		}
		data, err := fs.ReadFile(appleIntermediates, "certs/apple/"+e.Name())
		if err != nil {
			log.Printf("read %s: %v", e.Name(), err)
			continue
		}
		cert, err := x509.ParseCertificate(data)
		if err != nil {
			log.Printf("parse %s: %v", e.Name(), err)
			continue
		}
		out[cert.Subject.String()] = cert
	}
	return out
}

// findAppleIntermediate returns the WWDR intermediate that issued `leaf`,
// or nil if the leaf isn't an Apple-issued cert (or its issuer isn't in
// the embedded set — log a warning so we know to add a newer generation).
func findAppleIntermediate(leaf *x509.Certificate) *x509.Certificate {
	if leaf == nil {
		return nil
	}
	want := leaf.Issuer.String()
	// Only intervene for Apple-rooted leaves; otherwise let the user's
	// PKCS12-supplied chain do its thing.
	if !strings.Contains(want, "O=Apple Inc.") {
		return nil
	}
	if ic, ok := appleChainStore[want]; ok {
		return ic
	}
	log.Printf("apple WWDR intermediate not bundled for issuer %q — chain may be incomplete; add the matching .cer to internal/server/certs/apple/", want)
	return nil
}
