// Package auth provides password hashing and signed session tokens for the
// public web build (login + role-based access). Self-contained, no JWT lib —
// tokens are base64(payload).base64(HMAC-SHA256), verified with a server secret.
package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// Roles, lowest → highest privilege.
const (
	RolePublic = "public"
	RoleMember = "member"
	RoleVIP    = "vip"
	RoleAdmin  = "admin"
)

var roleRank = map[string]int{RolePublic: 0, RoleMember: 1, RoleVIP: 2, RoleAdmin: 3}

// AtLeast reports whether have meets the minimum role want.
func AtLeast(have, want string) bool { return roleRank[have] >= roleRank[want] }

// HashPassword returns a bcrypt hash. CheckPassword verifies one.
func HashPassword(pw string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(pw), 12)
	return string(b), err
}

func CheckPassword(hash, pw string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(pw)) == nil
}

type claims struct {
	User string `json:"u"`
	Role string `json:"r"`
	Exp  int64  `json:"e"`
}

func sign(payload, secret []byte) string {
	m := hmac.New(sha256.New, secret)
	m.Write(payload)
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// IssueToken signs a token valid for ttl.
func IssueToken(user, role, secret string, ttl time.Duration) string {
	c, _ := json.Marshal(claims{User: user, Role: role, Exp: time.Now().Add(ttl).Unix()})
	p := base64.RawURLEncoding.EncodeToString(c)
	return p + "." + sign([]byte(p), []byte(secret))
}

// ParseToken verifies the signature + expiry and returns the user and role.
func ParseToken(token, secret string) (user, role string, err error) {
	parts := strings.SplitN(token, ".", 2)
	if len(parts) != 2 {
		return "", "", errors.New("bad token")
	}
	if !hmac.Equal([]byte(parts[1]), []byte(sign([]byte(parts[0]), []byte(secret)))) {
		return "", "", errors.New("bad signature")
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return "", "", err
	}
	var c claims
	if err := json.Unmarshal(raw, &c); err != nil {
		return "", "", err
	}
	if time.Now().Unix() > c.Exp {
		return "", "", errors.New("expired")
	}
	return c.User, c.Role, nil
}
