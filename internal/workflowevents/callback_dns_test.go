package workflowevents

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/netip"
	"strings"
	"testing"
)

func TestCallbackFakeIPResolutionKeepsPrivateAddressesBlocked(t *testing.T) {
	for _, test := range []struct {
		name, host, system, fallback string
		wantFallback, wantError      bool
	}{
		{"ordinary public", "callback.example", "8.8.8.8", "", false, false},
		{"synthetic public", "callback.example", "198.18.0.12", "8.8.8.8", true, false},
		{"private", "callback.example", "10.0.0.1", "8.8.8.8", false, true},
		{"literal synthetic", "198.18.0.12", "198.18.0.12", "8.8.8.8", false, true},
		{"fallback private", "callback.example", "198.18.0.12", "127.0.0.1", true, true},
		{"fallback synthetic", "callback.example", "198.18.0.12", "198.18.0.13", true, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			called := false
			ips, err := callbackIPs(context.Background(), test.host, func(context.Context, string) ([]netip.Addr, error) {
				return []netip.Addr{netip.MustParseAddr(test.system)}, nil
			}, func(context.Context, string) ([]netip.Addr, error) {
				called = true
				return []netip.Addr{netip.MustParseAddr(test.fallback)}, nil
			})
			if (err != nil) != test.wantError || called != test.wantFallback {
				t.Fatalf("ips=%v err=%v fallback=%v", ips, err, called)
			}
			if err == nil && (len(ips) != 1 || !publicIP(ips[0])) {
				t.Fatal("dial target is not a verified public IP")
			}
		})
	}
}

func TestCallbackPublicDNSValidatesBoundedAnswersAndFixedEndpoint(t *testing.T) {
	for _, test := range []struct {
		name, body string
		code       int
		valid      bool
	}{
		{"valid", `{"Status":0,"Question":[{"name":"callback.example.","type":1}],"Answer":[{"type":1,"data":"8.8.8.8"}]}`, 200, true},
		{"wrong question", `{"Status":0,"Question":[{"name":"other.example.","type":1}],"Answer":[{"type":1,"data":"8.8.8.8"}]}`, 200, false},
		{"truncated", `{"Status":0,"TC":true}`, 200, false},
		{"missing status", `{}`, 200, false},
		{"oversized", strings.Repeat(" ", 65537), 200, false},
		{"redirect", `{}`, 302, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
				if r.URL.Scheme != "https" || r.URL.Host != "cloudflare-dns.com" || r.URL.Path != "/dns-query" || r.URL.Query().Get("name") != "callback.example" || r.Header.Get("Accept") != "application/dns-json" || len(r.Header) != 1 {
					t.Fatal("unexpected resolver target or headers")
				}
				return &http.Response{StatusCode: test.code, Body: io.NopCloser(strings.NewReader(test.body))}, nil
			})}
			_, err := callbackPublicDNS(context.Background(), "callback.example", client)
			if (err == nil) != test.valid {
				t.Fatal(err)
			}
		})
	}
	client := callbackDNSClient()
	transport := client.Transport.(*http.Transport)
	if transport.Proxy != nil || transport.TLSClientConfig != nil || client.CheckRedirect(nil, nil) != http.ErrUseLastResponse {
		t.Fatal("resolver proxy, TLS or redirect protections changed")
	}
	if _, err := transport.DialContext(context.Background(), "tcp", "other.example:443"); !errors.Is(err, errCallbackDNS) {
		t.Fatal("resolver dial accepts an unpinned endpoint")
	}
}
