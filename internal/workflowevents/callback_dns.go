package workflowevents

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

type callbackLookup func(context.Context, string) ([]netip.Addr, error)

var benchmarkRange = netip.MustParsePrefix("198.18.0.0/15")

// TUN proxies can return benchmark-range synthetic addresses for public DNS.
// Resolve that specific case independently; never dial the synthetic address.
// All final addresses still pass the public-address guard and are pinned by
// the caller. Literal IPs and other private answers never use the fallback.
func callbackIPs(ctx context.Context, host string, system, fallback callbackLookup) ([]netip.Addr, error) {
	ips, err := system(ctx, host)
	if err != nil || len(ips) == 0 {
		return nil, errCallbackDNS
	}
	synthetic := false
	for _, ip := range ips {
		if benchmarkRange.Contains(ip.Unmap()) {
			synthetic = true
		} else if !publicIP(ip) {
			return nil, errCallbackNonPublic
		}
	}
	if synthetic {
		if _, err := netip.ParseAddr(host); err == nil {
			return nil, errCallbackNonPublic
		}
		ips, err = fallback(ctx, host)
		if err != nil || len(ips) == 0 {
			return nil, errCallbackDNS
		}
	}
	for _, ip := range ips {
		if !publicIP(ip) {
			return nil, errCallbackNonPublic
		}
	}
	return ips, nil
}

func callbackDNSClient() *http.Client {
	transport := &http.Transport{TLSHandshakeTimeout: 2 * time.Second, ResponseHeaderTimeout: 2 * time.Second, DisableKeepAlives: true}
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		if address != "cloudflare-dns.com:443" {
			return nil, errCallbackDNS
		}
		// Keep TLS verification against cloudflare-dns.com while avoiding the
		// system resolver for the resolver endpoint itself. No proxy/redirects.
		return (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, network, "1.1.1.1:443")
	}
	return &http.Client{Transport: transport, Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

// Only the hostname goes to the fixed public resolver, never a callback URL,
// signing secret, verification challenge or business data.
func callbackPublicDNS(ctx context.Context, host string, client *http.Client) ([]netip.Addr, error) {
	if len(host) > 253 || strings.ContainsAny(host, "/\\?#@\x00 \t\r\n") {
		return nil, errCallbackDNS
	}
	for _, kind := range []int{1, 28} {
		query := url.Values{"name": {host}, "type": {"A"}}
		if kind == 28 {
			query.Set("type", "AAAA")
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://cloudflare-dns.com/dns-query?"+query.Encode(), nil)
		if err != nil {
			return nil, errCallbackDNS
		}
		req.Header.Set("Accept", "application/dns-json")
		response, err := client.Do(req)
		if err != nil {
			return nil, errCallbackDNS
		}
		body, err := io.ReadAll(io.LimitReader(response.Body, (64<<10)+1))
		response.Body.Close()
		var answer struct {
			Status   *int
			TC       bool
			Question []struct {
				Name string
				Type int
			}
			Answer []struct {
				Type int
				Data string
			}
		}
		if err != nil || len(body) > 64<<10 || response.StatusCode != http.StatusOK || json.Unmarshal(body, &answer) != nil || answer.Status == nil || *answer.Status != 0 || answer.TC || len(answer.Question) != 1 || !strings.EqualFold(strings.TrimSuffix(answer.Question[0].Name, "."), strings.TrimSuffix(host, ".")) || answer.Question[0].Type != kind || len(answer.Answer) > 128 {
			return nil, errCallbackDNS
		}
		ips := []netip.Addr{}
		for _, record := range answer.Answer {
			if record.Type != kind {
				continue
			}
			ip, err := netip.ParseAddr(record.Data)
			if err != nil || kind == 1 && !ip.Is4() || kind == 28 && !ip.Is6() {
				return nil, errCallbackDNS
			}
			ips = append(ips, ip)
		}
		if len(ips) > 0 {
			return ips, nil
		}
	}
	return nil, errCallbackDNS
}
