package httpclient

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"syscall"
	"time"
)

// IsPublicIP reports whether ip is a routable public address. It rejects
// loopback (127/8, ::1), RFC1918 private ranges, link-local (incl. the
// 169.254.169.254 cloud metadata endpoint), multicast and the unspecified
// address (0.0.0.0, ::).
func IsPublicIP(ip net.IP) bool {
	return !ip.IsLoopback() &&
		!ip.IsPrivate() &&
		!ip.IsLinkLocalUnicast() &&
		!ip.IsLinkLocalMulticast() &&
		!ip.IsMulticast() &&
		!ip.IsUnspecified()
}

// SSRFDialControl is invoked by net.Dialer after DNS resolution but before
// the TCP handshake. It rejects addresses that are not safe to fetch from
// over the public internet.
//
// Performing the check post-resolution defeats DNS rebinding: an attacker
// cannot point a public hostname at 127.0.0.1 or 169.254.169.254 to bypass
// us, because we re-validate the resolved IP itself.
func SSRFDialControl(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("parsing dial address %q: %w", address, err)
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return fmt.Errorf("refusing to dial %q: not a valid IP", host)
	}
	if !IsPublicIP(ip) {
		return fmt.Errorf("refusing to dial non-public address %s", ip)
	}
	return nil
}

// NewSSRFSafeTransport returns a clone of [http.DefaultTransport] whose
// dialer enforces [SSRFDialControl] on every connection. All other settings
// — proxy, idle pool, HTTP/2, timeouts — are inherited so the transport
// keeps up with future stdlib changes.
//
// Use this for outbound HTTP that may follow attacker-influenced URLs
// (OpenAPI specs whose servers[] list is taken from the spec body,
// user-configured API endpoints, etc.). It does not enforce HTTPS —
// callers that require it must validate the request URL themselves
// and/or supply a CheckRedirect on the surrounding *http.Client.
//
// As an exception, the explicitly-configured HTTP/HTTPS/ALL proxy is
// always dialable, even if it lives on a private address. Refusing to
// dial the operator-configured proxy adds no SSRF protection (the proxy
// enforces destination policy itself) and breaks sandboxes — like
// docker-agent's — whose mandatory egress proxy is on an RFC1918 IP.
func NewSSRFSafeTransport() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	proxies := proxyDialAllowlist()
	t.DialContext = (&net.Dialer{
		Timeout:   30 * time.Second,
		KeepAlive: 30 * time.Second,
		Control: func(network, address string, c syscall.RawConn) error {
			if _, ok := proxies[canonicalHostPort(address)]; ok {
				return nil
			}
			return SSRFDialControl(network, address, c)
		},
	}).DialContext
	return t
}

// canonicalHostPort normalises a "host:port" dial address so equivalent
// IPv4 forms compare equal. An IPv4-mapped IPv6 address (::ffff:a.b.c.d)
// is folded back to its dotted-quad form, matching what proxyHostPorts
// stores in the allowlist for an IPv4-literal proxy. Non-IP hosts and
// malformed inputs are returned unchanged so the allowlist lookup
// simply misses and the SSRF fall-through applies.
func canonicalHostPort(address string) string {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return address
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return address
	}
	if ip4 := ip.To4(); ip4 != nil {
		return net.JoinHostPort(ip4.String(), port)
	}
	return net.JoinHostPort(ip.String(), port)
}

// proxyDialAllowlist returns the set of "host:port" strings that the
// HTTP_PROXY / HTTPS_PROXY / ALL_PROXY environment variables resolve to.
// Hostname-based proxies are resolved to all of their A/AAAA records so
// the comparison can be done against the post-resolution dial address.
// The result is a snapshot taken at call time — safe to capture in a
// dialer's Control closure.
func proxyDialAllowlist() map[string]struct{} {
	out := map[string]struct{}{}
	raw := []string{
		os.Getenv("HTTP_PROXY"), os.Getenv("http_proxy"),
		os.Getenv("HTTPS_PROXY"), os.Getenv("https_proxy"),
		os.Getenv("ALL_PROXY"), os.Getenv("all_proxy"),
	}
	for _, s := range raw {
		for addr := range proxyHostPorts(s) {
			out[addr] = struct{}{}
		}
	}
	return out
}

// proxyHostPorts parses a single proxy specification (matching the syntax
// accepted by net/http.ProxyFromEnvironment: a full URL or a bare host[:port]
// in which case http:// is implied) and returns each "ip:port" it can be
// reached at. A nil/empty input yields an empty set.
func proxyHostPorts(spec string) map[string]struct{} {
	if spec == "" {
		return nil
	}
	u, err := url.Parse(spec)
	if err != nil || u.Host == "" {
		u, err = url.Parse("http://" + spec)
		if err != nil || u.Host == "" {
			return nil
		}
	}
	port := u.Port()
	if port == "" {
		switch u.Scheme {
		case "https":
			port = "443"
		case "socks5", "socks5h":
			port = "1080"
		default:
			port = "80"
		}
	}
	out := map[string]struct{}{}
	host := u.Hostname()
	if ip := net.ParseIP(host); ip != nil {
		out[net.JoinHostPort(ip.String(), port)] = struct{}{}
		return out
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ips, _ := net.DefaultResolver.LookupIPAddr(ctx, host)
	for _, ip := range ips {
		out[net.JoinHostPort(ip.IP.String(), port)] = struct{}{}
	}
	return out
}

// BoundedRedirects returns an http.Client.CheckRedirect that limits a
// redirect chain to maxHops. SSRF on each redirect target is enforced
// by the transport's dialer; this only prevents infinite loops.
func BoundedRedirects(maxHops int) func(*http.Request, []*http.Request) error {
	return func(_ *http.Request, via []*http.Request) error {
		if len(via) >= maxHops {
			return fmt.Errorf("stopped after %d redirects", maxHops)
		}
		return nil
	}
}

// HTTPSOnlyRedirects returns an http.Client.CheckRedirect that limits the
// redirect chain to maxHops AND rejects redirects whose Location is not
// https://. Use this when the original request is required to be HTTPS
// and a TLS downgrade through a Location header must be prevented.
func HTTPSOnlyRedirects(maxHops int) func(*http.Request, []*http.Request) error {
	return func(req *http.Request, via []*http.Request) error {
		if len(via) >= maxHops {
			return fmt.Errorf("stopped after %d redirects", maxHops)
		}
		if req.URL.Scheme != "https" {
			return fmt.Errorf("refusing redirect to non-https URL %q", req.URL.Redacted())
		}
		return nil
	}
}
