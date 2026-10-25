package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func TestPasswordHashAndCheck(t *testing.T) {
	if _, err := HashPassword("short"); err == nil {
		t.Fatalf("passwords under 8 characters must be rejected")
	}
	hash, err := HashPassword("s3cret-password")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if string(hash) == "s3cret-password" {
		t.Fatalf("hash must not be the plaintext")
	}
	if !CheckPassword(hash, "s3cret-password") {
		t.Fatalf("correct password rejected")
	}
	if CheckPassword(hash, "wrong-password") || CheckPassword(nil, "s3cret-password") || CheckPassword([]byte("garbage"), "s3cret-password") {
		t.Fatalf("wrong password / bad hash accepted")
	}
}

func issueCookie(t *testing.T, s *Session, user string) *http.Cookie {
	t.Helper()
	rec := httptest.NewRecorder()
	if err := s.Issue(rec, user); err != nil {
		t.Fatalf("Issue: %v", err)
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == CookieName {
			return c
		}
	}
	t.Fatalf("no session cookie set")
	return nil
}

func requestWith(c *http.Cookie) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/auth/me", nil)
	if c != nil {
		r.AddCookie(c)
	}
	return r
}

func TestSessionRoundTrip(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	s := NewSession(key, time.Hour, true)
	c := issueCookie(t, s, "alice")
	if !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteLaxMode || c.Path != "/" {
		t.Fatalf("cookie attributes: %+v", c)
	}
	sub, err := s.Subject(requestWith(c))
	if err != nil || sub != "alice" {
		t.Fatalf("Subject: %q %v", sub, err)
	}

	// 无 cookie / 篡改 / 另一把钥匙 / 过期 都应失效
	if _, err := s.Subject(requestWith(nil)); err == nil {
		t.Fatalf("missing cookie accepted")
	}
	bad := *c
	bad.Value = c.Value[:len(c.Value)-2] + "xx"
	if _, err := s.Subject(requestWith(&bad)); err == nil {
		t.Fatalf("tampered token accepted")
	}
	other := NewSession([]byte("another-key-another-key-another!"), time.Hour, false)
	if _, err := other.Subject(requestWith(c)); err == nil {
		t.Fatalf("token signed with a different key accepted")
	}
	expired, err := s.Sign(jwt.RegisteredClaims{
		Issuer: jwtIssuer, Subject: "alice",
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(-2 * time.Minute)),
	})
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if _, err := s.Subject(requestWith(&http.Cookie{Name: CookieName, Value: expired})); err == nil {
		t.Fatalf("expired token accepted")
	}
	noExp, _ := s.Sign(jwt.RegisteredClaims{Issuer: jwtIssuer, Subject: "alice"})
	if _, err := s.Subject(requestWith(&http.Cookie{Name: CookieName, Value: noExp})); err == nil {
		t.Fatalf("token without exp accepted")
	}

	// 不安全（http）部署下 cookie 不带 Secure
	insecure := NewSession(key, time.Hour, false)
	if issueCookie(t, insecure, "bob").Secure {
		t.Fatalf("Secure must follow the flag")
	}
	rec := httptest.NewRecorder()
	s.Clear(rec)
	if cs := rec.Result().Cookies(); len(cs) != 1 || cs[0].MaxAge != -1 {
		t.Fatalf("Clear must expire the cookie: %+v", cs)
	}
}

func TestRateLimiterLockout(t *testing.T) {
	l := NewRateLimiter(3, time.Minute)
	now := time.Unix(1_700_000_000, 0)
	l.now = func() time.Time { return now }

	if !l.Allowed("alice", "1.2.3.4") {
		t.Fatalf("fresh keys must be allowed")
	}
	l.Fail("alice", "1.2.3.4")
	l.Fail("alice", "1.2.3.4")
	if !l.Allowed("alice", "1.2.3.4") {
		t.Fatalf("below threshold must still be allowed")
	}
	l.Fail("alice", "1.2.3.4")
	if l.Allowed("alice") || l.Allowed("1.2.3.4") || l.Allowed("bob", "1.2.3.4") {
		t.Fatalf("locked key must block, including when combined with an innocent key")
	}
	if !l.Allowed("bob", "5.6.7.8") {
		t.Fatalf("unrelated keys must not be affected")
	}
	now = now.Add(61 * time.Second)
	if !l.Allowed("alice", "1.2.3.4") {
		t.Fatalf("lock must expire")
	}
}
