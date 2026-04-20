package codex

import (
	"bufio"
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"

	utls "github.com/refraction-networking/utls"
	"golang.org/x/net/http2"
)

// UTLSClientHelloID selects the TLS ClientHello fingerprint applied to outbound codex
// requests. We default to a recent Chrome profile because the OpenAI ChatGPT backend
// is overwhelmingly hit by Chrome-based clients, so blending in there is much harder
// to fingerprint than the Go default ClientHello (which OpenAI can almost certainly
// detect — it's distinctive). Real codex_cli_rs uses rustls; both Chrome and rustls
// look similar enough that this profile holds up well.
//
// Override at process start with SetUTLSClientHello if you need a different profile
// (e.g. HelloFirefox_Auto, HelloIOS_Auto).
var (
	utlsClientHelloMu sync.RWMutex
	utlsClientHelloID = utls.HelloChrome_Auto
)

// SetUTLSClientHello swaps the ClientHello profile used by the codex uTLS transport.
// Call before constructing the codex outbound transformer for it to take effect on
// subsequent requests.
func SetUTLSClientHello(id utls.ClientHelloID) {
	utlsClientHelloMu.Lock()
	defer utlsClientHelloMu.Unlock()
	utlsClientHelloID = id
}

func getUTLSClientHello() utls.ClientHelloID {
	utlsClientHelloMu.RLock()
	defer utlsClientHelloMu.RUnlock()
	return utlsClientHelloID
}

// NewUTLSTransport constructs an http.RoundTripper that performs HTTPS handshakes via
// uTLS using the configured ClientHello, while leaving plain HTTP and proxy semantics
// to a standard http.Transport. Intended for the codex outbound only — bridging the
// codex traffic past TLS-fingerprint-based detection while keeping the rest of axonhub
// on the regular Go TLS stack to limit blast radius.
//
// Background: the Go net/http TLS stack produces a distinctive JA3/JA4 signature that
// is trivially detectable. Real codex_cli_rs is built on rustls; OpenAI can (and likely
// does) bucket inbound requests by TLS fingerprint to spot CLI-style traffic that came
// in over a non-CLI client. Pretending to be Chrome is a robust approximation since the
// volume of Chrome traffic makes the bucket too noisy to act on.
func NewUTLSTransport(base *http.Transport) http.RoundTripper {
	if base == nil {
		base = newDefaultTransport()
	}

	base.DialTLSContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		return dialUTLS(ctx, network, addr, base)
	}
	base.TLSClientConfig = &tls.Config{InsecureSkipVerify: false}
	base.ForceAttemptHTTP2 = true
	// Best-effort h2 wiring; a failure leaves http/1.1 working, which the codex backend supports.
	_ = http2.ConfigureTransport(base)
	return base
}

func newDefaultTransport() *http.Transport {
	return &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   16,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
}

func dialUTLS(ctx context.Context, network, addr string, base *http.Transport) (net.Conn, error) {
	dialer := &net.Dialer{
		Timeout:   30 * time.Second,
		KeepAlive: 30 * time.Second,
	}

	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("split host: %w", err)
	}

	if base.Proxy != nil {
		proxyURL, err := base.Proxy(&http.Request{URL: &url.URL{Scheme: "https", Host: addr}})
		if err == nil && proxyURL != nil {
			rawConn, err := dialer.DialContext(ctx, network, proxyURL.Host)
			if err != nil {
				return nil, fmt.Errorf("proxy dial: %w", err)
			}
			if err := connectProxy(rawConn, addr); err != nil {
				_ = rawConn.Close()
				return nil, err
			}
			return wrapUTLS(ctx, rawConn, host)
		}
	}

	rawConn, err := dialer.DialContext(ctx, network, addr)
	if err != nil {
		return nil, err
	}
	return wrapUTLS(ctx, rawConn, host)
}

func wrapUTLS(ctx context.Context, rawConn net.Conn, host string) (net.Conn, error) {
	uconn := utls.UClient(rawConn, &utls.Config{
		ServerName: host,
		NextProtos: []string{"h2", "http/1.1"},
	}, getUTLSClientHello())

	if deadline, ok := ctx.Deadline(); ok {
		_ = uconn.SetDeadline(deadline)
	} else {
		_ = uconn.SetDeadline(time.Now().Add(10 * time.Second))
	}
	if err := uconn.HandshakeContext(ctx); err != nil {
		_ = uconn.Close()
		return nil, fmt.Errorf("utls handshake: %w", err)
	}
	_ = uconn.SetDeadline(time.Time{})
	return uconn, nil
}

// connectProxy issues an HTTP CONNECT through a forward proxy so the upstream uTLS
// handshake reaches the real codex backend. We do this by hand because uTLS owns the
// TLS dial step. Proxy auth must be handled at the http.Transport level if needed.
func connectProxy(c net.Conn, addr string) error {
	req := &http.Request{
		Method: http.MethodConnect,
		URL:    &url.URL{Opaque: addr},
		Host:   addr,
		Header: make(http.Header),
	}

	if err := req.Write(c); err != nil {
		return fmt.Errorf("proxy CONNECT write: %w", err)
	}

	resp, err := http.ReadResponse(bufio.NewReader(c), req)
	if err != nil {
		return fmt.Errorf("proxy CONNECT read: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("proxy CONNECT failed: %s", resp.Status)
	}
	return nil
}

// IsUTLSTransport reports whether the given RoundTripper has been wired with the codex
// uTLS dialer. Used by tests and diagnostics to assert wiring without poking unexported
// state.
func IsUTLSTransport(rt http.RoundTripper) bool {
	if rt == nil {
		return false
	}
	t, ok := rt.(*http.Transport)
	if !ok {
		return false
	}
	return t.DialTLSContext != nil
}

// NewUTLSHTTPClient returns an *http.Client whose Transport performs uTLS handshakes.
// The proxyURLFn lets callers plug in any proxy resolution they need (pass nil for
// http.ProxyFromEnvironment). Use NewHttpClientFromUTLS to wrap into the axonhub
// httpclient.HttpClient surface.
func NewUTLSHTTPClient(proxyURLFn func(*http.Request) (*url.URL, error)) *http.Client {
	base := newDefaultTransport()
	if proxyURLFn != nil {
		base.Proxy = proxyURLFn
	}
	rt := NewUTLSTransport(base)
	return &http.Client{
		Transport: rt,
	}
}
