// Package egress builds per-account outbound HTTP transports. Proxy failure
// never falls back to direct connection implicitly (§8, §22): failure_mode=stop
// aborts, failure_mode=fallback only tries the explicitly configured fallback
// list; exhausting it returns an error.
package egress

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/proxy"

	"subai/internal/storage"
)

var ErrEgressFailed = errors.New("egress failed")

type Profile struct {
	ID       string
	Name     string
	Kind     string // direct|http|socks5
	Endpoint string
	Username string
	Password string
	Status   string
}

type Policy struct {
	ID          string
	Name        string
	Primary     Profile
	FailureMode string // stop|fallback
	Fallbacks   []Profile
}

// Resolver loads egress policies with decrypted proxy credentials.
type Resolver struct {
	db  *storage.DB
	mu  syncMap
	ttl time.Duration
}

type syncMap struct {
	mu sync.Mutex
	m  map[string]cacheEntry
}

type cacheEntry struct {
	p   *Policy
	err error
	at  time.Time
}

func NewResolver(db *storage.DB) *Resolver {
	return &Resolver{db: db, ttl: 30 * time.Second, mu: syncMap{m: map[string]cacheEntry{}}}
}

func (r *Resolver) GetPolicy(ctx context.Context, policyID string) (*Policy, error) {
	r.mu.mu.Lock()
	if e, ok := r.mu.m[policyID]; ok && time.Since(e.at) < r.ttl {
		r.mu.mu.Unlock()
		return e.p, e.err
	}
	r.mu.mu.Unlock()

	p, err := r.loadPolicy(ctx, policyID)
	r.mu.mu.Lock()
	r.mu.m[policyID] = cacheEntry{p: p, err: err, at: time.Now()}
	r.mu.mu.Unlock()
	return p, err
}

func (r *Resolver) loadPolicy(ctx context.Context, policyID string) (*Policy, error) {
	var p Policy
	var primaryID string
	var fallbackIDs []string
	err := r.db.Pool.QueryRow(ctx, `
		SELECT id, name, primary_proxy_id, failure_mode, ordered_fallback_proxy_ids
		FROM egress_policies WHERE id=$1`, policyID).
		Scan(&p.ID, &p.Name, &primaryID, &p.FailureMode, &fallbackIDs)
	if err != nil {
		return nil, fmt.Errorf("egress policy %s: %w", policyID, err)
	}
	ids := append([]string{primaryID}, fallbackIDs...)
	profiles := map[string]Profile{}
	for _, id := range ids {
		prof, err := r.loadProfile(ctx, id)
		if err != nil {
			return nil, err
		}
		profiles[id] = *prof
	}
	p.Primary = profiles[primaryID]
	for _, id := range fallbackIDs {
		if prof, ok := profiles[id]; ok && prof.Status == "active" {
			p.Fallbacks = append(p.Fallbacks, prof)
		}
	}
	return &p, nil
}

func (r *Resolver) loadProfile(ctx context.Context, id string) (*Profile, error) {
	var prof Profile
	var endpoint *string
	var creds []byte
	err := r.db.Pool.QueryRow(ctx, `
		SELECT id, name, kind, endpoint, credentials_ciphertext, status
		FROM proxy_profiles WHERE id=$1`, id).
		Scan(&prof.ID, &prof.Name, &prof.Kind, &endpoint, &creds, &prof.Status)
	if err != nil {
		return nil, fmt.Errorf("proxy profile %s: %w", id, err)
	}
	if endpoint != nil {
		prof.Endpoint = *endpoint
	}
	if len(creds) > 0 {
		plain, err := r.db.Decrypt(creds)
		if err != nil {
			return nil, fmt.Errorf("decrypt proxy credentials: %w", err)
		}
		var c struct{ Username, Password string }
		if json.Unmarshal(plain, &c) == nil {
			prof.Username, prof.Password = c.Username, c.Password
		}
	}
	return &prof, nil
}

