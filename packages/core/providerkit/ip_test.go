package providerkit_test

import (
	"errors"
	"net/netip"
	"testing"

	"github.com/memohai/connect-it/packages/core/providerkit"
)

func TestClassifyIP(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		raw        string
		wantClass  providerkit.IPClass
		wantReason providerkit.IPReason
		wantMapped bool
	}{
		{"public IPv4", "8.8.8.8", providerkit.IPPublic, providerkit.IPReasonPublic, false},
		{"public IPv6", "2606:4700:4700::1111", providerkit.IPPublic, providerkit.IPReasonPublic, false},
		{"unspecified IPv4", "0.0.0.0", providerkit.IPPermanentlyBlocked, providerkit.IPReasonUnspecified, false},
		{"unspecified IPv6", "::", providerkit.IPPermanentlyBlocked, providerkit.IPReasonUnspecified, false},
		{"loopback IPv4", "127.0.0.2", providerkit.IPPermanentlyBlocked, providerkit.IPReasonLoopback, false},
		{"loopback IPv6", "::1", providerkit.IPPermanentlyBlocked, providerkit.IPReasonLoopback, false},
		{"AWS metadata", "169.254.169.254", providerkit.IPPermanentlyBlocked, providerkit.IPReasonMetadata, false},
		{"ECS metadata", "169.254.170.2", providerkit.IPPermanentlyBlocked, providerkit.IPReasonMetadata, false},
		{"Alibaba metadata", "100.100.100.200", providerkit.IPPermanentlyBlocked, providerkit.IPReasonMetadata, false},
		{"AWS IPv6 metadata", "fd00:ec2::254", providerkit.IPPermanentlyBlocked, providerkit.IPReasonMetadata, false},
		{"link local IPv4", "169.254.20.1", providerkit.IPPermanentlyBlocked, providerkit.IPReasonLinkLocal, false},
		{"link local IPv6", "fe80::1", providerkit.IPPermanentlyBlocked, providerkit.IPReasonLinkLocal, false},
		{"multicast IPv4", "224.0.0.1", providerkit.IPPermanentlyBlocked, providerkit.IPReasonMulticast, false},
		{"multicast IPv6", "ff02::1", providerkit.IPPermanentlyBlocked, providerkit.IPReasonMulticast, false},
		{"RFC1918 10", "10.0.0.1", providerkit.IPPrivate, providerkit.IPReasonPrivate, false},
		{"RFC1918 172", "172.31.255.254", providerkit.IPPrivate, providerkit.IPReasonPrivate, false},
		{"RFC1918 192", "192.168.1.1", providerkit.IPPrivate, providerkit.IPReasonPrivate, false},
		{"CGNAT start", "100.64.0.1", providerkit.IPPrivate, providerkit.IPReasonCGNAT, false},
		{"CGNAT end", "100.127.255.254", providerkit.IPPrivate, providerkit.IPReasonCGNAT, false},
		{"IPv6 ULA", "fd12:3456::1", providerkit.IPPrivate, providerkit.IPReasonPrivate, false},
		{"documentation IPv4 A", "192.0.2.1", providerkit.IPPermanentlyBlocked, providerkit.IPReasonDocumentation, false},
		{"documentation IPv4 B", "198.51.100.1", providerkit.IPPermanentlyBlocked, providerkit.IPReasonDocumentation, false},
		{"documentation IPv4 C", "203.0.113.1", providerkit.IPPermanentlyBlocked, providerkit.IPReasonDocumentation, false},
		{"documentation IPv6", "2001:db8::1", providerkit.IPPermanentlyBlocked, providerkit.IPReasonDocumentation, false},
		{"documentation IPv6 new", "3fff::1", providerkit.IPPermanentlyBlocked, providerkit.IPReasonDocumentation, false},
		{"benchmark IPv4", "198.18.0.1", providerkit.IPPermanentlyBlocked, providerkit.IPReasonBenchmark, false},
		{"benchmark IPv6", "2001:2::1", providerkit.IPPermanentlyBlocked, providerkit.IPReasonBenchmark, false},
		{"this network", "0.1.2.3", providerkit.IPPermanentlyBlocked, providerkit.IPReasonReserved, false},
		{"IETF protocol", "192.0.0.1", providerkit.IPPermanentlyBlocked, providerkit.IPReasonReserved, false},
		{"future use", "240.0.0.1", providerkit.IPPermanentlyBlocked, providerkit.IPReasonReserved, false},
		{"IPv6 discard", "100::1", providerkit.IPPermanentlyBlocked, providerkit.IPReasonReserved, false},
		{"IPv6 dummy prefix", "100:0:0:1::1", providerkit.IPPermanentlyBlocked, providerkit.IPReasonReserved, false},
		{"IETF protocol assignments", "2001:5::1", providerkit.IPPermanentlyBlocked, providerkit.IPReasonReserved, false},
		{"IPv6 ORCHID", "2001:20::1", providerkit.IPPermanentlyBlocked, providerkit.IPReasonReserved, false},
		{"IPv6 site local", "fec0::1", providerkit.IPPermanentlyBlocked, providerkit.IPReasonReserved, false},
		{"IPv6 unallocated 4000", "4000::1", providerkit.IPPermanentlyBlocked, providerkit.IPReasonReserved, false},
		{"IPv6 unallocated 6000", "6000::1", providerkit.IPPermanentlyBlocked, providerkit.IPReasonReserved, false},
		{"IPv6 unallocated 8000", "8000::1", providerkit.IPPermanentlyBlocked, providerkit.IPReasonReserved, false},
		{"IPv6 unallocated a000", "a000::1", providerkit.IPPermanentlyBlocked, providerkit.IPReasonReserved, false},
		{"IPv6 unallocated c000", "c000::1", providerkit.IPPermanentlyBlocked, providerkit.IPReasonReserved, false},
		{"IPv6 unallocated e000", "e000::1", providerkit.IPPermanentlyBlocked, providerkit.IPReasonReserved, false},
		{"IPv6 reclaimed 6bone", "3ffe::1", providerkit.IPPermanentlyBlocked, providerkit.IPReasonReserved, false},
		{"NAT64", "64:ff9b::808:808", providerkit.IPPermanentlyBlocked, providerkit.IPReasonTransition, false},
		{"deprecated IPv4 compatible loopback", "::127.0.0.1", providerkit.IPPermanentlyBlocked, providerkit.IPReasonTransition, false},
		{"deprecated IPv4 compatible private", "::192.168.1.1", providerkit.IPPermanentlyBlocked, providerkit.IPReasonTransition, false},
		{"Teredo", "2001::1", providerkit.IPPermanentlyBlocked, providerkit.IPReasonTransition, false},
		{"6to4", "2002:0808:0808::1", providerkit.IPPermanentlyBlocked, providerkit.IPReasonTransition, false},
		{"SRv6 SID special purpose", "5f00::1", providerkit.IPPermanentlyBlocked, providerkit.IPReasonReserved, false},
		{"mapped public", "::ffff:8.8.8.8", providerkit.IPPublic, providerkit.IPReasonPublic, true},
		{"mapped loopback", "::ffff:127.0.0.1", providerkit.IPPermanentlyBlocked, providerkit.IPReasonLoopback, true},
		{"mapped private", "::ffff:10.0.0.1", providerkit.IPPrivate, providerkit.IPReasonPrivate, true},
		{"mapped documentation", "::ffff:192.0.2.1", providerkit.IPPermanentlyBlocked, providerkit.IPReasonDocumentation, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := providerkit.ClassifyIP(netip.MustParseAddr(tt.raw))
			if got.Class != tt.wantClass {
				t.Errorf("ClassifyIP(%s).Class = %v, want %v", tt.raw, got.Class, tt.wantClass)
			}
			if got.Reason != tt.wantReason {
				t.Errorf("ClassifyIP(%s).Reason = %q, want %q", tt.raw, got.Reason, tt.wantReason)
			}
			if got.WasMapped != tt.wantMapped {
				t.Errorf("ClassifyIP(%s).WasMapped = %t, want %t", tt.raw, got.WasMapped, tt.wantMapped)
			}
			if tt.wantMapped && !got.Address.Is4() {
				t.Errorf("ClassifyIP(%s).Address = %s, want unmapped IPv4", tt.raw, got.Address)
			}
		})
	}
}

