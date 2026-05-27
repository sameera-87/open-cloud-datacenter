// Package handlers — netvalidate.go
//
// CIDR and network validation helpers shared by all M2 network handlers.
// These are pure functions with no external dependencies — easy to unit-test.
//
// All functions return a human-readable error string on failure; callers convert
// to HTTP 400. The naming matches the spec language so the error messages can be
// read verbatim from the code.
package handlers

import (
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"
)

// ── RFC1918 private ranges ────────────────────────────────────────────────────

var rfc1918Nets = func() []*net.IPNet {
	cidrs := []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16"}
	nets := make([]*net.IPNet, len(cidrs))
	for i, c := range cidrs {
		_, n, _ := net.ParseCIDR(c)
		nets[i] = n
	}
	return nets
}()

// isRFC1918 returns true if the given network is entirely within one of the
// three RFC1918 private ranges (10/8, 172.16/12, 192.168/16).
func isRFC1918(n *net.IPNet) bool {
	for _, priv := range rfc1918Nets {
		if priv.Contains(n.IP) {
			return true
		}
	}
	return false
}

// parseCIDR wraps net.ParseCIDR and returns the network (the host bits stripped).
// Accepts "0.0.0.0/0" as a valid default route.
func parseCIDR(s string) (*net.IPNet, error) {
	_, network, err := net.ParseCIDR(s)
	if err != nil {
		return nil, fmt.Errorf("%q is not a valid CIDR", s)
	}
	return network, nil
}

// validateRFC1918CIDR checks that a CIDR is valid, is RFC1918, is not a host
// route (/32), and falls within the allowed prefix-length range.
// minBits and maxBits are the inclusive min/max prefix lengths allowed.
func validateRFC1918CIDR(cidr string, minBits, maxBits int) error {
	n, err := parseCIDR(cidr)
	if err != nil {
		return err
	}
	ones, _ := n.Mask.Size()
	if ones < minBits || ones > maxBits {
		return fmt.Errorf("CIDR %q prefix length must be between /%d and /%d", cidr, minBits, maxBits)
	}
	if !isRFC1918(n) {
		return fmt.Errorf("CIDR %q is not an RFC1918 private address range (10/8, 172.16/12, 192.168/16)", cidr)
	}
	return nil
}

// ── Reserved CIDR check ───────────────────────────────────────────────────────

// ReservedCIDR is a parsed entry from the regions.reserved_cidrs column.
// The DB stores entries in "cidr:label" format (e.g. "10.42.0.0/16:rke2-pod-cidr").
type ReservedCIDR struct {
	Network *net.IPNet
	Label   string
	Raw     string
}

// ParseReservedCIDRs parses the "cidr:label" entries stored in the DB.
// Entries without a label are treated as having an empty label.
func ParseReservedCIDRs(entries []string) ([]ReservedCIDR, error) {
	result := make([]ReservedCIDR, 0, len(entries))
	for _, e := range entries {
		parts := strings.SplitN(e, ":", 2)
		cidrStr := parts[0]
		label := ""
		if len(parts) == 2 {
			label = parts[1]
		}
		_, n, err := net.ParseCIDR(cidrStr)
		if err != nil {
			return nil, fmt.Errorf("invalid reserved CIDR entry %q: %w", e, err)
		}
		result = append(result, ReservedCIDR{Network: n, Label: label, Raw: cidrStr})
	}
	return result, nil
}

// checkNotReserved returns an error if cidr overlaps any reserved range.
func checkNotReserved(cidr string, reserved []ReservedCIDR) error {
	n, err := parseCIDR(cidr)
	if err != nil {
		return err
	}
	for _, r := range reserved {
		if r.Network.Contains(n.IP) || n.Contains(r.Network.IP) {
			msg := fmt.Sprintf("CIDR %s overlaps reserved range %s", cidr, r.Raw)
			if r.Label != "" {
				msg += fmt.Sprintf(" (%s)", r.Label)
			}
			return fmt.Errorf("%s", msg)
		}
	}
	return nil
}

// ── Subnet containment ────────────────────────────────────────────────────────

