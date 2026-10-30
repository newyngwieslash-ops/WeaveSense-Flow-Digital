// Package browsersecurity is the explicit agriculture browser boundary. It does
// not infer a public origin from proxy headers or change the legacy film profile.
package browsersecurity

import (
	"crypto/rand"
	"errors"
	"io"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type Policy struct {
	Origin   string
	Secure   bool
	guestKey [32]byte
	now      func() time.Time
}

func FromEnv() (*Policy, error) {
	enabled, err := strconv.ParseBool(valueOr("FLOW_AGRICULTURE_ENABLED", "false"))
	if err != nil {
		return nil, errors.New("invalid agriculture enablement")
	}
	if !enabled {
		return nil, nil
	}
	dev, err := strconv.ParseBool(valueOr("FLOW_AGRICULTURE_ALLOW_HTTP_LOOPBACK_DEV", "false"))
	if err != nil {
		return nil, errors.New("invalid agriculture HTTP development configuration")
	}
	p, err := Parse(os.Getenv("FLOW_AGRICULTURE_EXTERNAL_ORIGIN"), dev)
	if err != nil {
		return nil, err
	}
	key, err := readKey(os.Getenv("FLOW_AGRICULTURE_CSRF_KEY_FILE"))
	if err != nil {
		return nil, err
	}
	p.guestKey = key
	return p, nil
}

func readKey(path string) ([32]byte, error) {
	var key [32]byte
	invalid := errors.New("agriculture CSRF key must be a configured private regular file containing exactly 32 raw bytes")
	before, err := os.Lstat(path)
	if err != nil || !before.Mode().IsRegular() || before.Mode().Perm()&0077 != 0 || before.Size() != 32 {
		return key, invalid
	}
	f, err := os.Open(path)
	if err != nil {
		return key, invalid
	}
	defer f.Close()
	after, err := f.Stat()
	if err != nil || !os.SameFile(before, after) || !after.Mode().IsRegular() || after.Mode().Perm()&0077 != 0 || after.Size() != 32 {
		return key, invalid
	}
	data, err := io.ReadAll(io.LimitReader(f, 33))
	if err != nil || len(data) != 32 {
		return key, invalid
	}
	copy(key[:], data)
	return key, nil
}

func valueOr(key, fallback string) string {
	if s := os.Getenv(key); s != "" {
		return s
	}
	return fallback
}

func Parse(origin string, dev bool) (*Policy, error) {
	u, err := url.Parse(origin)
	invalid := errors.New("agriculture requires one canonical HTTPS external origin (literal loopback HTTP requires explicit development opt-in)")
	if err != nil || origin == "" || strings.TrimSpace(origin) != origin || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" || u.Scheme+"://"+u.Host != origin {
		return nil, invalid
	}
	if strings.ContainsAny(u.Host, "*,\\\t\r\n ") || strings.ToLower(u.Host) != u.Host {
		return nil, invalid
	}
	if _, e := netip.ParseAddr(u.Hostname()); e != nil {
		if len(u.Hostname()) == 0 || len(u.Hostname()) > 253 {
			return nil, invalid
		}
		for _, label := range strings.Split(u.Hostname(), ".") {
			if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
				return nil, invalid
			}
			for _, ch := range label {
				if !(ch >= 'a' && ch <= 'z' || ch >= '0' && ch <= '9' || ch == '-') {
					return nil, invalid
				}
			}
		}
	}
	if port := u.Port(); port != "" {
		n, e := strconv.Atoi(port)
		if e != nil || n < 1 || n > 65535 || strconv.Itoa(n) != port {
			return nil, invalid
		}
		// Browsers serialize default ports away. Refuse an origin that could
		// never exactly match the browser rather than silently rewriting it.
		if u.Scheme == "https" && n == 443 || u.Scheme == "http" && n == 80 {
			return nil, invalid
		}
	}
	if strings.HasSuffix(u.Host, ":") {
		return nil, invalid
	}
	if u.Scheme == "https" {
		return newPolicy(origin, true)
	}
	addr, e := netip.ParseAddr(u.Hostname())
	if u.Scheme != "http" || !dev || e != nil || (addr.String() != "127.0.0.1" && addr.String() != "::1") {
		return nil, invalid
	}
	return newPolicy(origin, false)
}

func newPolicy(origin string, secure bool) (*Policy, error) {
	p := &Policy{Origin: origin, Secure: secure, now: time.Now}
	if _, err := rand.Read(p.guestKey[:]); err != nil {
		return nil, err
	}
	return p, nil
}
