package auth

import (
	"regexp"
	"testing"
)

var uuidV4Re = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func TestRandomUUIDv4_Format(t *testing.T) {
	for i := 0; i < 50; i++ {
		u, err := RandomUUIDv4()
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if !uuidV4Re.MatchString(u) {
			t.Fatalf("not a valid RFC 4122 v4 UUID: %q", u)
		}
	}
}

func TestRandomUUIDv4_Unique(t *testing.T) {
	seen := make(map[string]struct{}, 1000)
	for i := 0; i < 1000; i++ {
		u, err := RandomUUIDv4()
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := seen[u]; ok {
			t.Fatalf("collision after %d iterations: %s", i, u)
		}
		seen[u] = struct{}{}
	}
}

func TestRandomToken_HexAndLength(t *testing.T) {
	tok, err := RandomToken(16)
	if err != nil {
		t.Fatal(err)
	}
	if len(tok) != 32 {
		t.Errorf("len(token) = %d, want 32", len(tok))
	}
	if matched, _ := regexp.MatchString(`^[0-9a-f]{32}$`, tok); !matched {
		t.Errorf("not lowercase hex: %q", tok)
	}
}
