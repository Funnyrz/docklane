package security

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVaultAuthenticatedEncryptionAndRestart(t *testing.T) {
	dir := t.TempDir()
	v, e := NewVault(dir)
	if e != nil {
		t.Fatal(e)
	}
	a, e := v.Seal("registry-password")
	if e != nil {
		t.Fatal(e)
	}
	b, _ := v.Seal("registry-password")
	if a == b {
		t.Fatal("nonces reused")
	}
	v2, e := NewVault(dir)
	if e != nil {
		t.Fatal(e)
	}
	plain, e := v2.Open(a)
	if e != nil || plain != "registry-password" {
		t.Fatal("restart lost key", e)
	}
	bytes := []byte(a)
	bytes[len(bytes)-3] ^= 1
	if _, e = v.Open(string(bytes)); e == nil {
		t.Fatal("accepted tampered ciphertext")
	}
	info, _ := os.Stat(filepath.Join(dir, "master.key"))
	if info.Mode().Perm() != 0600 {
		t.Fatal("master key is not private")
	}
}
func TestPasswordAndRedaction(t *testing.T) {
	h, e := HashPassword("a-strong-password-123")
	if e != nil {
		t.Fatal(e)
	}
	if !CheckPassword(h, "a-strong-password-123") || CheckPassword(h, "wrong") {
		t.Fatal("password verification")
	}
	if _, e = HashPassword("short"); e == nil {
		t.Fatal("weak password allowed")
	}
	s := Redact(`password=hunter2 authorization:bearer123 https://admin:pass@example.org token="foo"`)
	for _, secret := range []string{"hunter2", "bearer123", "admin:pass", "foo"} {
		if strings.Contains(s, secret) {
			t.Fatalf("secret not removed: %s", s)
		}
	}
}
