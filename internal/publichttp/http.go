package publichttp

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"time"
)

var blockedNetworks = func() []netip.Prefix {
	var blocks []netip.Prefix
	for _, network := range []string{"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16", "172.16.0.0/12", "192.0.0.0/24", "192.0.2.0/24", "192.88.99.0/24", "192.168.0.0/16", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "224.0.0.0/4", "240.0.0.0/4", "2001::/23", "2001:db8::/32", "2002::/16", "3fff::/20"} {
		blocks = append(blocks, netip.MustParsePrefix(network))
	}
	return blocks
}()

func PublicAddress(address netip.Addr) bool {
	address = address.Unmap()
	if !address.IsGlobalUnicast() || address.IsPrivate() || address.Zone() != "" {
		return false
	}
	if address.Is6() && !netip.MustParsePrefix("2000::/3").Contains(address) {
		return false
	}
	for _, block := range blockedNetworks {
		if block.Contains(address) {
			return false
		}
	}
	return true
}

// DialContext resolves and pins public IPs at connection time, including redirected requests.
func DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, err
	}
	if len(ips) == 0 {
		return nil, errors.New("no image address")
	}
	for _, ip := range ips {
		if !PublicAddress(ip) {
			return nil, errors.New("private image address")
		}
	}
	var failures []error
	for _, ip := range ips {
		c, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		if err == nil {
			return c, nil
		}
		failures = append(failures, err)
	}
	return nil, errors.Join(failures...)
}