// ClientForProfile builds an isolated HTTP client for one profile. Connection
// pools are keyed by (profile, policy version) via a fresh Transport per
// resolution so a changed egress never reuses old-egress connections (§22).
func ClientForProfile(prof Profile, timeout time.Duration) (*http.Client, error) {
	if prof.Status != "active" {
		return nil, fmt.Errorf("%w: proxy profile %s disabled", ErrEgressFailed, prof.Name)
	}
	transport := &http.Transport{
		MaxIdleConns:        16,
		IdleConnTimeout:     60 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,
		TLSClientConfig:     &tls.Config{MinVersion: tls.VersionTLS12},
		// Never inherit system environment proxies implicitly (§22).
		Proxy: nil,
		DialContext: (&net.Dialer{
			Timeout:   15 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
	}
	switch strings.ToLower(prof.Kind) {
	case "direct":
		// explicit direct egress
	case "http":
		u, err := url.Parse(prof.Endpoint)
		if err != nil {
			return nil, fmt.Errorf("proxy endpoint: %w", err)
		}
		if prof.Username != "" || prof.Password != "" {
			u.User = url.UserPassword(prof.Username, prof.Password)
		}
		transport.Proxy = http.ProxyURL(u)
	case "socks5":
		var auth *proxy.Auth
		if prof.Username != "" || prof.Password != "" {
			auth = &proxy.Auth{User: prof.Username, Password: prof.Password}
		}
		// SOCKS5 performs remote DNS through the proxy (domain names are sent
		// to the proxy; documented in deploy/README).
		dialer, err := proxy.SOCKS5("tcp", prof.Endpoint, auth, proxy.Direct)
		if err != nil {
			return nil, fmt.Errorf("socks5 dialer: %w", err)
		}
		if cd, ok := dialer.(proxy.ContextDialer); ok {
			transport.DialContext = cd.DialContext
		} else {
			return nil, fmt.Errorf("socks5 dialer does not support context")
		}
	default:
		return nil, fmt.Errorf("unknown proxy kind %q", prof.Kind)
	}
	return &http.Client{Transport: transport, Timeout: timeout}, nil
}

// Dispatch runs fn against the policy's primary egress, honouring
// failure_mode. Replay safety (review R2-01) classifies every failure into
// exactly one of three outcomes:
//
//   - dial-phase failure (TCP/proxy connect never established): the request
//     was never transmitted → fallback allowed;
//   - upstream-status answer: the request WAS received and answered → never
//     retried on any egress;
//   - everything else after the POST was transmitted (connection broken
//     before response headers, mid-stream break, stream started): outcome
//     unconfirmable → ErrUnconfirmed, never replayed.
func (p *Policy) Dispatch(ctx context.Context, timeout time.Duration, sent *bool, fn func(client *http.Client, prof Profile) error) (Profile, error) {
	try := func(prof Profile) error { return fn(mustClient(prof, timeout), prof) }

	err := try(p.Primary)
	if err == nil {
		return p.Primary, nil
	}
	// Stream already delivered bytes downstream: unconfirmable, full stop.
	if sent != nil && *sent {
		return p.Primary, fmt.Errorf("%w: stream already started; refusing fallback replay", ErrStreamStarted)
	}
	if cls := classify(err); cls == classStatus {
		// Upstream received the POST and answered: not a transport issue.
		return p.Primary, fmt.Errorf("%w: %v", ErrUnconfirmed, err)
	}
	if p.FailureMode != "fallback" {
		return p.Primary, fmt.Errorf("%w: primary egress %s: %v (failure_mode=%s)", ErrEgressFailed, p.Primary.Name, err, p.FailureMode)
	}
	if cls := classify(err); cls != classDial {
		// POST transmitted but outcome unknown (broken before headers, mid-
		// request failure): replaying could double-execute (review R2-01).
		return p.Primary, fmt.Errorf("%w: request transmitted via %s but outcome unconfirmable: %v", ErrUnconfirmed, p.Primary.Name, err)
	}
	for _, fb := range p.Fallbacks {
		fberr := try(fb)
		switch {
		case fberr == nil:
			return fb, nil
		case sent != nil && *sent:
			return p.Primary, fmt.Errorf("%w: stream started during fallback; refusing replay", ErrStreamStarted)
		case classify(fberr) != classDial:
			// per-fallback classification (review R2-01): this fallback's
			// upstream received the request — stop, never try the next one.
			return p.Primary, fmt.Errorf("%w: request transmitted via %s but outcome unconfirmable: %v", ErrUnconfirmed, fb.Name, fberr)
		}
	}
	return p.Primary, fmt.Errorf("%w: all fallback egresses exhausted for policy %s; no implicit direct allowed", ErrEgressFailed, p.Name)
}

// ErrStreamStarted marks unconfirmable dispatches where bytes already reached
// the client (§19). ErrUnconfirmed marks dispatches whose POST reached an
// upstream but whose outcome could not be observed.
var ErrStreamStarted = errors.New("egress stream already started")
var ErrUnconfirmed = errors.New("egress outcome unconfirmable")

type errClass int

const (
	classDial   errClass = iota // request never transmitted
	classOther                  // transmitted, outcome unknown
	classStatus                 // transmitted and answered with HTTP status
)

// classify inspects the error chain: dial/proxy-connect failures prove the
// request never left; upstreamStatusError proves it was received AND
// answered; everything else is unconfirmable.
func classify(err error) errClass {
	var se interface{ HTTPStatus() int }
	if errors.As(err, &se) {
		return classStatus
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) && (opErr.Op == "dial" || opErr.Op == "proxyconnect") {
		return classDial
	}
	// SOCKS5/HTTP proxy dialers surface plain dial errors too; treat marked
	// egress construction failures as dial-phase (request never sent).
	if errors.Is(err, ErrEgressFailed) {
		return classDial
	}
	return classOther
}

func mustClient(p Profile, timeout time.Duration) *http.Client {
	c, err := ClientForProfile(p, timeout)
	if err != nil {
		return &http.Client{Transport: failingTransport{err}, Timeout: timeout}
	}
	return c
}

type failingTransport struct{ err error }

func (f failingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	// Egress construction failure: the request never left this process.
	return nil, fmt.Errorf("%w: egress unusable: %v", ErrEgressFailed, f.err)
}
