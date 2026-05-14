package server

import (
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"testing"
	"time"
)

// TestAppleChainStoreLoaded asserts the embed.FS shipped the WWDR
// intermediates and they're indexed by Subject DN.
func TestAppleChainStoreLoaded(t *testing.T) {
	if len(appleChainStore) < 4 {
		t.Fatalf("expected at least 4 Apple WWDR intermediates (G3..G6), got %d", len(appleChainStore))
	}
	// All entries are issued by Apple Root CA (any of the variants).
	for dn, cert := range appleChainStore {
		if cert.Subject.String() != dn {
			t.Errorf("indexed cert subject %q doesn't match key %q", cert.Subject.String(), dn)
		}
		if !containsAppleRoot(cert.Issuer) {
			t.Errorf("intermediate %q not issued by Apple Root CA (issuer=%q)", cert.Subject, cert.Issuer)
		}
	}
}

func containsAppleRoot(name pkix.Name) bool {
	for _, o := range name.Organization {
		if o == "Apple Inc." {
			return true
		}
	}
	return false
}

// TestFindAppleIntermediate_MatchesByIssuerDN — synthesise a leaf
// whose Issuer DN matches the WWDR G3 intermediate and check the
// lookup returns it.
func TestFindAppleIntermediate_MatchesByIssuerDN(t *testing.T) {
	// Pick any one of the bundled intermediates as our target.
	var target *x509.Certificate
	for _, c := range appleChainStore {
		target = c
		break
	}
	if target == nil {
		t.Skip("no apple intermediates bundled")
	}

	leaf := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "Apple Distribution: Test", Organization: []string{"Apple Inc."}},
		Issuer:       target.Subject,
		NotBefore:    time.Now(),
		NotAfter:     time.Now().Add(time.Hour),
	}

	got := findAppleIntermediate(leaf)
	if got == nil {
		t.Fatalf("expected to find intermediate for issuer %q", target.Subject)
	}
	if !got.Equal(target) {
		t.Errorf("got wrong intermediate")
	}
}

// TestFindAppleIntermediate_SkipsNonAppleLeaves — leaves not rooted at
// Apple Inc. shouldn't be molested (e.g. our docker-test self-signed
// dummy cert).
func TestFindAppleIntermediate_SkipsNonAppleLeaves(t *testing.T) {
	leaf := &x509.Certificate{
		Subject: pkix.Name{CommonName: "distsrv test signer"},
		Issuer:  pkix.Name{CommonName: "distsrv test signer"},
	}
	if got := findAppleIntermediate(leaf); got != nil {
		t.Fatalf("non-Apple leaf should not match; got %q", got.Subject)
	}
}

// TestFindAppleIntermediate_NilLeaf
func TestFindAppleIntermediate_NilLeaf(t *testing.T) {
	if got := findAppleIntermediate(nil); got != nil {
		t.Fatal("nil leaf should return nil")
	}
}
