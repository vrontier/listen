package engine

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

// protocolWhitelist is what ffmpeg may open for a stream: HTTP and HTTPS and
// what they are built on. A redirect cannot lead to a file or another
// protocol.
const protocolWhitelist = "http,https,tcp,tls,crypto"

// addressPolicy decides which stream addresses the engine connects to.
type addressPolicy struct {
	allowPrivate bool // intranet installations and tests
	allowFiles   bool // development and tests
	resolve      func(ctx context.Context, host string) ([]netip.Addr, error)
	own          func() []netip.Addr
}

func newAddressPolicy(allowPrivate, allowFiles bool) *addressPolicy {
	return &addressPolicy{
		allowPrivate: allowPrivate, allowFiles: allowFiles,
		resolve: func(ctx context.Context, host string) ([]netip.Addr, error) {
			return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		},
		own: ownAddrs,
	}
}

// errUnresolved marks a host name that could not be resolved: not a reason
// to refuse a running stream, since ffmpeg will fail on it too and retry.
var errUnresolved = errors.New("unresolved")

// check reports why input may not be connected to, or nil. The host's
// addresses are looked up and each must be public.
func (p *addressPolicy) check(ctx context.Context, input string) (*Problem, error) {
	return p.checkWith(ctx, input, true)
}

// checkStatic makes the checks that need no name lookup.
func (p *addressPolicy) checkStatic(input string) *Problem {
	pr, _ := p.checkWith(context.Background(), input, false)
	return pr
}

func (p *addressPolicy) checkWith(ctx context.Context, input string, resolve bool) (*Problem, error) {
	if !strings.Contains(input, "://") {
		if p.allowFiles && input != "" {
			return nil, nil
		}
		return &Problem{Code: "protocol_not_allowed", Message: "only http and https addresses are allowed"}, nil
	}
	u, err := url.Parse(input)
	if err != nil {
		return &Problem{Code: "bad_address", Message: "the address cannot be read"}, nil
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return &Problem{Code: "protocol_not_allowed", Message: "only http and https addresses are allowed"}, nil
	}
	host := u.Hostname()
	if host == "" {
		return &Problem{Code: "bad_address", Message: "the address has no host"}, nil
	}
	if u.User != nil {
		return &Problem{Code: "bad_address", Message: "addresses with a user name or password are not allowed"}, nil
	}
	if p.allowPrivate {
		return nil, nil
	}
	var addrs []netip.Addr
	if a, err := netip.ParseAddr(host); err == nil {
		addrs = []netip.Addr{a}
	} else {
		if strings.HasSuffix(strings.ToLower(host), ".local") || strings.EqualFold(host, "localhost") {
			return notAllowed(), nil
		}
		if !resolve {
			return nil, nil
		}
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		addrs, err = p.resolve(ctx, host)
		if err != nil || len(addrs) == 0 {
			return &Problem{Code: "no_answer", Message: "the host name could not be resolved"}, errUnresolved
		}
	}
	own := p.own()
	for _, a := range addrs {
		if !public(a.Unmap()) {
			return notAllowed(), nil
		}
		for _, o := range own {
			if a.Unmap() == o.Unmap() {
				return notAllowed(), nil
			}
		}
	}
	return nil, nil
}

func notAllowed() *Problem {
	return &Problem{Code: "address_not_allowed", Message: "addresses on loopback, private or link-local networks, or of this server, are not allowed"}
}

// Ranges that are not on the public Internet, beyond what netip names.
var nonPublic = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"), // carrier-grade NAT
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"), // benchmarking
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("64:ff9b::/96"), // NAT64, may reach private IPv4
	netip.MustParsePrefix("2001:db8::/32"),
}

func public(a netip.Addr) bool {
	if !a.IsValid() || a.IsLoopback() || a.IsPrivate() || a.IsLinkLocalUnicast() || a.IsLinkLocalMulticast() ||
		a.IsInterfaceLocalMulticast() || a.IsMulticast() || a.IsUnspecified() {
		return false
	}
	for _, p := range nonPublic {
		if p.Contains(a) {
			return false
		}
	}
	return true
}

func ownAddrs() []netip.Addr {
	ifas, err := net.InterfaceAddrs()
	if err != nil {
		return nil
	}
	var out []netip.Addr
	for _, ia := range ifas {
		if pn, ok := ia.(*net.IPNet); ok {
			if a, ok := netip.AddrFromSlice(pn.IP); ok {
				out = append(out, a)
			}
		}
	}
	return out
}
