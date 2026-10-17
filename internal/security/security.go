package security

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"golang.org/x/crypto/argon2"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

type Vault struct{ aead cipher.AEAD }

func NewVault(dir string) (*Vault, error) {
	p := filepath.Join(dir, "master.key")
	b, e := os.ReadFile(p)
	if errors.Is(e, os.ErrNotExist) {
		b = make([]byte, 32)
		if _, e = rand.Read(b); e != nil {
			return nil, e
		}
		f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return nil, err
		}
		_, e = f.Write(b)
		ce := f.Close()
		if e != nil {
			return nil, e
		}
		if ce != nil {
			return nil, ce
		}
	} else if e != nil {
		return nil, e
	}
	if len(b) != 32 {
		return nil, fmt.Errorf("invalid master.key: expected 32 bytes")
	}
	if e = os.Chmod(p, 0600); e != nil {
		return nil, e
	}
	block, e := aes.NewCipher(b)
	if e != nil {
		return nil, e
	}
	a, e := cipher.NewGCM(block)
	return &Vault{aead: a}, e
}
func (v *Vault) Seal(s string) (string, error) {
	n := make([]byte, v.aead.NonceSize())
	if _, e := rand.Read(n); e != nil {
		return "", e
	}
	return base64.RawStdEncoding.EncodeToString(v.aead.Seal(n, n, []byte(s), []byte("docker-manager:v1"))), nil
}
func (v *Vault) Open(s string) (string, error) {
	b, e := base64.RawStdEncoding.DecodeString(s)
	if e != nil {
		return "", e
	}
	n := v.aead.NonceSize()
	if len(b) < n {
		return "", errors.New("invalid encrypted value")
	}
	p, e := v.aead.Open(nil, b[:n], b[n:], []byte("docker-manager:v1"))
	return string(p), e
}

func Token() string {
	b := make([]byte, 32)
	if _, e := rand.Read(b); e != nil {
		panic(e)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
func TokenHash(s string) string { v := sha256.Sum256([]byte(s)); return hex.EncodeToString(v[:]) }
func HashPassword(password string) (string, error) {
	if len(password) < 12 || len(password) > 1024 {
		return "", errors.New("password must contain 12–1024 bytes")
	}
	salt := make([]byte, 16)
	if _, e := rand.Read(salt); e != nil {
		return "", e
	}
	h := argon2.IDKey([]byte(password), salt, 1, 64*1024, 4, 32)
	return "argon2id$" + base64.RawStdEncoding.EncodeToString(salt) + "$" + base64.RawStdEncoding.EncodeToString(h), nil
}
func CheckPassword(encoded, password string) bool {
	if len(password) > 1024 {
		return false
	}
	p := strings.Split(encoded, "$")
	if len(p) != 3 || p[0] != "argon2id" {
		return false
	}
	s, e := base64.RawStdEncoding.DecodeString(p[1])
	if e != nil || len(s) != 16 {
		return false
	}
	h, e := base64.RawStdEncoding.DecodeString(p[2])
	if e != nil || len(h) != 32 {
		return false
	}
	v := argon2.IDKey([]byte(password), s, 1, 64*1024, 4, 32)
	return subtle.ConstantTimeCompare(v, h) == 1
}

var quotedSensitive = regexp.MustCompile(`(?i)\b(password|passwd|token|secret|authorization|registryauth|api[_-]?key)(\s*[:=]\s*)("(?:\\.|[^"\\])*"|'[^']*')`)
var jsonSensitive = regexp.MustCompile(`(?i)("(?:password|passwd|token|secret|authorization|registryauth|api[_-]?key)"\s*:\s*)"(?:\\.|[^"\\])*"`)
var bearer = regexp.MustCompile(`(?i)(authorization\s*[:=]\s*)(?:Bearer|Basic)\s+[^\s,;]+`)
var sensitive = regexp.MustCompile(`(?i)(password|passwd|token|secret|authorization|registryauth|api[_-]?key)(["\s:=]+)([^\s,;"}]+)`)
var userinfo = regexp.MustCompile(`(https?://)[^/@\s]+:[^/@\s]+@`)

func Redact(s string) string {
	s = jsonSensitive.ReplaceAllString(s, `${1}"[REDACTED]"`)
	s = quotedSensitive.ReplaceAllString(s, `${1}${2}[REDACTED]`)
	s = bearer.ReplaceAllString(s, `${1}[REDACTED]`)
	return userinfo.ReplaceAllString(sensitive.ReplaceAllString(s, "${1}${2}[REDACTED]"), "${1}[REDACTED]@")
}
