package httpapi

// ssrf.go guards the one outbound request in this server whose target
// is not an admin-configured value: fetchResource (#107) dials a URL
// taken from note content a user imported. Every other outbound
// client here (Karakeep, Slink, S3, webhooks) trusts its target
// because an operator typed it into config; this one cannot.

import (
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"syscall"
	"time"
)

// guardedFetchClient builds an http.Client whose Dialer.Control runs
// after DNS resolution but before the connect(2) syscall, so it
// inspects the actual IP the dial is about to use — the standard
// defense against a hostname that resolves to a public address during
// an earlier check and a private one by the time the connection is
// actually made (DNS rebinding). Redirects are never followed: a 30x
// to an internal address would otherwise walk straight around the
// guard, since Go only re-runs DialContext, not an app-level recheck,
// unless the caller follows the redirect itself.
func guardedFetchClient(timeout time.Duration) *http.Client {
	dialer := &net.Dialer{
		Timeout: timeout,
		Control: func(_, address string, _ syscall.RawConn) error {
			return checkDialAddress(address)
		},
	}
	return &http.Client{
		Timeout: timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
		Transport: &http.Transport{
			DialContext: dialer.DialContext,
		},
	}
}

// checkDialAddress rejects a "host:port" dial target whose host is not
// a publicly routable IP literal. By the time Control runs, address is
// always an IP literal (the resolver already ran), never a hostname.
func checkDialAddress(address string) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("invalid dial address %q", address)
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return fmt.Errorf("invalid dial address %q", host)
	}
	if !isPubliclyRoutable(addr) {
		return fmt.Errorf("refusing to connect to %s: not a public address", host)
	}
	return nil
}

// isPubliclyRoutable rejects every address range a note's attachment
// URL should never be able to reach: loopback (127.0.0.1, ::1),
// RFC1918/ULA private ranges, link-local (including the
// 169.254.169.254 cloud metadata address every provider uses),
// unspecified (0.0.0.0, ::) and multicast.
func isPubliclyRoutable(addr netip.Addr) bool {
	addr = addr.Unmap()
	if !addr.IsValid() {
		return false
	}
	switch {
	case addr.IsLoopback(),
		addr.IsPrivate(),
		addr.IsLinkLocalUnicast(),
		addr.IsLinkLocalMulticast(),
		addr.IsInterfaceLocalMulticast(),
		addr.IsMulticast(),
		addr.IsUnspecified():
		return false
	}
	return true
}
