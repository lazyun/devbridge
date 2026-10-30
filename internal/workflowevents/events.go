// Package workflowevents delivers scoped observations; it never advances work.
package workflowevents

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/lazyun/devbridge/internal/observability"
)

const Name = "task.updated"

var ErrInvalid = errors.New("invalid workflow event subscription")

var (
	errCallbackDNS       = errors.New("callback DNS unavailable")
	errCallbackNonPublic = errors.New("callback address is not public")
)

type Arguments struct {
	AISessionID string `json:"aiSessionId"`
	TaskID      string `json:"taskId"`
}
type Delivery struct {
	Mode   string `json:"mode"`
	URL    string `json:"url"`
	Secret string `json:"secret,omitempty"`
}
type Input struct {
	Name      string    `json:"name"`
	Arguments Arguments `json:"arguments"`
	Delivery  Delivery  `json:"delivery"`
	Cursor    *string   `json:"cursor,omitempty"`
	TTLMS     *int64    `json:"ttlMs,omitempty"`
}
type Receipt struct {
	ID            string    `json:"id"`
	RefreshBefore time.Time `json:"refreshBefore"`
	Cursor        *string   `json:"cursor"`
	Truncated     bool      `json:"truncated"`
}
type Subscription struct {
	ID, Scope, ProjectID string
	Arguments            Arguments
	Delivery             Delivery
	ExpiresAt            time.Time
}
type Pending struct {
	Subscription Subscription
	EventID      string
	Body         json.RawMessage
	Attempts     int
}
type Store interface {
	Put(context.Context, Subscription) error
	Remove(context.Context, string, string) error
	Pending(context.Context, time.Time) ([]Pending, error)
	Active(context.Context, string, time.Time) (bool, error)
	Finish(context.Context, Pending, string, time.Time) error
}
type Authorize func(context.Context, Arguments) (string, error)
type Status struct {
	ActiveSubscriptions  int `json:"activeSubscriptions"`
	StoppedSubscriptions int `json:"stoppedSubscriptions"`
	Pending              int `json:"pending"`
	Delivered            int `json:"delivered"`
	Exhausted            int `json:"exhausted"`
}

func (s *Service) Status(ctx context.Context, project, task string) (Status, error) {
	reader, ok := s.store.(interface {
		Status(context.Context, string, string, time.Time) (Status, error)
	})
	if !ok {
		return Status{}, errors.New("event status unavailable")
	}
	return reader.Status(ctx, project, task, time.Now().UTC())
}

type Service struct {
	store     Store
	authorize Authorize
	client    *http.Client
	cancel    context.CancelFunc
	done      chan struct{}
	once      sync.Once
}

func New(store Store, authorize Authorize) *Service {
	return &Service{store: store, authorize: authorize, client: publicClient()}
}
func (s *Service) Start(ctx context.Context) {
	s.once.Do(func() {
		ctx, s.cancel = context.WithCancel(ctx)
		s.done = make(chan struct{})
		go func() {
			defer close(s.done)
			timer := time.NewTicker(time.Second)
			defer timer.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-timer.C:
					s.Dispatch(ctx)
				}
			}
		}()
	})
}
func (s *Service) Close() {
	if s.cancel != nil {
		s.cancel()
		<-s.done
	}
}
func validID(v string) bool {
	return v != "" && len(v) <= 128 && !strings.ContainsAny(v, " \t\r\n\x00")
}
func subscriptionID(scope string, in Input) string {
	raw, _ := json.Marshal([]any{scope, in.Name, in.Arguments, in.Delivery.URL})
	sum := sha256.Sum256(raw)
	return "sub_" + hex.EncodeToString(sum[:])
}
func validate(in Input) error {
	u, e := url.Parse(in.Delivery.URL)
	if in.Name != Name || !validID(in.Arguments.AISessionID) || !validID(in.Arguments.TaskID) || in.Delivery.Mode != "webhook" || e != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" || len(in.Delivery.URL) > 2048 || in.Cursor != nil {
		return ErrInvalid
	}
	return nil
}
func secretKey(secret string) ([]byte, error) {
	if !strings.HasPrefix(secret, "whsec_") {
		return nil, ErrInvalid
	}
	key, e := base64.StdEncoding.DecodeString(strings.TrimPrefix(secret, "whsec_"))
	if e != nil || len(key) < 24 || len(key) > 64 {
		return nil, ErrInvalid
	}
	return key, nil
}
func (s *Service) Subscribe(ctx context.Context, scope string, in Input) (receipt Receipt, err error) {
	stage := "validation"
	reason := ""
	defer func() {
		// Callback URLs, keys, response bodies and network errors can contain
		// credentials. Only a fixed stage is safe for this diagnostic boundary.
		if err != nil {
			observability.Error(ctx, "workflow-events", "workflow.subscription.failed", "Task event subscription failed", map[string]any{"stage": stage, "reason": reason})
		} else {
			observability.Info(ctx, "workflow-events", "workflow.subscription.established", "Task event subscription established", nil)
		}
	}()
	if validate(in) != nil {
		return Receipt{}, ErrInvalid
	}
	if _, e := secretKey(in.Delivery.Secret); e != nil {
		return Receipt{}, e
	}
	ttl := int64(86400000)
	if in.TTLMS != nil {
		ttl = *in.TTLMS
	}
	if ttl < 60000 || ttl > 7*86400000 {
		return Receipt{}, ErrInvalid
	}
	stage = "authorization"
	project, e := s.authorize(ctx, in.Arguments)
	if e != nil {
		return Receipt{}, e
	}
	sub := Subscription{ID: subscriptionID(scope, in), Scope: scope, ProjectID: project, Arguments: in.Arguments, Delivery: in.Delivery, ExpiresAt: time.Now().UTC().Add(time.Duration(ttl) * time.Millisecond)}
	challenge := make([]byte, 32)
	stage = "challenge_generation"
	if _, e = rand.Read(challenge); e != nil {
		return Receipt{}, e
	}
	token := hex.EncodeToString(challenge)
	body, _ := json.Marshal(map[string]string{"type": "verification", "challenge": token})
	stage = "callback_request"
	response, e := s.send(ctx, sub, "verify_"+token, body)
	if e != nil {
		reason = callbackFailureKind(e)
		return Receipt{}, errors.New("callback verification failed")
	}
	defer response.Body.Close()
	stage = "callback_response"
	var answer struct {
		Challenge string `json:"challenge"`
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 || json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&answer) != nil || subtle.ConstantTimeCompare([]byte(answer.Challenge), []byte(token)) != 1 {
		return Receipt{}, errors.New("callback verification failed")
	}
	stage = "authorization_recheck"
	if _, e = s.authorize(ctx, in.Arguments); e != nil {
		return Receipt{}, e
	}
	stage = "persistence"
	if e = s.store.Put(ctx, sub); e != nil {
		return Receipt{}, e
	}
	return Receipt{ID: sub.ID, RefreshBefore: sub.ExpiresAt}, nil
}

