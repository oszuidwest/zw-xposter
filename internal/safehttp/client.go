// Package safehttp builds clients for fetching untrusted content URLs.
package safehttp

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strings"
	"syscall"
	"time"
)

const (
	requestTimeout = 30 * time.Second
	dialTimeout    = 10 * time.Second
	keepAlive      = 30 * time.Second
)

// New restricts HTTPS requests and redirects to the feed host and additionalHosts.
// Connections must resolve to public addresses; proxies are disabled.
func New(feedURL string, additionalHosts []string) (*http.Client, error) {
	feed, err := url.Parse(feedURL)
	if err != nil {
		return nil, fmt.Errorf("parse feed URL: %w", err)
	}
	if feed.Scheme != "https" || feed.Hostname() == "" {
		return nil, errors.New("FEED_URL must use https and include a host")
	}

	allowed := map[string]struct{}{normalizeHost(feed.Hostname()): {}}
	for _, raw := range additionalHosts {
		host, err := configuredHost(raw)
		if err != nil {
			return nil, err
		}
		allowed[host] = struct{}{}
	}

	dialer := &net.Dialer{
		Timeout:   dialTimeout,
		KeepAlive: keepAlive,
		Control:   blockRestrictedAddress,
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	// A proxy could resolve the destination itself and bypass the dial check.
	transport.Proxy = nil
	transport.DialContext = dialer.DialContext

	return &http.Client{
		Timeout: requestTimeout,
		Transport: &allowlistTransport{
			base:    transport,
			allowed: allowed,
		},
	}, nil
}

// allowlistTransport enforces the URL restrictions on every redirect hop too.
type allowlistTransport struct {
	base    http.RoundTripper
	allowed map[string]struct{}
}

func (t *allowlistTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := validateURL(req.URL, t.allowed); err != nil {
		return nil, err
	}
	return t.base.RoundTrip(req)
}

func validateURL(target *url.URL, allowed map[string]struct{}) error {
	if target == nil || target.Scheme != "https" || target.Hostname() == "" {
		return errors.New("content URL must use https and include a host")
	}
	host := normalizeHost(target.Hostname())
	if _, ok := allowed[host]; !ok {
		return fmt.Errorf("content host %q is not allowed", target.Hostname())
	}
	return nil
}

func configuredHost(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("ALLOWED_HOSTS contains an empty host")
	}
	if strings.ContainsAny(raw, "/?#@") {
		return "", fmt.Errorf("ALLOWED_HOSTS entry %q must be a host without a scheme or path", raw)
	}
	parsed, err := url.Parse("https://" + raw)
	if err != nil || parsed.Hostname() == "" || parsed.Port() != "" {
		return "", fmt.Errorf("ALLOWED_HOSTS entry %q is not a valid host", raw)
	}
	return normalizeHost(parsed.Hostname()), nil
}

func normalizeHost(host string) string {
	return strings.TrimSuffix(strings.ToLower(host), ".")
}

// globalUnicastIPv6 limits IPv6 to 2000::/3, excluding local and compatible ranges.
// NAT64 is also excluded: a translator could reach a restricted IPv4 address.
var globalUnicastIPv6 = netip.MustParsePrefix("2000::/3")

// specialPurpose blocks IANA special-purpose ranges the broader checks allow.
// Whole ranges are denied, including public exceptions in 192.0.0.0/24 and 2001::/23;
// content fetching needs none of those destinations.
var specialPurpose = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),       // "this network"
	netip.MustParsePrefix("100.64.0.0/10"),   // shared address space (CGNAT)
	netip.MustParsePrefix("192.0.0.0/24"),    // IETF protocol assignments
	netip.MustParsePrefix("192.0.2.0/24"),    // documentation
	netip.MustParsePrefix("192.88.99.0/24"),  // deprecated 6to4 relay anycast
	netip.MustParsePrefix("198.18.0.0/15"),   // benchmarking
	netip.MustParsePrefix("198.51.100.0/24"), // documentation
	netip.MustParsePrefix("203.0.113.0/24"),  // documentation
	netip.MustParsePrefix("240.0.0.0/4"),     // reserved, includes broadcast
	netip.MustParsePrefix("2001::/23"),       // IETF protocol assignments, includes Teredo
	netip.MustParsePrefix("2001:db8::/32"),   // documentation
	netip.MustParsePrefix("2002::/16"),       // 6to4, embeds an IPv4 address
	netip.MustParsePrefix("3fff::/20"),       // documentation
}

// blockRestrictedAddress checks the resolved IP before connecting to prevent DNS rebinding.
func blockRestrictedAddress(_, address string, _ syscall.RawConn) error {
	addrPort, err := netip.ParseAddrPort(address)
	if err != nil {
		return fmt.Errorf("dial address %q did not resolve to an IP: %w", address, err)
	}
	ip := addrPort.Addr().WithZone("").Unmap()
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || (ip.Is6() && !globalUnicastIPv6.Contains(ip)) ||
		slices.ContainsFunc(specialPurpose, func(p netip.Prefix) bool { return p.Contains(ip) }) {
		return fmt.Errorf("dial address %s is not public", ip)
	}
	return nil
}
