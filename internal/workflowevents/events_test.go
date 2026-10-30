package workflowevents

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/observability"
)

type memoryStore struct {
	put      []Subscription
	removed  int
	rows     []Pending
	outcomes []string
	active   bool
}

func (m *memoryStore) Put(_ context.Context, s Subscription) error {
	m.put = append(m.put, s)
	return nil
}
func (m *memoryStore) Remove(context.Context, string, string) error            { m.removed++; return nil }
func (m *memoryStore) Pending(context.Context, time.Time) ([]Pending, error)   { return m.rows, nil }
func (m *memoryStore) Active(context.Context, string, time.Time) (bool, error) { return m.active, nil }
func (m *memoryStore) Finish(_ context.Context, _ Pending, state string, _ time.Time) error {
	m.outcomes = append(m.outcomes, state)
	return nil
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func eventInput() Input {
	return Input{Name: Name, Arguments: Arguments{AISessionID: "session", TaskID: "task"}, Delivery: Delivery{Mode: "webhook", URL: "https://callback.example/events", Secret: "whsec_" + base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 32)))}}
}
func TestEventSubscriptionVerifiesSignatureAndStableIdentity(t *testing.T) {
	ctx := context.Background()
	store := &memoryStore{}
	service := New(store, func(context.Context, Arguments) (string, error) { return "project", nil })
	in := eventInput()
	calls := 0
	service.client = &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		calls++
		body, _ := io.ReadAll(r.Body)
		id, stamp := r.Header.Get("webhook-id"), r.Header.Get("webhook-timestamp")
		key, _ := secretKey(in.Delivery.Secret)
		mac := hmac.New(sha256.New, key)
		mac.Write([]byte(id + "." + stamp + "."))
		mac.Write(body)
		if r.Header.Get("webhook-signature") != "v1,"+base64.StdEncoding.EncodeToString(mac.Sum(nil)) {
			t.Fatal("signature does not cover exact request bytes")
		}
		var v map[string]string
		json.Unmarshal(body, &v)
		if v["type"] != "verification" || v["challenge"] == "" {
			t.Fatal("missing challenge")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(body)))}, nil
	})}
	first, e := service.Subscribe(ctx, "scope", in)
	if e != nil {
		t.Fatal(e)
	}
	second, e := service.Subscribe(ctx, "scope", in)
	if e != nil || first.ID != second.ID || calls != 2 || len(store.put) != 2 {
		t.Fatalf("refresh did not preserve identity: %+v %v", second, e)
	}
	if e = service.Unsubscribe(ctx, "scope", in); e != nil || store.removed != 1 {
		t.Fatal(e)
	}
}
func TestEventSubscriptionRejectsUnverifiedAndUnauthorizedCallbacks(t *testing.T) {
	for _, mode := range []string{"wrong-challenge", "redirect", "unauthorized", "bad-secret", "replay"} {
		t.Run(mode, func(t *testing.T) {
			store := &memoryStore{}
			calls := 0
			service := New(store, func(context.Context, Arguments) (string, error) {
				if mode == "unauthorized" {
					return "", errors.New("denied")
				}
				return "project", nil
			})
			service.client = &http.Client{Transport: roundTrip(func(*http.Request) (*http.Response, error) {
				calls++
				code := 200
				if mode == "redirect" {
					code = 302
				}
				return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader(`{"challenge":"wrong"}`)), Header: http.Header{}}, nil
			})}
			in := eventInput()
			if mode == "bad-secret" {
				in.Delivery.Secret = "bad"
			}
			if mode == "replay" {
				v := "old"
				in.Cursor = &v
			}
			if _, e := service.Subscribe(context.Background(), "scope", in); e == nil || len(store.put) != 0 {
				t.Fatal("unverified subscription persisted")
			}
			if (mode == "unauthorized" || mode == "bad-secret" || mode == "replay") && calls != 0 {
				t.Fatal("network used before validation")
			}
		})
	}
}
func TestEventDeliveryOutcomesAndStableEventID(t *testing.T) {
	for _, code := range []int{200, 429, 500, 410, 413, 403} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			in := eventInput()
			row := Pending{Subscription: Subscription{ID: "sub", Delivery: in.Delivery}, EventID: "event-original", Body: json.RawMessage(`{"eventId":"event-original"}`)}
			store := &memoryStore{active: true, rows: []Pending{row}}
			service := New(store, nil)
			calls := 0
			service.client = &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.Header.Get("webhook-id") != "event-original" {
					t.Fatal("event ID changed")
				}
				return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader(""))}, nil
			})}
			service.Dispatch(context.Background())
			want := "RETRY"
			if code == 200 {
				want = "DELIVERED"
			}
			if code == 410 || code == 413 || code == 403 {
				want = "STOPPED"
			}
			if len(store.outcomes) != 1 || store.outcomes[0] != want {
				t.Fatalf("outcomes=%v", store.outcomes)
			}
			store.active = false
			service.Dispatch(context.Background())
			if calls != 1 {
				t.Fatal("inactive subscription delivered")
			}
		})
	}
}
func TestEventCallbackAddressesArePublicOnly(t *testing.T) {
	for _, raw := range []string{"127.0.0.1", "10.0.0.1", "169.254.169.254", "100.64.0.1", "0.0.0.0", "::1", "fe80::1", "fc00::1", "::ffff:127.0.0.1", "64:ff9b::7f00:1", "198.18.0.1", "2001:db8::1"} {
		if publicIP(netip.MustParseAddr(raw)) {
			t.Fatalf("accepted %s", raw)
		}
	}
	if !publicIP(netip.MustParseAddr("8.8.8.8")) {
		t.Fatal("rejected public IP")
	}
	client := publicClient()
	if client.Transport.(*http.Transport).Proxy != nil {
		t.Fatal("callback transport must not use environment proxies")
	}
	if client.CheckRedirect(nil, nil) != http.ErrUseLastResponse {
		t.Fatal("redirects enabled")
	}
}