func callbackFailureKind(err error) string {
	switch {
	case errors.Is(err, errCallbackDNS):
		return "DNS_UNAVAILABLE"
	case errors.Is(err, errCallbackNonPublic):
		return "NON_PUBLIC_ADDRESS"
	case errors.Is(err, context.DeadlineExceeded):
		return "TIMEOUT"
	case errors.Is(err, context.Canceled):
		return "CANCELED"
	}
	var networkError *net.OpError
	if errors.As(err, &networkError) {
		return "NETWORK_ERROR"
	}
	return "TRANSPORT_ERROR"
}
func (s *Service) Unsubscribe(ctx context.Context, scope string, in Input) error {
	if validate(in) != nil {
		return ErrInvalid
	}
	if _, e := s.authorize(ctx, in.Arguments); e != nil {
		return e
	}
	return s.store.Remove(ctx, scope, subscriptionID(scope, in))
}
func (s *Service) send(ctx context.Context, sub Subscription, id string, body []byte) (*http.Response, error) {
	key, e := secretKey(sub.Delivery.Secret)
	if e != nil {
		return nil, e
	}
	stamp := fmt.Sprint(time.Now().Unix())
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(id + "." + stamp + "."))
	mac.Write(body)
	req, e := http.NewRequestWithContext(ctx, http.MethodPost, sub.Delivery.URL, strings.NewReader(string(body)))
	if e != nil {
		return nil, e
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("webhook-id", id)
	req.Header.Set("webhook-timestamp", stamp)
	req.Header.Set("webhook-signature", "v1,"+base64.StdEncoding.EncodeToString(mac.Sum(nil)))
	req.Header.Set("X-MCP-Subscription-Id", sub.ID)
	return s.client.Do(req)
}

// Dispatch is single-owner in Core. Durable rows survive crashes and duplicate
// delivery retains its event ID. No result authorizes a workflow transition.
func (s *Service) Dispatch(ctx context.Context) {
	rows, e := s.store.Pending(ctx, time.Now().UTC())
	if e != nil {
		return
	}
	for _, row := range rows {
		if ctx.Err() != nil {
			return
		}
		active, e := s.store.Active(ctx, row.Subscription.ID, time.Now().UTC())
		if e != nil || !active {
			continue
		}
		status := "RETRY"
		attempt, cancel := context.WithTimeout(ctx, 6*time.Second)
		response, e := s.send(attempt, row.Subscription, row.EventID, row.Body)
		if e == nil {
			io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
			response.Body.Close()
			if response.StatusCode >= 200 && response.StatusCode < 300 {
				status = "DELIVERED"
			} else if response.StatusCode == 410 || response.StatusCode == 413 || response.StatusCode >= 400 && response.StatusCode < 500 && response.StatusCode != 429 {
				status = "STOPPED"
			}
		}
		cancel()
		if row.Attempts >= 7 && status == "RETRY" {
			status = "EXHAUSTED"
		}
		delay := time.Duration(1<<min(row.Attempts+1, 6)) * time.Second
		_ = s.store.Finish(ctx, row, status, time.Now().UTC().Add(delay))
	}
}
func publicIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return false
	}
	for _, cidr := range []string{"100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4", "2001:db8::/32", "64:ff9b::/96"} {
		if netip.MustParsePrefix(cidr).Contains(ip) {
			return false
		}
	}
	return true
}
func publicClient() *http.Client {
	transport := &http.Transport{TLSHandshakeTimeout: 3 * time.Second, ResponseHeaderTimeout: 4 * time.Second, DisableKeepAlives: true}
	resolverClient := callbackDNSClient()
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, e := net.SplitHostPort(address)
		if e != nil {
			return nil, e
		}
		ips, e := callbackIPs(ctx, host, func(ctx context.Context, host string) ([]netip.Addr, error) {
			return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		}, func(ctx context.Context, host string) ([]netip.Addr, error) {
			return callbackPublicDNS(ctx, host, resolverClient)
		})
		if e != nil {
			return nil, e
		}
		return (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, network, net.JoinHostPort(ips[0].String(), port))
	}
	return &http.Client{Transport: transport, Timeout: 6 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}