func TestClassifyInvalidIP(t *testing.T) {
	t.Parallel()

	got := providerkit.ClassifyIP(netip.Addr{})
	if got.Class != providerkit.IPPermanentlyBlocked {
		t.Fatalf("Class = %v, want permanently blocked", got.Class)
	}
	if got.Reason != providerkit.IPReasonInvalid {
		t.Fatalf("Reason = %q, want invalid", got.Reason)
	}
}

func TestValidateResolvedIPSeparatesPrivateAndPermanentBlocks(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		raw          string
		allowPrivate bool
		wantErr      bool
	}{
		{"public public-only", "8.8.8.8", false, false},
		{"public self-hosted", "8.8.8.8", true, false},
		{"private public-only", "10.0.0.1", false, true},
		{"private self-hosted", "10.0.0.1", true, false},
		{"CGNAT public-only", "100.64.0.1", false, true},
		{"CGNAT self-hosted", "100.64.0.1", true, false},
		{"ULA public-only", "fd12::1", false, true},
		{"ULA self-hosted", "fd12::1", true, false},
		{"loopback remains blocked", "127.0.0.1", true, true},
		{"link local remains blocked", "169.254.10.1", true, true},
		{"metadata remains blocked", "100.100.100.200", true, true},
		{"documentation remains blocked", "192.0.2.1", true, true},
		{"reserved remains blocked", "240.0.0.1", true, true},
		{"multicast remains blocked", "224.0.0.1", true, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := providerkit.ValidateResolvedIP(
				netip.MustParseAddr(tt.raw),
				tt.allowPrivate,
			)
			if tt.wantErr {
				if err == nil {
					t.Fatal("ValidateResolvedIP() error = nil")
				}
				var resolvedErr *providerkit.ResolvedIPError
				if !errors.As(err, &resolvedErr) {
					t.Fatalf("error type = %T, want *ResolvedIPError", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("ValidateResolvedIP() error = %v", err)
			}
		})
	}
}

