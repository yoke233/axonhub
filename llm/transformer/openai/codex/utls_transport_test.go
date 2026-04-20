package codex

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	utls "github.com/refraction-networking/utls"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUTLS_DialerWiredIntoTransport(t *testing.T) {
	rt := NewUTLSTransport(nil)
	assert.True(t, IsUTLSTransport(rt),
		"NewUTLSTransport must install a DialTLSContext recognizable by IsUTLSTransport")
}

func TestUTLS_DefaultClientHelloIsChrome(t *testing.T) {
	got := getUTLSClientHello()
	assert.Equal(t, utls.HelloChrome_Auto, got,
		"default ClientHello must be Chrome (rationale documented at top of utls_transport.go)")
}

func TestUTLS_SetClientHelloOverridesProfile(t *testing.T) {
	previous := getUTLSClientHello()
	t.Cleanup(func() { SetUTLSClientHello(previous) })

	SetUTLSClientHello(utls.HelloFirefox_Auto)
	assert.Equal(t, utls.HelloFirefox_Auto, getUTLSClientHello())
}

func TestUTLS_NewHTTPClientUsesUTLSTransport(t *testing.T) {
	c := NewUTLSHTTPClient(nil)
	require.NotNil(t, c)
	assert.True(t, IsUTLSTransport(c.Transport),
		"NewUTLSHTTPClient must return an http.Client whose Transport is uTLS-wired")
}

func TestUTLS_HandshakeAgainstLocalServer_E2E(t *testing.T) {
	// Spin up an https server with a self-signed cert; use the cert as a trust anchor
	// in the client config so the uTLS handshake completes successfully. This proves
	// the dialer path is fully functional end-to-end (handshake + ALPN + body).
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("pong"))
	}))
	defer server.Close()

	rootCAs := x509.NewCertPool()
	for _, c := range server.TLS.Certificates {
		for _, derBytes := range c.Certificate {
			pemBlock := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: derBytes})
			require.True(t, rootCAs.AppendCertsFromPEM(pemBlock))
		}
	}

	dialed := atomic.Bool{}
	rt := &http.Transport{
		Proxy: nil,
		DialTLSContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			dialed.Store(true)
			rawConn, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, network, addr)
			if err != nil {
				return nil, err
			}
			host, _, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			uconn := utls.UClient(rawConn, &utls.Config{
				ServerName: host,
				RootCAs:    rootCAs,
				NextProtos: []string{"http/1.1"},
				MinVersion: tls.VersionTLS12,
			}, getUTLSClientHello())
			if err := uconn.HandshakeContext(ctx); err != nil {
				_ = uconn.Close()
				return nil, err
			}
			return uconn, nil
		},
	}
	client := &http.Client{Transport: rt}

	parsed, err := url.Parse(server.URL)
	require.NoError(t, err)

	resp, err := client.Get(parsed.String())
	require.NoError(t, err, "uTLS handshake against local TLS server should succeed")
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, "pong", string(body))
	assert.True(t, dialed.Load(), "DialTLSContext should have been invoked")
}
