// Package testkit contains deterministic helpers for Provider adapter tests.
// It must only be imported by *_test.go files.
package testkit

import (
	"context"
	"net"
	"net/netip"

	"github.com/memohai/connect-it/packages/core/providerkit"
)

var publicTestAddress = netip.MustParseAddr("93.184.216.34")

// Resolver maps every requested test hostname to the supplied address. The
// production transport still performs its normal DNS pinning.
type Resolver struct {
	Addresses []netip.Addr
	Err       error
	Calls     func(network, host string)
}

func (resolver Resolver) LookupNetIP(
	_ context.Context,
	network string,
	host string,
) ([]netip.Addr, error) {
	if resolver.Calls != nil {
		resolver.Calls(network, host)
	}
	if resolver.Err != nil {
		return nil, resolver.Err
	}
	addresses := resolver.Addresses
	if len(addresses) == 0 {
		addresses = []netip.Addr{publicTestAddress}
	}
	return append([]netip.Addr(nil), addresses...), nil
}

// DialTarget returns a dialer that always connects to target. The guarded
// transport still passes a literal, previously validated IP to this function;
// redirecting it to an httptest listener is confined to test code.
func DialTarget(target string) providerkit.DialContextFunc {
	dialer := &net.Dialer{}
	return func(
		ctx context.Context,
		network string,
		_ string,
	) (net.Conn, error) {
		return dialer.DialContext(ctx, network, target)
	}
}
