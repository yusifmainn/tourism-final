package main

import (
	"context"
	"net"
	"net/netip"
	"sync/atomic"
	"testing"
)

func TestCymruQueryNames(t *testing.T) {
	cases := map[string]string{
		"8.8.4.4":              "4.4.8.8.origin.asn.cymru.com",
		"85.132.1.20":          "20.1.132.85.origin.asn.cymru.com",
		"::ffff:85.132.1.20":   "20.1.132.85.origin.asn.cymru.com", // IPv4-mapped IPv6
		"2001:4860:4860::8888": "8.8.8.8.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.6.8.4.0.6.8.4.1.0.0.2.origin6.asn.cymru.com",
		"2a02:4780:a::1":       "1.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.a.0.0.0.0.8.7.4.2.0.a.2.origin6.asn.cymru.com",
	}
	for ip, want := range cases {
		if got := cymruOriginName(netip.MustParseAddr(ip)); got != want {
			t.Errorf("%s:\n got %s\nwant %s", ip, got, want)
		}
	}
}

func TestParseCymru(t *testing.T) {
	origins := []struct{ txt, asn, country string }{
		{"15169 | 8.8.8.0/24 | US | arin | 2023-12-28", "AS15169", "US"},
		{"28787 | 85.132.0.0/17 | AZ | ripencc | 2005-04-20", "AS28787", "AZ"},
		{"23028 15169 | 8.8.4.0/24 | us | arin | 2023-12-28", "AS23028", "US"}, // several origins: first one
		{"garbage", "", ""},
		{"NA | 1.2.3.0/24 | AZ | ripencc |", "", ""},
	}
	for _, c := range origins {
		asn, country := parseCymruOrigin(c.txt)
		if asn != c.asn || country != c.country {
			t.Errorf("parseCymruOrigin(%q) = %q, %q; want %q, %q", c.txt, asn, country, c.asn, c.country)
		}
	}
	names := map[string]string{
		"15169 | US | arin | 2000-03-30 | GOOGLE - Google LLC, US":    "GOOGLE - Google LLC",
		"28787 | AZ | ripencc | 2003-08-21 | BAKINTERNET-AS, AZ":      "BAKINTERNET-AS",
		"12345 | AZ | ripencc | 2003-08-21 | Example | With Pipe, AZ": "With Pipe",
		"too | short": "",
	}
	for txt, want := range names {
		if got := parseCymruASName(txt); got != want {
			t.Errorf("parseCymruASName(%q) = %q, want %q", txt, got, want)
		}
	}
}

// fakeResolver answers like Team Cymru and counts the queries.
type fakeResolver struct {
	txt     map[string][]string
	ptr     map[string][]string
	queries atomic.Int32
}

func (f *fakeResolver) LookupTXT(_ context.Context, name string) ([]string, error) {
	f.queries.Add(1)
	if v, ok := f.txt[name]; ok {
		return v, nil
	}
	return nil, &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
}

func (f *fakeResolver) LookupAddr(_ context.Context, addr string) ([]string, error) {
	f.queries.Add(1)
	if v, ok := f.ptr[addr]; ok {
		return v, nil
	}
	return nil, &net.DNSError{Err: "no such host", Name: addr, IsNotFound: true}
}

func TestProviderLookup(t *testing.T) {
	r := &fakeResolver{
		txt: map[string][]string{
			"20.1.132.85.origin.asn.cymru.com": {"28787 | 85.132.0.0/17 | AZ | ripencc | 2005-04-20"},
			"AS28787.asn.cymru.com":            {"28787 | AZ | ripencc | 2003-08-21 | BAKINTERNET-AS, AZ"},
		},
		ptr: map[string][]string{"85.132.1.20": {"host-20.example.az."}},
	}
	p := newProviderLookup(r)
	ctx := context.Background()

	want := netInfo{Provider: "BAKINTERNET-AS", ASN: "AS28787", Country: "AZ", Hostname: "host-20.example.az"}
	if got := p.lookup(ctx, "85.132.1.20"); got != want {
		t.Errorf("lookup = %+v, want %+v", got, want)
	}
	queries := r.queries.Load()
	if got := p.lookup(ctx, "85.132.1.20"); got != want {
		t.Errorf("cached lookup = %+v", got)
	}
	if r.queries.Load() != queries {
		t.Error("second lookup of the same IP went to DNS instead of the cache")
	}

	for _, ip := range []string{"127.0.0.1", "192.168.1.10", "10.0.0.5", "::1", "fd00::1"} {
		if got := p.lookup(ctx, ip); got != (netInfo{Provider: localNetwork}) {
			t.Errorf("%s: %+v, want the local network label", ip, got)
		}
	}
	if got := p.lookup(ctx, "not an ip"); got != (netInfo{}) {
		t.Errorf("bad IP: %+v", got)
	}
	if got := p.lookup(ctx, "203.0.113.9"); got != (netInfo{}) { // unknown everywhere
		t.Errorf("unknown IP: %+v", got)
	}
}