// cidrContainedInAny returns nil if cidr is a subset of at least one entry in
// parentCIDRs (i.e. the subnet falls within the VNet's address space).
func cidrContainedInAny(cidr string, parentCIDRs []string) error {
	_, sub, err := net.ParseCIDR(cidr)
	if err != nil {
		return fmt.Errorf("%q is not a valid CIDR", cidr)
	}
	for _, p := range parentCIDRs {
		_, parent, _ := net.ParseCIDR(p)
		if parent == nil {
			continue
		}
		// sub is contained in parent when parent.Contains(sub's first IP) AND
		// parent.Contains(sub's broadcast address.
		if parent.Contains(sub.IP) && parent.Contains(broadcastIP(sub)) {
			return nil
		}
	}
	return fmt.Errorf("CIDR %s is not contained in VNet address space %s", cidr, strings.Join(parentCIDRs, ", "))
}

// broadcastIP returns the last address of a network (used for containment check).
func broadcastIP(n *net.IPNet) net.IP {
	ip := make(net.IP, len(n.IP))
	copy(ip, n.IP)
	for i := range ip {
		ip[i] |= ^n.Mask[i]
	}
	return ip
}

// cidrsOverlap returns true if two CIDR strings overlap.
func cidrsOverlap(a, b string) bool {
	_, na, err1 := net.ParseCIDR(a)
	_, nb, err2 := net.ParseCIDR(b)
	if err1 != nil || err2 != nil {
		return false
	}
	return na.Contains(nb.IP) || nb.Contains(na.IP)
}

// checkNoSiblingOverlap returns an error if cidr overlaps any existing sibling
// subnet CIDR in the same VNet. existingCIDRs come from the DB.
func checkNoSiblingOverlap(cidr string, existingCIDRs []string) error {
	for _, ex := range existingCIDRs {
		if cidrsOverlap(cidr, ex) {
			return fmt.Errorf("CIDR %s overlaps existing subnet %s in the same VNet", cidr, ex)
		}
	}
	return nil
}

// gatewayInCIDR checks that a gateway IP is within the given CIDR and is
// not the network address or broadcast address.
func gatewayInCIDR(gwStr, cidr string) error {
	gw := net.ParseIP(gwStr)
	if gw == nil {
		return fmt.Errorf("%q is not a valid IP address", gwStr)
	}
	_, n, err := net.ParseCIDR(cidr)
	if err != nil {
		return fmt.Errorf("invalid CIDR %q", cidr)
	}
	if !n.Contains(gw) {
		return fmt.Errorf("gateway %s is not within CIDR %s", gwStr, cidr)
	}
	// Reject network address
	if gw.Equal(n.IP) {
		return fmt.Errorf("gateway %s is the network address of %s — use a host address", gwStr, cidr)
	}
	// Reject broadcast
	if gw.Equal(broadcastIP(n)) {
		return fmt.Errorf("gateway %s is the broadcast address of %s — use a host address", gwStr, cidr)
	}
	return nil
}

// defaultGateway computes the first usable host IP from a CIDR string.
// e.g. "10.1.1.0/24" → "10.1.1.1"
func defaultGateway(cidr string) string {
	_, n, err := net.ParseCIDR(cidr)
	if err != nil {
		return ""
	}
	ip := make(net.IP, len(n.IP))
	copy(ip, n.IP)
	ip[len(ip)-1]++ // increment last octet → first host address
	return ip.String()
}

