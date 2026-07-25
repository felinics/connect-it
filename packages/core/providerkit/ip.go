package providerkit

import (
	"fmt"
	"net/netip"
)

// IPClass describes whether a resolved address may be used for Provider
// egress. Private addresses require an explicit self-hosted deployment opt-in;
// permanently blocked addresses are never allowed.
type IPClass uint8

const (
	IPPublic IPClass = iota
	IPPrivate
	IPPermanentlyBlocked
)

func (c IPClass) String() string {
	switch c {
	case IPPublic:
		return "public"
	case IPPrivate:
		return "private"
	case IPPermanentlyBlocked:
		return "permanently_blocked"
	default:
		return "unknown"
	}
}

// IPReason is a stable, non-secret explanation for an IP classification.
type IPReason string

const (
	IPReasonPublic        IPReason = "public"
	IPReasonInvalid       IPReason = "invalid"
	IPReasonUnspecified   IPReason = "unspecified"
	IPReasonLoopback      IPReason = "loopback"
	IPReasonLinkLocal     IPReason = "link_local"
	IPReasonMulticast     IPReason = "multicast"
	IPReasonMetadata      IPReason = "cloud_metadata"
	IPReasonPrivate       IPReason = "private"
	IPReasonCGNAT         IPReason = "cgnat"
	IPReasonDocumentation IPReason = "documentation"
	IPReasonBenchmark     IPReason = "benchmark"
	IPReasonReserved      IPReason = "reserved"
	IPReasonTransition    IPReason = "transition"
)

// IPClassification is the result of classifying a resolved address.
//
// Address is unmapped, so IPv4-mapped IPv6 values cannot bypass IPv4 policy.
// WasMapped records that normalization for diagnostics and tests.
type IPClassification struct {
	Address   netip.Addr
	Class     IPClass
	Reason    IPReason
	WasMapped bool
}

// ResolvedIPError reports a rejected resolved address.
type ResolvedIPError struct {
	Address netip.Addr
	Class   IPClass
	Reason  IPReason
}

func (e *ResolvedIPError) Error() string {
	if e == nil {
		return "resolved IP is not allowed"
	}
	if !e.Address.IsValid() {
		return fmt.Sprintf("resolved IP is not allowed: %s", e.Reason)
	}
	return fmt.Sprintf(
		"resolved IP %s is not allowed: %s",
		e.Address,
		e.Reason,
	)
}

var (
	ipv4CGNAT = netip.MustParsePrefix("100.64.0.0/10")
	// IANA's IPv6 Global Unicast Address Space registry says unlisted space in
	// 2000::/3 is reserved. netip.IsGlobalUnicast is intentionally broader,
	// so PublicOnly uses this reviewed allocation allowlist (registry updated
	// 2025-10-10) and fails closed when IANA allocates a new block.
	//
	// https://www.iana.org/assignments/ipv6-unicast-address-assignments/
	ipv6AllocatedGlobalUnicastPrefixes = []netip.Prefix{
		netip.MustParsePrefix("2001:200::/23"),
		netip.MustParsePrefix("2001:400::/23"),
		netip.MustParsePrefix("2001:600::/23"),
		netip.MustParsePrefix("2001:800::/22"),
		netip.MustParsePrefix("2001:c00::/23"),
		netip.MustParsePrefix("2001:e00::/23"),
		netip.MustParsePrefix("2001:1200::/23"),
		netip.MustParsePrefix("2001:1400::/22"),
		netip.MustParsePrefix("2001:1800::/23"),
		netip.MustParsePrefix("2001:1a00::/23"),
		netip.MustParsePrefix("2001:1c00::/22"),
		netip.MustParsePrefix("2001:2000::/19"),
		netip.MustParsePrefix("2001:4000::/23"),
		netip.MustParsePrefix("2001:4200::/23"),
		netip.MustParsePrefix("2001:4400::/23"),
		netip.MustParsePrefix("2001:4600::/23"),
		netip.MustParsePrefix("2001:4800::/23"),
		netip.MustParsePrefix("2001:4a00::/23"),
		netip.MustParsePrefix("2001:4c00::/23"),
		netip.MustParsePrefix("2001:5000::/20"),
		netip.MustParsePrefix("2001:8000::/19"),
		netip.MustParsePrefix("2001:a000::/20"),
		netip.MustParsePrefix("2001:b000::/20"),
		netip.MustParsePrefix("2003::/18"),
		netip.MustParsePrefix("2400::/12"),
		netip.MustParsePrefix("2410::/12"),
		netip.MustParsePrefix("2600::/12"),
		netip.MustParsePrefix("2610::/23"),
		netip.MustParsePrefix("2620::/23"),
		netip.MustParsePrefix("2630::/12"),
		netip.MustParsePrefix("2800::/12"),
		netip.MustParsePrefix("2a00::/12"),
		netip.MustParsePrefix("2a10::/12"),
		netip.MustParsePrefix("2c00::/12"),
	}

	ipv4MetadataPrefixes = []netip.Prefix{
		netip.MustParsePrefix("100.100.100.200/32"),
		netip.MustParsePrefix("169.254.169.254/32"),
		netip.MustParsePrefix("169.254.170.2/32"),
	}
	ipv4DocumentationPrefixes = []netip.Prefix{
		netip.MustParsePrefix("192.0.2.0/24"),
		netip.MustParsePrefix("198.51.100.0/24"),
		netip.MustParsePrefix("203.0.113.0/24"),
	}
	ipv4BenchmarkPrefixes = []netip.Prefix{
		netip.MustParsePrefix("198.18.0.0/15"),
	}
	ipv4ReservedPrefixes = []netip.Prefix{
		netip.MustParsePrefix("0.0.0.0/8"),
		netip.MustParsePrefix("192.0.0.0/24"),
		netip.MustParsePrefix("192.88.99.0/24"),
		netip.MustParsePrefix("240.0.0.0/4"),
	}

	ipv6DocumentationPrefixes = []netip.Prefix{
		netip.MustParsePrefix("2001:db8::/32"),
		netip.MustParsePrefix("3fff::/20"),
	}
	ipv6MetadataPrefixes = []netip.Prefix{
		netip.MustParsePrefix("fd00:ec2::254/128"),
	}
	ipv6BenchmarkPrefixes = []netip.Prefix{
		netip.MustParsePrefix("2001:2::/48"),
	}
	ipv6TransitionPrefixes = []netip.Prefix{
		// IPv4-embedded transition mechanisms can otherwise hide an IPv4
		// destination from this policy layer.
		netip.MustParsePrefix("::/96"),
		netip.MustParsePrefix("64:ff9b::/96"),
		netip.MustParsePrefix("64:ff9b:1::/48"),
		netip.MustParsePrefix("2001::/32"),
		netip.MustParsePrefix("2002::/16"),
	}
	ipv6ReservedPrefixes = []netip.Prefix{
		netip.MustParsePrefix("100::/64"),
		netip.MustParsePrefix("100:0:0:1::/64"),
		netip.MustParsePrefix("5f00::/16"),
		netip.MustParsePrefix("2001::/23"),
		netip.MustParsePrefix("2001:10::/28"),
		netip.MustParsePrefix("2001:20::/28"),
		netip.MustParsePrefix("fec0::/10"),
	}
)

