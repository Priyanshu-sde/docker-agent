package httpclient

import (
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIsPublicIP(t *testing.T) {
	t.Parallel()

	tests := []struct {
		ip       string
		isPublic bool
	}{
		{"8.8.8.8", true},
		{"1.1.1.1", true},
		{"2001:4860:4860::8888", true}, // public IPv6

		{"127.0.0.1", false},
		{"::1", false},
		{"10.0.0.1", false},
		{"172.16.0.1", false},
		{"192.168.0.1", false},
		{"169.254.169.254", false}, // AWS/GCP/Azure metadata
		{"fe80::1", false},         // link-local IPv6
		{"224.0.0.1", false},       // multicast
		{"0.0.0.0", false},
		{"::", false},

		// CGNAT RFC 6598 (100.64.0.0/10)
		{"100.64.0.1", false},
		{"100.127.255.254", false},
		{"100.63.255.255", true}, // just below CGNAT
		{"100.128.0.1", true},    // just above CGNAT

		// 6to4 (RFC 3056, 2002::/16)
		{"2002:a9fe:a9fe::", false}, // encodes 169.254.169.254
		{"2002:0a00:0001::", false}, // encodes 10.0.0.1
		{"2002::1", false},
		{"2001::1", true}, // just outside
		{"2003::1", true}, // just outside

		// NAT64 well-known (RFC 6052, 64:ff9b::/96)
		{"64:ff9b::a9fe:a9fe", false}, // encodes 169.254.169.254
		{"64:ff9b::a00:1", false},     // encodes 10.0.0.1
		{"64:ff9c::1", true},          // just outside

		// NAT64 local-use (RFC 8215, 64:ff9b:1::/48)
		{"64:ff9b:1::1", false},
		{"64:ff9b:1:abcd::", false},
		{"64:ff9b:2::1", true}, // just outside

		// Site-local (RFC 3879, fec0::/10)
		{"fec0::1", false},
		{"feff::1", false},
		{"febf::1", false}, // just below fec0::/10; fe80::/10 (link-local) extends to febf::, so still blocked
	}
	for _, tt := range tests {
		t.Run(tt.ip, func(t *testing.T) {
			t.Parallel()
			ip := net.ParseIP(tt.ip)
			require.NotNil(t, ip, "failed to parse IP %q", tt.ip)
			assert.Equal(t, tt.isPublic, IsPublicIP(ip))
		})
	}
}

func TestSSRFDialControl(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		address string
		wantErr string // empty means no error
	}{
		{"public IPv4", "8.8.8.8:443", ""},
		{"public IPv6", "[2001:4860:4860::8888]:443", ""},
		{"loopback IPv4", "127.0.0.1:80", "non-public"},
		{"loopback IPv6", "[::1]:80", "non-public"},
		{"RFC1918", "10.0.0.1:80", "non-public"},
		{"link-local cloud metadata", "169.254.169.254:80", "non-public"},
		{"unspecified", "0.0.0.0:80", "non-public"},
		{"hostname (not an IP)", "example.com:80", "not a valid IP"},
		{"missing port", "8.8.8.8", "parsing dial address"},

		// New SSRF ranges
		{"CGNAT", "[100.64.0.1]:80", "non-public"},
		{"6to4 encodes link-local", "[2002:a9fe:a9fe::]:443", "non-public"},
		{"NAT64 well-known", "[64:ff9b::a9fe:a9fe]:443", "non-public"},
		{"NAT64 local-use", "[64:ff9b:1::1]:443", "non-public"},
		{"site-local IPv6", "[fec0::1]:443", "non-public"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := SSRFDialControl("tcp", tt.address, nil)
			if tt.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestBoundedRedirects(t *testing.T) {
	t.Parallel()

	check := BoundedRedirects(3)

	mustParse := func(s string) *url.URL {
		u, err := url.Parse(s)
		require.NoError(t, err)
		return u
	}

	// Within bound: no error.
	for n := range 3 {
		t.Run("within-bound", func(t *testing.T) {
			via := make([]*http.Request, n)
			req := &http.Request{URL: mustParse("https://example.com/")}
			assert.NoError(t, check(req, via))
		})
	}

	// At the bound: rejected.
	via := make([]*http.Request, 3)
	req := &http.Request{URL: mustParse("https://example.com/")}
	err := check(req, via)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "stopped after 3 redirects")
}

func TestHTTPSOnlyRedirects(t *testing.T) {
	t.Parallel()

	mustParse := func(s string) *url.URL {
		u, err := url.Parse(s)
		require.NoError(t, err)
		return u
	}

	tests := []struct {
		name    string
		target  string
		via     int    // length of the via slice
		wantErr string // empty means no error
	}{
		{"https redirect allowed", "https://example.com/agent.yaml", 1, ""},
		{"http redirect rejected", "http://example.com/agent.yaml", 1, "non-https"},
		{"file redirect rejected", "file:///etc/passwd", 1, "non-https"},
		{"javascript redirect rejected", "javascript:alert(1)", 1, "non-https"},
		{"ftp redirect rejected", "ftp://example.com/x", 1, "non-https"},
		{"redirect loop bounded", "https://example.com/agent.yaml", 10, "10 redirects"},
	}
	check := HTTPSOnlyRedirects(10)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			req := &http.Request{URL: mustParse(tt.target)}
			via := make([]*http.Request, tt.via)
			err := check(req, via)
			if tt.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestLocalhostOnlyRedirects(t *testing.T) {
	t.Parallel()

	mustParse := func(s string) *url.URL {
		u, err := url.Parse(s)
		require.NoError(t, err)
		return u
	}

	tests := []struct {
		name    string
		target  string
		via     int
		wantErr string
	}{
		{"localhost http allowed", "http://localhost/agent.yaml", 1, ""},
		{"localhost http with port allowed", "http://localhost:8080/agent.yaml", 1, ""},
		{"non-localhost rejected", "http://example.com/agent.yaml", 1, "non-localhost"},
		{"https localhost rejected", "https://localhost/agent.yaml", 1, "non-localhost"},
		{"metadata endpoint rejected", "http://169.254.169.254/latest/meta-data/", 1, "non-localhost"},
		{"private IP rejected", "http://10.0.0.1/agent.yaml", 1, "non-localhost"},
		{"loopback IP rejected", "http://127.0.0.1/agent.yaml", 1, "non-localhost"},
		{"redirect loop bounded", "http://localhost/agent.yaml", 10, "10 redirects"},
	}
	check := LocalhostOnlyRedirects(10)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			req := &http.Request{URL: mustParse(tt.target)}
			via := make([]*http.Request, tt.via)
			err := check(req, via)
			if tt.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestNewSSRFSafeTransport_RefusesPrivateIP(t *testing.T) {
	t.Parallel()

	// Drive the SSRF check end-to-end through an http.Client. We don't
	// need a server: the dial-time hook fires before any TCP handshake.
	client := &http.Client{Transport: NewSSRFSafeTransport()}

	tests := []string{
		"http://127.0.0.1/",
		"http://[::1]/",
		"http://10.0.0.1/",
		"http://169.254.169.254/latest/meta-data/",
		"http://0.0.0.0/",
	}
	for _, target := range tests {
		t.Run(target, func(t *testing.T) {
			t.Parallel()
			req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, target, http.NoBody)
			require.NoError(t, err)
			resp, err := client.Do(req)
			if resp != nil {
				_ = resp.Body.Close()
			}
			require.Error(t, err)
			assert.True(
				t,
				strings.Contains(err.Error(), "non-public address") ||
					strings.Contains(err.Error(), "not a valid IP"),
				"unexpected error %q", err.Error(),
			)
		})
	}
}

// TestIsPublicIP_IPv4MappedIPv6 is a regression test that pins Go's
// behaviour: IPv4-mapped IPv6 addresses (::ffff:a.b.c.d) inherit the
// classification of their embedded IPv4 address. This prevents an
// attacker from bypassing SSRF checks by wrapping 169.254.169.254
// in IPv6 notation.
func TestIsPublicIP_IPv4MappedIPv6(t *testing.T) {
	t.Parallel()

	tests := []struct {
		addr     string
		isPublic bool
	}{
		{"::ffff:127.0.0.1", false},       // loopback
		{"::ffff:10.0.0.1", false},        // RFC1918
		{"::ffff:169.254.169.254", false}, // cloud metadata (link-local)
		{"::ffff:8.8.8.8", true},          // public
	}

	for _, tt := range tests {
		t.Run(tt.addr, func(t *testing.T) {
			t.Parallel()
			ip := net.ParseIP(tt.addr)
			require.NotNil(t, ip, "ParseIP must succeed")
			assert.Equal(t, tt.isPublic, IsPublicIP(ip))
		})
	}
}

// TestSSRFDialControl_IPv6ZoneID verifies that IPv6 addresses with zone
// identifiers (fe80::1%eth0) are rejected. net.ParseIP returns nil for
// such addresses, causing the dial control to fail-closed with "not a
// valid IP" rather than potentially bypassing the public-IP check.
func TestSSRFDialControl_IPv6ZoneID(t *testing.T) {
	t.Parallel()

	err := SSRFDialControl("tcp", "[fe80::1%eth0]:80", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a valid IP")
}

// TestProxyHostPorts pins the parsing rules for HTTP_PROXY-style values.
// We support full URLs and bare host[:port] (matching net/http's
// ProxyFromEnvironment), and we always emit a port — dial addresses
// have one and we compare against them verbatim.
func TestProxyHostPorts(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		spec string
		want []string
	}{
		{"empty", "", nil},
		{"http URL with port", "http://172.17.0.0:3128", []string{"172.17.0.0:3128"}},
		{"http URL without port", "http://10.0.0.1", []string{"10.0.0.1:80"}},
		{"https URL without port", "https://10.0.0.1", []string{"10.0.0.1:443"}},
		{"socks5 URL without port", "socks5://10.0.0.1", []string{"10.0.0.1:1080"}},
		{"bare host:port", "172.17.0.0:3128", []string{"172.17.0.0:3128"}},
		{"bare host", "172.17.0.0", []string{"172.17.0.0:80"}},
		{"IPv6 with port", "http://[::1]:3128", []string{"[::1]:3128"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := proxyHostPorts(tt.spec)
			if tt.want == nil {
				assert.Empty(t, got)
				return
			}
			for _, w := range tt.want {
				assert.Contains(t, got, w)
			}
		})
	}
}

// TestNewSSRFSafeTransport_AllowsConfiguredProxy is the regression test
// for the docker-agent sandbox bug: HTTP_PROXY=http://172.17.0.0:3128
// must not be rejected by SSRFDialControl, because that's the only way
// outbound traffic can reach the public internet from inside the
// sandbox. We verify the dial is *attempted* (i.e. SSRF check passes)
// by using a port we know is closed, then asserting the resulting
// error is a connection error, not an SSRF rejection.
func TestNewSSRFSafeTransport_AllowsConfiguredProxy(t *testing.T) {
	// Bind a socket and immediately close it, then reuse the freed port:
	// connections to it will fail with ECONNREFUSED, proving the dial
	// passed the SSRF gate. We use 127.0.0.1 as a stand-in for any
	// private-IP proxy — same code path.
	var lc net.ListenConfig
	ln, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()
	require.NoError(t, ln.Close())

	t.Setenv("HTTP_PROXY", "http://"+addr)
	t.Setenv("HTTPS_PROXY", "")
	t.Setenv("http_proxy", "")
	t.Setenv("https_proxy", "")

	// Override the transport's Proxy directly so the test does not
	// depend on http.ProxyFromEnvironment's sync.Once-cached env
	// snapshot, which can be frozen by an earlier test in the same
	// process before we get a chance to set HTTP_PROXY.
	proxyURL, err := url.Parse("http://" + addr)
	require.NoError(t, err)
	transport := NewSSRFSafeTransport()
	transport.Proxy = http.ProxyURL(proxyURL)

	client := &http.Client{Transport: transport}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://example.com/", http.NoBody)
	require.NoError(t, err)
	resp, reqErr := client.Do(req)
	if resp != nil {
		_ = resp.Body.Close()
	}
	require.Error(t, reqErr)
	assert.NotContains(t, reqErr.Error(), "non-public address",
		"explicitly-configured proxy must bypass SSRF dial-control")
	assert.NotContains(t, reqErr.Error(), "not a valid IP")
}

// TestCanonicalHostPort verifies that IPv4-mapped IPv6 dial addresses
// (e.g. "[::ffff:10.0.0.1]:3128", which dual-stack Linux dialers may
// emit even for IPv4-literal proxy URLs) collapse to the same key as
// the dotted-quad form, so the allowlist match doesn't miss.
func TestCanonicalHostPort(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in, want string
	}{
		{"10.0.0.1:3128", "10.0.0.1:3128"},
		{"[::ffff:10.0.0.1]:3128", "10.0.0.1:3128"},
		{"[::ffff:0a00:0001]:3128", "10.0.0.1:3128"},
		{"[2001:db8::1]:3128", "[2001:db8::1]:3128"},
		{"example.com:3128", "example.com:3128"},
		{"not a valid address", "not a valid address"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, canonicalHostPort(tt.in))
		})
	}
}

