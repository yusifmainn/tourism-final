package main

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"strings"
	"sync"
	"time"
)

// netInfo is what is known about the network a visitor connects from.
type netInfo struct {
	Provider string // internet provider (AS name), e.g. "AZERTELECOM-AS"
	ASN      string // "AS28787"
	Country  string // two-letter code of the network's registration
	Hostname string // reverse DNS (PTR) name of the IP address
}

// lookupFunc finds the network details of an IP address. Tests swap it for a stub
// so they never touch the network.
type lookupFunc func(ctx context.Context, ip string) netInfo

const (
	lookupTimeout   = 3 * time.Second
	lookupCacheTTL  = 24 * time.Hour
	lookupRetryTTL  = 15 * time.Minute // after a DNS timeout or server failure
	lookupCacheSize = 10000
	localNetwork    = "Lokal şəbəkə"
)

// dnsResolver is the part of *net.Resolver the lookup uses.
type dnsResolver interface {
	LookupTXT(ctx context.Context, name string) ([]string, error)
	LookupAddr(ctx context.Context, addr string) ([]string, error)
}

// providerLookup asks Team Cymru's free IP-to-ASN DNS service (no API key, plain DNS
// through the system resolver) which network an address belongs to, and caches answers.
// https://www.team-cymru.com/ip-asn-mapping
type providerLookup struct {
	resolver dnsResolver
	mu       sync.Mutex
	cache    map[string]cachedNet
}

type cachedNet struct {
	info    netInfo
	expires time.Time
}

func newProviderLookup(resolver dnsResolver) *providerLookup {
	return &providerLookup{resolver: resolver, cache: map[string]cachedNet{}}
}

// lookup never fails: whatever could not be found is left empty.
func (p *providerLookup) lookup(ctx context.Context, ip string) netInfo {
	addr, err := netip.ParseAddr(ip)
	if err != nil {
		return netInfo{}
	}
	addr = addr.Unmap()
	if isLocalAddr(addr) {
		return netInfo{Provider: localNetwork}
	}
	key := addr.String()
	now := time.Now()
	p.mu.Lock()
	if c, ok := p.cache[key]; ok && now.Before(c.expires) {
		p.mu.Unlock()
		return c.info
	}
	p.mu.Unlock()

	ctx, cancel := context.WithTimeout(ctx, lookupTimeout)
	defer cancel()
	info, complete := p.resolve(ctx, addr)

	ttl := lookupCacheTTL
	if !complete {
		ttl = lookupRetryTTL
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.cache) >= lookupCacheSize {
		for k, c := range p.cache {
			if now.After(c.expires) || len(p.cache) >= lookupCacheSize/2 {
				delete(p.cache, k) // expired entries first, then arbitrary ones down to half
			}
		}
	}
	p.cache[key] = cachedNet{info: info, expires: now.Add(ttl)}
	return info
}

// resolve runs the PTR query alongside the two Cymru TXT queries. complete is false
// when a DNS error other than "no such name" got in the way.
func (p *providerLookup) resolve(ctx context.Context, addr netip.Addr) (info netInfo, complete bool) {
	var hostname string
	var ptrErr error
	done := make(chan struct{})
	go func() {
		defer close(done)
		names, err := p.resolver.LookupAddr(ctx, addr.String())
		if err == nil && len(names) > 0 {
			hostname = strings.TrimSuffix(names[0], ".")
		}
		ptrErr = err
	}()

	complete = true
	txt, err := p.resolver.LookupTXT(ctx, cymruOriginName(addr))
	complete = complete && !transientDNS(err)
	if err == nil && len(txt) > 0 {
		info.ASN, info.Country = parseCymruOrigin(txt[0])
	}
	if info.ASN != "" {
		txt, err := p.resolver.LookupTXT(ctx, info.ASN+".asn.cymru.com")
		complete = complete && !transientDNS(err)
		if err == nil && len(txt) > 0 {
			info.Provider = parseCymruASName(txt[0])
		}
	}
	<-done
	info.Hostname = hostname
	return info, complete && !transientDNS(ptrErr)
}

// transientDNS reports an error worth retrying later (not a definite "no such name").
func transientDNS(err error) bool {
	if err == nil {
		return false
	}
	var dnsErr *net.DNSError
	return !errors.As(err, &dnsErr) || !dnsErr.IsNotFound
}

// isLocalAddr is true for addresses of a private, loopback or link-local network.
func isLocalAddr(a netip.Addr) bool {
	return a.IsPrivate() || a.IsLoopback() || a.IsLinkLocalUnicast() || a.IsUnspecified() ||
		a.IsLinkLocalMulticast() || a.IsMulticast()
}

// cymruOriginName is the TXT query for an address: 8.8.4.4 -> "4.4.8.8.origin.asn.cymru.com";
// IPv6 uses the reversed nibbles under origin6.asn.cymru.com.
func cymruOriginName(a netip.Addr) string {
	a = a.Unmap()
	if a.Is4() {
		b := a.As4()
		return netip.AddrFrom4([4]byte{b[3], b[2], b[1], b[0]}).String() + ".origin.asn.cymru.com"
	}
	const hexDigits = "0123456789abcdef"
	b := a.As16()
	var sb strings.Builder
	for i := len(b) - 1; i >= 0; i-- {
		sb.WriteByte(hexDigits[b[i]&0x0f])
		sb.WriteByte('.')
		sb.WriteByte(hexDigits[b[i]>>4])
		sb.WriteByte('.')
	}
	return sb.String() + "origin6.asn.cymru.com"
}

// cymruFields splits "a | b | c" and trims each field.
func cymruFields(txt string) []string {
	fields := strings.Split(txt, "|")
	for i := range fields {
		fields[i] = strings.TrimSpace(fields[i])
	}
	return fields
}

// parseCymruOrigin reads "15169 | 8.8.8.0/24 | US | arin | 2023-12-28" as ("AS15169", "US").
// When a prefix is announced by several networks the first field lists them all; the first is kept.
func parseCymruOrigin(txt string) (asn, country string) {
	f := cymruFields(txt)
	if len(f) < 3 {
		return "", ""
	}
	first := strings.Fields(f[0])
	if len(first) == 0 || strings.Trim(first[0], "0123456789") != "" {
		return "", ""
	}
	return "AS" + first[0], strings.ToUpper(f[2])
}

// parseCymruASName reads the provider name from "15169 | US | arin | 2000-03-30 | GOOGLE - Google LLC, US"
// (the last field), dropping the repeated country code at its end.
func parseCymruASName(txt string) string {
	f := cymruFields(txt)
	if len(f) < 5 {
		return ""
	}
	name := f[len(f)-1]
	if country := f[1]; country != "" {
		name = strings.TrimSuffix(name, ", "+country)
	}
	return name
}