// ipInAnySubnet returns true if ip (string) falls within any of the given CIDR
// strings. Used to validate next_hop_ip for virtual_appliance routes.
func ipInAnySubnet(ipStr string, cidrList []string) bool {
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return false
	}
	for _, c := range cidrList {
		_, n, err := net.ParseCIDR(c)
		if err != nil {
			continue
		}
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// ── Route rule validation ─────────────────────────────────────────────────────

var validNextHopTypes = map[string]bool{
	"vnet_local":         true,
	"internet":           true,
	"virtual_appliance":  true,
	"none":               true,
}

// validateRouteRule checks a single RouteRule. subnetCIDRs is used to validate
// next_hop_ip when next_hop_type = "virtual_appliance".
func validateRouteRule(r routeRuleDTO, subnetCIDRs []string) error {
	if r.Name == "" {
		return fmt.Errorf("route name is required")
	}
	if strings.Contains(r.Name, "/") {
		return fmt.Errorf("route name must not contain '/'")
	}
	if r.DestinationCIDR == "" {
		return fmt.Errorf("route %q: destination_cidr is required", r.Name)
	}
	// Allow default route "0.0.0.0/0" as a special case.
	if r.DestinationCIDR != "0.0.0.0/0" {
		if _, err := parseCIDR(r.DestinationCIDR); err != nil {
			return fmt.Errorf("route %q: %w", r.Name, err)
		}
	}
	if !validNextHopTypes[r.NextHopType] {
		return fmt.Errorf("route %q: next_hop_type must be one of: vnet_local, internet, virtual_appliance, none", r.Name)
	}
	if r.NextHopType == "virtual_appliance" {
		if r.NextHopIP == "" {
			return fmt.Errorf("route %q: next_hop_ip is required when next_hop_type is virtual_appliance", r.Name)
		}
		if net.ParseIP(r.NextHopIP) == nil {
			return fmt.Errorf("route %q: next_hop_ip %q is not a valid IP", r.Name, r.NextHopIP)
		}
		if len(subnetCIDRs) > 0 && !ipInAnySubnet(r.NextHopIP, subnetCIDRs) {
			return fmt.Errorf("route %q: next_hop_ip %s must be within a subnet of the VNet", r.Name, r.NextHopIP)
		}
	}
	return nil
}

// ── NSG rule validation ───────────────────────────────────────────────────────

var validProtocols = map[string]bool{
	"tcp": true, "udp": true, "icmp": true, "*": true,
}
var validActions = map[string]bool{
	"allow": true, "deny": true,
}
var validDirections = map[string]bool{
	"inbound": true, "outbound": true,
}

// validateNSGRule validates a single NSG rule struct.
func validateNSGRule(rule nsgRuleDTO) error {
	if rule.Name == "" {
		return fmt.Errorf("rule name is required")
	}
	if strings.Contains(rule.Name, "/") {
		return fmt.Errorf("rule name must not contain '/' (used as ACL tag separator)")
	}
	if !validDirections[rule.Direction] {
		return fmt.Errorf("rule %q: direction must be 'inbound' or 'outbound'", rule.Name)
	}
	if rule.Priority < 100 || rule.Priority > 4096 {
		return fmt.Errorf("rule %q: priority must be between 100 and 4096", rule.Name)
	}
	if !validProtocols[rule.Protocol] {
		return fmt.Errorf("rule %q: protocol must be tcp, udp, icmp, or *", rule.Name)
	}
	if rule.SourceAddressPrefix == "" {
		return fmt.Errorf("rule %q: source_address_prefix is required", rule.Name)
	}
	if rule.DestinationAddressPrefix == "" {
		return fmt.Errorf("rule %q: destination_address_prefix is required", rule.Name)
	}
	if !validActions[rule.Action] {
		return fmt.Errorf("rule %q: action must be 'allow' or 'deny'", rule.Name)
	}
	if err := validatePortRange(rule.Name, "source_port_range", rule.SourcePortRange); err != nil {
		return err
	}
	if err := validatePortRange(rule.Name, "destination_port_range", rule.DestinationPortRange); err != nil {
		return err
	}
	return nil
}

// validatePortRange accepts port, "*", or "low-high" format.
func validatePortRange(ruleName, field, portRange string) error {
	if portRange == "" {
		return fmt.Errorf("rule %q: %s is required", ruleName, field)
	}
	if portRange == "*" {
		return nil
	}
	if strings.Contains(portRange, "-") {
		parts := strings.SplitN(portRange, "-", 2)
		lo, err1 := strconv.Atoi(parts[0])
		hi, err2 := strconv.Atoi(parts[1])
		if err1 != nil || err2 != nil || lo < 0 || hi > 65535 || lo > hi {
			return fmt.Errorf("rule %q: %s %q is not a valid port range (use low-high, e.g. 1024-2048)", ruleName, field, portRange)
		}
		return nil
	}
	p, err := strconv.Atoi(portRange)
	if err != nil || p < 0 || p > 65535 {
		return fmt.Errorf("rule %q: %s %q is not a valid port number or range", ruleName, field, portRange)
	}
	return nil
}

// checkNSGRulePriorityUnique checks that priorities are unique within each
// direction across the full rule set being installed.
func checkNSGRulePriorityUnique(rules []nsgRuleDTO) error {
	type dirPri struct{ dir string; pri int }
	seen := make(map[dirPri]string) // value = rule name that claimed it
	for _, r := range rules {
		key := dirPri{r.Direction, r.Priority}
		if existing, dup := seen[key]; dup {
			return fmt.Errorf(
				"priority %d in direction %q is used by both rule %q and rule %q — priorities must be unique within a direction",
				r.Priority, r.Direction, existing, r.Name,
			)
		}
		seen[key] = r.Name
	}
	return nil
}

// ── Resource name validation ─────────────────────────────────────────────────

// MaxResourceNameLen caps every user-supplied resource name at 30 chars. The
// downstream limit that actually matters is Kubernetes' 63-char label-value
// cap: dc-api derives the kube-ovn VPC name as `vnet-<tenant>-<vnet-name>`,
// kube-ovn then derives a NAT-gateway StatefulSet name from that, and K8s
// finally appends a `controller-revision-hash` label of the form
// "<SS-name>-<11-char-hash>" which must fit in 63 chars.
//
// Keeping user names at ≤ 30 chars leaves comfortable headroom for the
// internal hashing fallbacks (subnetResourceName, natGWName, etc.) without
// relying on them. If we ever loosen this, every downstream-name helper needs
// to be re-audited.
const MaxResourceNameLen = 30

var resourceNameRE = regexp.MustCompile(`^[a-z][a-z0-9-]{0,29}$`)

// validateResourceName checks that a network resource name matches
// [a-z][a-z0-9-]*, length 1-30, starts with a letter.
//
// Tightened from 63 → 30 to enforce the headroom the kube-ovn provider needs
// for its derived label values (see MaxResourceNameLen doc).
func validateResourceName(name string) error {
	if !resourceNameRE.MatchString(name) {
		return fmt.Errorf(
			"name %q is invalid — must be 1-%d characters, lowercase alphanumeric and hyphens only, must start with a letter",
			name, MaxResourceNameLen)
	}
	return nil
}

// ── DNS zone name validation ──────────────────────────────────────────────────

// DNS label max 63 chars; full name max 253.
var dnsLabelRE = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9\-]{0,61}[a-zA-Z0-9])?$`)

// validateDNSZoneName checks that a DNS zone name is valid (RFC 1123).
func validateDNSZoneName(name string) error {
	if len(name) == 0 || len(name) > 253 {
		return fmt.Errorf("DNS zone name must be 1-253 characters")
	}
	if strings.HasPrefix(name, ".") {
		return fmt.Errorf("DNS zone name must not start with a dot")
	}
	labels := strings.Split(strings.TrimSuffix(name, "."), ".")
	for _, label := range labels {
		if label == "" {
			return fmt.Errorf("DNS zone name contains an empty label (double dot?)")
		}
		if !dnsLabelRE.MatchString(label) {
			return fmt.Errorf("DNS label %q is invalid — must be alphanumeric and hyphens, 1-63 chars", label)
		}
	}
	return nil
}

// ── DNS record validation ─────────────────────────────────────────────────────

var validRecordTypes = map[string]bool{
	"A": true, "AAAA": true, "CNAME": true, "TXT": true, "SRV": true, "MX": true,
}

// validateDNSRecord validates the create/update request for a DNS record.
func validateDNSRecord(recType, name string, values []string, ttl int) error {
	if !validRecordTypes[recType] {
		return fmt.Errorf("type %q is not supported; allowed: A, AAAA, CNAME, TXT, SRV, MX", recType)
	}
	if name == "" {
		return fmt.Errorf("record name is required")
	}
	if len(values) == 0 {
		return fmt.Errorf("at least one value is required")
	}
	if ttl != 0 && (ttl < 30 || ttl > 86400) {
		return fmt.Errorf("ttl must be between 30 and 86400 seconds")
	}
	switch recType {
	case "CNAME":
		if len(values) != 1 {
			return fmt.Errorf("CNAME records must have exactly one value")
		}
	case "A":
		for _, v := range values {
			if ip := net.ParseIP(v); ip == nil || ip.To4() == nil {
				return fmt.Errorf("A record value %q is not a valid IPv4 address", v)
			}
		}
	case "AAAA":
		for _, v := range values {
			if ip := net.ParseIP(v); ip == nil || ip.To4() != nil {
				return fmt.Errorf("AAAA record value %q is not a valid IPv6 address", v)
			}
		}
	case "SRV":
		// SRV format: "priority weight port target"
		for _, v := range values {
			parts := strings.Fields(v)
			if len(parts) != 4 {
				return fmt.Errorf("SRV value %q must be in 'priority weight port target' format", v)
			}
		}
	}
	return nil
}
