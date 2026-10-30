package browsersecurity

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

const SessionCookie = "open_ai_canvas_session"
const policyKey = "agriculture.browsersecurity"
const CSRFHeader = "X-CSRF-Token"

func Configured(c *gin.Context) bool { _, ok := c.Get(policyKey); return ok }
func CookieSecure(c *gin.Context) (bool, bool) {
	v, ok := c.Get(policyKey)
	if !ok {
		return false, false
	}
	p, ok := v.(*Policy)
	if !ok {
		return false, false
	}
	return p.Secure, true
}

func (p *Policy) guestCookie() string {
	if p.Secure {
		return "__Host-agriculture-preauth"
	}
	return "agriculture-preauth-dev"
}

func reject(c *gin.Context, reason string) {
	c.Header("Cache-Control", "no-store")
	c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"code": 403, "data": nil, "msg": "请求安全校验失败", "reason": reason})
}

func oneHeader(r *http.Request, name string) (string, bool) {
	values := r.Header.Values(name)
	returnValue := ""
	if len(values) == 1 {
		returnValue = values[0]
	}
	return returnValue, len(values) == 1 && returnValue != "" && strings.TrimSpace(returnValue) == returnValue
}

// Cookie ambiguity must not let the CSRF boundary and authentication handler
// choose different credentials. This also rejects duplicate Path cookies.
func oneCookie(r *http.Request, name string) (string, bool) {
	var found string
	count := 0
	for _, cookie := range r.Cookies() {
		if cookie.Name == name {
			count++
			found = cookie.Value
		}
	}
	return found, count == 1 && found != "" && len(found) <= 4096
}

func csrfToken(kind, credential string) string {
	sum := sha256.Sum256([]byte("weavesense-agriculture-csrf-v1\x00" + kind + "\x00" + credential))
	return hex.EncodeToString(sum[:])
}

func (p *Policy) credential(r *http.Request) (string, string, bool) {
	// Presence of any session cookie forbids falling back to an anonymous token.
	for _, cookie := range r.Cookies() {
		if cookie.Name == SessionCookie {
			value, ok := oneCookie(r, SessionCookie)
			return "session", value, ok
		}
	}
	value, ok := oneCookie(r, p.guestCookie())
	parts := strings.Split(value, ".")
	if !ok || len(parts) != 3 || len(parts[1]) != 64 || len(parts[2]) != 64 {
		return "preauth", "", false
	}
	expires, err := strconv.ParseInt(parts[0], 10, 64)
	now := p.now().Unix()
	// The authenticated issuer fixes TTL=600s. Allow at most five seconds of
	// replica clock skew in the future bound, never extend an expired nonce.
	if err != nil || expires <= now || expires > now+605 {
		return "preauth", "", false
	}
	mac := p.guestMAC(parts[0] + "." + parts[1])
	return "preauth", value, hmac.Equal([]byte(parts[2]), []byte(mac))
}

func (p *Policy) guestMAC(value string) string {
	mac := hmac.New(sha256.New, p.guestKey[:])
	mac.Write([]byte("agriculture-preauth-v1\x00" + value))
	return hex.EncodeToString(mac.Sum(nil))
}

// Anonymous nonces are short-lived CSRF context, never identity. All replicas
// load the same dedicated key file; rotation affects only anonymous nonces.
func ClearPreauth(c *gin.Context) {
	v, ok := c.Get(policyKey)
	if !ok {
		return
	}
	p := v.(*Policy)
	http.SetCookie(c.Writer, &http.Cookie{Name: p.guestCookie(), Path: "/", HttpOnly: true, Secure: p.Secure, SameSite: http.SameSiteLaxMode, MaxAge: -1})
}

func browserWrite(path string) bool {
	return path == "/api/agriculture" || strings.HasPrefix(path, "/api/agriculture/") || strings.HasPrefix(path, "/api/auth/")
}

func hasSessionCookie(r *http.Request) bool {
	for _, cookie := range r.Cookies() {
		if cookie.Name == SessionCookie {
			return true
		}
	}
	return false
}

// Middleware must be installed before every route in an agriculture deployment.
// Internal service callbacks are not browser routes and retain signed auth.
func (p *Policy) Middleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Set(policyKey, p)
		if strings.HasPrefix(c.Request.URL.Path, "/api/") {
			counts := map[string]int{}
			for _, cookie := range c.Request.Cookies() {
				if cookie.Name == SessionCookie || cookie.Name == p.guestCookie() {
					counts[cookie.Name]++
					if counts[cookie.Name] > 1 {
						reject(c, "csrf_invalid")
						return
					}
				}
			}
		}
		origin, hasOrigin := oneHeader(c.Request, "Origin")
		if len(c.Request.Header.Values("Origin")) > 0 && (!hasOrigin || origin != p.Origin) {
			reject(c, "origin_denied")
			return
		}
		if hasOrigin {
			c.Header("Access-Control-Allow-Origin", p.Origin)
			c.Header("Access-Control-Allow-Credentials", "true")
		}
		c.Header("Vary", "Origin, Cookie")
		if c.Request.Method == http.MethodOptions {
			if !hasOrigin {
				reject(c, "origin_denied")
				return
			}
			c.Header("Access-Control-Allow-Methods", "GET, HEAD, POST, PUT, PATCH, DELETE, OPTIONS")
			c.Header("Access-Control-Allow-Headers", "Content-Type, X-CSRF-Token, Idempotency-Key, If-Match, X-Request-ID")
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		if c.Request.URL.Path == "/api/auth/csrf" && c.Request.Method == http.MethodGet {
			if c.GetHeader("Sec-Fetch-Site") == "cross-site" {
				reject(c, "origin_denied")
				return
			}
			kind, value, ok := p.credential(c.Request)
			if !ok {
				if kind == "session" {
					reject(c, "csrf_invalid")
					return
				}
				var nonce [32]byte
				if _, err := rand.Read(nonce[:]); err != nil {
					c.AbortWithStatus(http.StatusServiceUnavailable)
					return
				}
				value = strconv.FormatInt(p.now().Unix()+600, 10) + "." + hex.EncodeToString(nonce[:])
				value += "." + p.guestMAC(value)
				http.SetCookie(c.Writer, &http.Cookie{Name: p.guestCookie(), Value: value, Path: "/", HttpOnly: true, Secure: p.Secure, SameSite: http.SameSiteLaxMode, MaxAge: 600})
			}
			c.Header("Cache-Control", "no-store")
			c.AbortWithStatusJSON(http.StatusOK, gin.H{"code": 0, "data": gin.H{"token": csrfToken(kind, value)}, "msg": "ok"})
			return
		}
		if c.Request.Method != http.MethodGet && c.Request.Method != http.MethodHead && (browserWrite(c.Request.URL.Path) || (strings.HasPrefix(c.Request.URL.Path, "/api/") && hasSessionCookie(c.Request))) {
			if !hasOrigin || c.GetHeader("Sec-Fetch-Site") == "cross-site" {
				reject(c, "origin_denied")
				return
			}
			token, unique := oneHeader(c.Request, CSRFHeader)
			kind, value, ok := p.credential(c.Request)
			expected := csrfToken(kind, value)
			if !unique || !ok || len(token) != 64 || subtle.ConstantTimeCompare([]byte(token), []byte(expected)) != 1 {
				reject(c, "csrf_invalid")
				return
			}
		}
		c.Next()
	}
}