func TestEventSubscriptionDiagnosticsExcludeCallbackSecrets(t *testing.T) {
	var output bytes.Buffer
	ctx := observability.WithLogger(context.Background(), observability.New(observability.Options{Process: "test", Writer: &output}))
	service := New(&memoryStore{}, func(context.Context, Arguments) (string, error) { return "project", nil })
	in := eventInput()
	in.Delivery.URL += "?token=private-callback-token"
	service.client = &http.Client{Transport: roundTrip(func(*http.Request) (*http.Response, error) {
		return nil, errors.New(in.Delivery.URL + " " + in.Delivery.Secret)
	})}
	if _, err := service.Subscribe(ctx, "scope", in); err == nil {
		t.Fatal("failed callback was accepted")
	}
	log := output.String()
	if !strings.Contains(log, "workflow.subscription.failed") || !strings.Contains(log, "callback_request") {
		t.Fatalf("missing safe failure stage: %s", log)
	}
	for _, private := range []string{in.Delivery.URL, in.Delivery.Secret, "private-callback-token"} {
		if strings.Contains(log, private) {
			t.Fatal("callback credentials leaked into diagnostics")
		}
	}
}

func TestCallbackDiagnosticsClassifyWrappedErrorsWithoutAddresses(t *testing.T) {
	for _, test := range []struct {
		err  error
		want string
	}{{errCallbackDNS, "DNS_UNAVAILABLE"}, {errCallbackNonPublic, "NON_PUBLIC_ADDRESS"}, {context.DeadlineExceeded, "TIMEOUT"}, {context.Canceled, "CANCELED"}} {
		wrapped := &url.Error{Op: "Post", URL: "https://private-callback.example/secret", Err: test.err}
		if got := callbackFailureKind(wrapped); got != test.want {
			t.Fatalf("classification=%q want=%q", got, test.want)
		}
	}
}