// ClassifyIP classifies a single resolved address. IPv4-mapped IPv6 addresses
// are first unmapped and then evaluated using all IPv4 rules.
func ClassifyIP(addr netip.Addr) IPClassification {
	if !addr.IsValid() {
		return IPClassification{
			Class:  IPPermanentlyBlocked,
			Reason: IPReasonInvalid,
		}
	}

	wasMapped := addr.Is4In6()
	addr = addr.Unmap()
	result := IPClassification{Address: addr, WasMapped: wasMapped}

	switch {
	case addr.IsUnspecified():
		result.Class = IPPermanentlyBlocked
		result.Reason = IPReasonUnspecified
	case addr.IsLoopback():
		result.Class = IPPermanentlyBlocked
		result.Reason = IPReasonLoopback
	case containsAddr(ipv4MetadataPrefixes, addr):
		result.Class = IPPermanentlyBlocked
		result.Reason = IPReasonMetadata
	case containsAddr(ipv6MetadataPrefixes, addr):
		result.Class = IPPermanentlyBlocked
		result.Reason = IPReasonMetadata
	case addr.IsMulticast():
		result.Class = IPPermanentlyBlocked
		result.Reason = IPReasonMulticast
	case addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast():
		result.Class = IPPermanentlyBlocked
		result.Reason = IPReasonLinkLocal
	case addr.Is4() && addr.IsPrivate():
		result.Class = IPPrivate
		result.Reason = IPReasonPrivate
	case addr.Is4() && ipv4CGNAT.Contains(addr):
		result.Class = IPPrivate
		result.Reason = IPReasonCGNAT
	case addr.Is6() && addr.IsPrivate():
		result.Class = IPPrivate
		result.Reason = IPReasonPrivate
	case containsAddr(ipv4DocumentationPrefixes, addr) ||
		containsAddr(ipv6DocumentationPrefixes, addr):
		result.Class = IPPermanentlyBlocked
		result.Reason = IPReasonDocumentation
	case containsAddr(ipv4BenchmarkPrefixes, addr) ||
		containsAddr(ipv6BenchmarkPrefixes, addr):
		result.Class = IPPermanentlyBlocked
		result.Reason = IPReasonBenchmark
	case containsAddr(ipv6TransitionPrefixes, addr):
		result.Class = IPPermanentlyBlocked
		result.Reason = IPReasonTransition
	case containsAddr(ipv4ReservedPrefixes, addr) ||
		containsAddr(ipv6ReservedPrefixes, addr) ||
		(addr.Is6() &&
			!containsAddr(ipv6AllocatedGlobalUnicastPrefixes, addr)) ||
		!addr.IsGlobalUnicast():
		result.Class = IPPermanentlyBlocked
		result.Reason = IPReasonReserved
	default:
		result.Class = IPPublic
		result.Reason = IPReasonPublic
	}
	return result
}

// ValidateResolvedIP rejects permanently blocked addresses and rejects private
// addresses unless allowPrivate is true. The caller must derive allowPrivate
// from SelfHostedOptIn plus the administrator-controlled deployment switch.
func ValidateResolvedIP(addr netip.Addr, allowPrivate bool) error {
	classification := ClassifyIP(addr)
	switch classification.Class {
	case IPPublic:
		return nil
	case IPPrivate:
		if allowPrivate {
			return nil
		}
	}
	return &ResolvedIPError{
		Address: classification.Address,
		Class:   classification.Class,
		Reason:  classification.Reason,
	}
}

// ValidateResolvedIPs validates every address returned for a hostname. An
// unsafe address rejects the complete resolution result; callers must not pick
// only a convenient safe address from a mixed DNS answer.
func ValidateResolvedIPs(addrs []netip.Addr, allowPrivate bool) error {
	if len(addrs) == 0 {
		return &ResolvedIPError{
			Class:  IPPermanentlyBlocked,
			Reason: IPReasonInvalid,
		}
	}
	for _, addr := range addrs {
		if err := ValidateResolvedIP(addr, allowPrivate); err != nil {
			return err
		}
	}
	return nil
}

func containsAddr(prefixes []netip.Prefix, addr netip.Addr) bool {
	for _, prefix := range prefixes {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}