func TestValidateResolvedIPsRejectsAnyUnsafeAnswer(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		addrs        []netip.Addr
		allowPrivate bool
		wantErr      bool
	}{
		{"all public", addrs("8.8.8.8", "1.1.1.1"), false, false},
		{"mixed public private", addrs("8.8.8.8", "10.0.0.1"), false, true},
		{"mixed allowed private", addrs("8.8.8.8", "10.0.0.1"), true, false},
		{"mixed permanent", addrs("8.8.8.8", "127.0.0.1"), true, true},
		{"empty", nil, false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := providerkit.ValidateResolvedIPs(tt.addrs, tt.allowPrivate)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ValidateResolvedIPs() error = %v, wantErr %t", err, tt.wantErr)
			}
		})
	}
}

func TestIPClassString(t *testing.T) {
	t.Parallel()

	tests := []struct {
		class providerkit.IPClass
		want  string
	}{
		{providerkit.IPPublic, "public"},
		{providerkit.IPPrivate, "private"},
		{providerkit.IPPermanentlyBlocked, "permanently_blocked"},
		{providerkit.IPClass(99), "unknown"},
	}
	for _, tt := range tests {
		if got := tt.class.String(); got != tt.want {
			t.Errorf("IPClass(%d).String() = %q, want %q", tt.class, got, tt.want)
		}
	}
}

func addrs(values ...string) []netip.Addr {
	out := make([]netip.Addr, 0, len(values))
	for _, value := range values {
		out = append(out, netip.MustParseAddr(value))
	}
	return out
}