// wrappedRoundTripper wraps an http.RoundTripper without exposing it, mimicking
// the posture of otelhttp.Transport (which does not implement Unwrap).
type wrappedRoundTripper struct{ http.RoundTripper }

// TestNewSSRFSafeTransport_WrappedDefaultTransport verifies that
// NewSSRFSafeTransport does not panic when http.DefaultTransport has been
// replaced by a non-*http.Transport wrapper (e.g. otelhttp.NewTransport).
// The returned transport must still enforce the SSRF dial check.
func TestNewSSRFSafeTransport_WrappedDefaultTransport(t *testing.T) {
	// Intentionally not parallel: mutates the http.DefaultTransport global.
	// Go runs non-parallel tests before paused parallel ones, and the
	// t.Cleanup below restores the original before any parallel test reads
	// it — adding t.Parallel() here would break that guarantee.
	orig := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = orig })
	http.DefaultTransport = wrappedRoundTripper{orig}

	transport := NewSSRFSafeTransport() // must not panic

	client := &http.Client{Transport: transport}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://127.0.0.1/", http.NoBody)
	require.NoError(t, err)
	resp, reqErr := client.Do(req)
	if resp != nil {
		_ = resp.Body.Close()
	}
	require.Error(t, reqErr)
	assert.Contains(t, reqErr.Error(), "non-public address")
}

// TestNewSSRFSafeTransport_AllowlistFrozenAtConstruction documents that
// the allowlist is captured when the transport is built, not on every
// dial. Changing the env after construction has no effect — acceptable
// because proxy env vars are set at process start.
func TestNewSSRFSafeTransport_AllowlistFrozenAtConstruction(t *testing.T) {
	t.Setenv("HTTP_PROXY", "")
	t.Setenv("HTTPS_PROXY", "")
	t.Setenv("http_proxy", "")
	t.Setenv("https_proxy", "")

	client := &http.Client{Transport: NewSSRFSafeTransport()}

	t.Setenv("HTTP_PROXY", "http://10.0.0.1:3128")

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://10.0.0.1:3128/", http.NoBody)
	require.NoError(t, err)
	resp, reqErr := client.Do(req)
	if resp != nil {
		_ = resp.Body.Close()
	}
	require.Error(t, reqErr)
	assert.Contains(t, reqErr.Error(), "non-public address")
}
