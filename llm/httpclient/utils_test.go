package httpclient

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReadHTTPRequest_NoContentEncoding(t *testing.T) {
	body := []byte(`{"model":"gpt-4","messages":[{"role":"user","content":"hello"}]}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	got, err := ReadHTTPRequest(req)
	require.NoError(t, err)
	assert.Equal(t, body, got.Body)
	assert.Equal(t, "", got.Headers.Get("Content-Encoding"))
}

func TestReadHTTPRequest_IdentityEncoding(t *testing.T) {
	body := []byte(`{"model":"gpt-4","messages":[{"role":"user","content":"hello"}]}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Content-Encoding", "identity")

	got, err := ReadHTTPRequest(req)
	require.NoError(t, err)
	assert.Equal(t, body, got.Body)
	assert.Equal(t, "identity", got.Headers.Get("Content-Encoding"))
}

func TestReadHTTPRequest_MaxBytesError(t *testing.T) {
	rawReq := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader("12345"))
	recorder := httptest.NewRecorder()
	rawReq.Body = http.MaxBytesReader(recorder, rawReq.Body, 4)

	req, err := ReadHTTPRequest(rawReq)

	require.Nil(t, req)

	var httpErr *Error
	require.True(t, errors.As(err, &httpErr))
	require.Equal(t, http.StatusRequestEntityTooLarge, httpErr.StatusCode)
	require.Contains(t, string(httpErr.Body), "request body too large")
}

func TestReadHTTPRequest_ReusableBodyAvoidsCopy(t *testing.T) {
	body := []byte(`{"model":"gpt-4o"}`)
	rawReq := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	rawReq.Body = NewReusableReadCloser(body)

	req, err := ReadHTTPRequest(rawReq)

	require.NoError(t, err)
	require.Equal(t, string(body), string(req.Body))
	require.True(t, &body[0] == &req.Body[0])
}

func TestReadHTTPRequest_GzipEncoding(t *testing.T) {
	originalBody := []byte(`{"model":"gpt-4","messages":[{"role":"user","content":"hello"}]}`)

	var buf bytes.Buffer
	writer := gzip.NewWriter(&buf)
	_, err := writer.Write(originalBody)
	require.NoError(t, err)
	writer.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(buf.Bytes()))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Content-Encoding", "gzip")

	got, err := ReadHTTPRequest(req)
	require.NoError(t, err)
	assert.Equal(t, originalBody, got.Body)
	assert.Equal(t, "", got.Headers.Get("Content-Encoding"))
	assert.Equal(t, "", got.Headers.Get("Content-Length"))
}

func TestReadHTTPRequest_GzipEncodingXGzip(t *testing.T) {
	originalBody := []byte(`{"model":"gpt-4"}`)

	var buf bytes.Buffer
	writer := gzip.NewWriter(&buf)
	_, err := writer.Write(originalBody)
	require.NoError(t, err)
	writer.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(buf.Bytes()))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Content-Encoding", "x-gzip")

	got, err := ReadHTTPRequest(req)
	require.NoError(t, err)
	assert.Equal(t, originalBody, got.Body)
	assert.Equal(t, "", got.Headers.Get("Content-Encoding"))
}

func TestReadHTTPRequest_DeflateEncoding(t *testing.T) {
	originalBody := []byte(`{"model":"gpt-4","messages":[{"role":"user","content":"hello"}]}`)

	var buf bytes.Buffer
	writer, err := flate.NewWriter(&buf, flate.DefaultCompression)
	require.NoError(t, err)
	_, err = writer.Write(originalBody)
	require.NoError(t, err)
	writer.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(buf.Bytes()))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Content-Encoding", "deflate")

	got, err := ReadHTTPRequest(req)
	require.NoError(t, err)
	assert.Equal(t, originalBody, got.Body)
	assert.Equal(t, "", got.Headers.Get("Content-Encoding"))
	assert.Equal(t, "", got.Headers.Get("Content-Length"))
}

func TestReadHTTPRequest_DeflateZlibEncoding(t *testing.T) {
	originalBody := []byte(`{"model":"gpt-4","messages":[{"role":"user","content":"hello"}]}`)

	var buf bytes.Buffer
	writer := zlib.NewWriter(&buf)
	_, err := writer.Write(originalBody)
	require.NoError(t, err)
	writer.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(buf.Bytes()))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Content-Encoding", "deflate")

	got, err := ReadHTTPRequest(req)
	require.NoError(t, err)
	assert.Equal(t, originalBody, got.Body)
	assert.Equal(t, "", got.Headers.Get("Content-Encoding"))
	assert.Equal(t, "", got.Headers.Get("Content-Length"))
}

func TestReadHTTPRequest_ZstdEncoding(t *testing.T) {
	originalBody := []byte(`{"model":"gpt-4","messages":[{"role":"user","content":"hello"}]}`)

	encoder, err := zstd.NewWriter(nil)
	require.NoError(t, err)
	compressedBody := encoder.EncodeAll(originalBody, nil)
	encoder.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(compressedBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Content-Encoding", "zstd")

	got, err := ReadHTTPRequest(req)
	require.NoError(t, err)
	assert.Equal(t, originalBody, got.Body)
	assert.Equal(t, "", got.Headers.Get("Content-Encoding"))
	assert.Equal(t, "", got.Headers.Get("Content-Length"))
}

func TestReadHTTPRequest_EncodingCaseInsensitive(t *testing.T) {
	tests := []struct {
		name     string
		encoding string
		compress func(t *testing.T, body []byte) []byte
	}{
		{
			name:     "gzip uppercase",
			encoding: "GZIP",
			compress: func(t *testing.T, body []byte) []byte {
				var buf bytes.Buffer
				writer := gzip.NewWriter(&buf)
				_, err := writer.Write(body)
				require.NoError(t, err)
				writer.Close()
				return buf.Bytes()
			},
		},
		{
			name:     "deflate uppercase",
			encoding: "DEFLATE",
			compress: func(t *testing.T, body []byte) []byte {
				var buf bytes.Buffer
				writer, err := flate.NewWriter(&buf, flate.DefaultCompression)
				require.NoError(t, err)
				_, err = writer.Write(body)
				require.NoError(t, err)
				writer.Close()
				return buf.Bytes()
			},
		},
		{
			name:     "zstd with spaces",
			encoding: "  ZSTD  ",
			compress: func(t *testing.T, body []byte) []byte {
				encoder, err := zstd.NewWriter(nil)
				require.NoError(t, err)
				compressed := encoder.EncodeAll(body, nil)
				encoder.Close()
				return compressed
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			originalBody := []byte(`{"model":"gpt-4"}`)
			compressedBody := tt.compress(t, originalBody)

			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(compressedBody))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Content-Encoding", tt.encoding)

			got, err := ReadHTTPRequest(req)
			require.NoError(t, err)
			assert.Equal(t, originalBody, got.Body)
		})
	}
}

func TestReadHTTPRequest_UnsupportedContentEncoding(t *testing.T) {
	body := []byte(`{"model":"gpt-4"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Content-Encoding", "br")

	_, err := ReadHTTPRequest(req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported content encoding")
}

func TestReadHTTPRequest_InvalidGzipData(t *testing.T) {
	invalidData := []byte("this is not valid gzip compressed data")
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(invalidData))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Content-Encoding", "gzip")

	_, err := ReadHTTPRequest(req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to create gzip reader")
}

func TestReadHTTPRequest_InvalidDeflateData(t *testing.T) {
	invalidData := []byte("this is not valid deflate compressed data")
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(invalidData))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Content-Encoding", "deflate")

	_, err := ReadHTTPRequest(req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to decompress deflate body")
}

func TestReadHTTPRequest_InvalidZstdData(t *testing.T) {
	invalidData := []byte("this is not valid zstd compressed data")
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(invalidData))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Content-Encoding", "zstd")

	_, err := ReadHTTPRequest(req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to decode zstd compressed body")
}

func TestReadHTTPRequest_EmptyBody(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("Content-Type", "application/json")

	got, err := ReadHTTPRequest(req)
	require.NoError(t, err)
	assert.Empty(t, got.Body)
}

func TestReadHTTPRequest_EmptyBodyWithContentEncoding(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Content-Encoding", "zstd")

	got, err := ReadHTTPRequest(req)
	require.NoError(t, err)
	assert.Empty(t, got.Body)
}

func TestDecodeRequestBody_NoEncoding(t *testing.T) {
	body := []byte(`{"test":"data"}`)
	headers := http.Header{}

	got, err := decodeRequestBody(body, headers)
	require.NoError(t, err)
	assert.Equal(t, body, got)
}

func TestDecodeRequestBody_IdentityEncoding(t *testing.T) {
	body := []byte(`{"test":"data"}`)
	headers := http.Header{}
	headers.Set("Content-Encoding", "identity")

	got, err := decodeRequestBody(body, headers)
	require.NoError(t, err)
	assert.Equal(t, body, got)
}

func TestDecodeRequestBody_GzipEncoding(t *testing.T) {
	originalBody := []byte(`{"test":"data"}`)

	var buf bytes.Buffer
	writer := gzip.NewWriter(&buf)
	_, err := writer.Write(originalBody)
	require.NoError(t, err)
	writer.Close()

	headers := http.Header{}
	headers.Set("Content-Encoding", "gzip")
	headers.Set("Content-Length", "100")

	got, err := decodeRequestBody(buf.Bytes(), headers)
	require.NoError(t, err)
	assert.Equal(t, originalBody, got)
	assert.Equal(t, "", headers.Get("Content-Encoding"))
	assert.Equal(t, "", headers.Get("Content-Length"))
}

func TestDecodeRequestBody_DeflateEncoding(t *testing.T) {
	originalBody := []byte(`{"test":"data"}`)

	var buf bytes.Buffer
	writer, err := flate.NewWriter(&buf, flate.DefaultCompression)
	require.NoError(t, err)
	_, err = writer.Write(originalBody)
	require.NoError(t, err)
	writer.Close()

	headers := http.Header{}
	headers.Set("Content-Encoding", "deflate")

	got, err := decodeRequestBody(buf.Bytes(), headers)
	require.NoError(t, err)
	assert.Equal(t, originalBody, got)
	assert.Equal(t, "", headers.Get("Content-Encoding"))
}

func TestDecodeRequestBody_ZstdEncoding(t *testing.T) {
	originalBody := []byte(`{"test":"data"}`)

	encoder, err := zstd.NewWriter(nil)
	require.NoError(t, err)
	compressedBody := encoder.EncodeAll(originalBody, nil)
	encoder.Close()

	headers := http.Header{}
	headers.Set("Content-Encoding", "zstd")

	got, err := decodeRequestBody(compressedBody, headers)
	require.NoError(t, err)
	assert.Equal(t, originalBody, got)
	assert.Equal(t, "", headers.Get("Content-Encoding"))
}

func TestRequestReleaseBody(t *testing.T) {
	req := &Request{
		Body:     []byte("request body"),
		JSONBody: []byte(`{"body":"request"}`),
	}

	req.ReleaseBody()

	require.Nil(t, req.Body)
	require.Nil(t, req.JSONBody)
}

func TestMergeHTTPHeaders(t *testing.T) {
	RegisterMergeWithAppendHeaders("User-Agent", "Accept")

	t.Run("should merge headers and query params", func(t *testing.T) {
		dest := &Request{
			Headers: http.Header{"Content-Type": []string{"application/json"}},
			Query:   url.Values{"q": []string{"old"}},
		}
		src := &Request{
			Headers: http.Header{"User-Agent": []string{"Test"}},
			Query:   url.Values{"q": []string{"new"}, "page": []string{"1"}},
		}

		got := MergeInboundRequest(dest, src)
		require.Equal(t, "application/json", got.Headers.Get("Content-Type"))
		require.Equal(t, "Test", got.Headers.Get("User-Agent"))
		require.Equal(t, "old", got.Query.Get("q"))
		require.Equal(t, "1", got.Query.Get("page"))
	})

	t.Run("should block Cloudflare headers by prefix", func(t *testing.T) {
		dest := &Request{
			Headers: http.Header{"Content-Type": []string{"application/json"}},
			Query:   url.Values{},
		}
		src := &Request{
			Headers: http.Header{
				"Cf-Ray":           []string{"abc123"},
				"Cf-Connecting-Ip": []string{"1.2.3.4"},
				"Cf-Ipcountry":     []string{"US"},
				"Cf-Visitor":       []string{`{"scheme":"https"}`},
				"Cdn-Loop":         []string{"cloudflare; loops=1"},
				"User-Agent":       []string{"Test/1.0"},
			},
			Query: url.Values{},
		}

		got := MergeInboundRequest(dest, src)
		require.Empty(t, got.Headers.Get("Cf-Ray"))
		require.Empty(t, got.Headers.Get("Cf-Connecting-Ip"))
		require.Empty(t, got.Headers.Get("Cf-Ipcountry"))
		require.Empty(t, got.Headers.Get("Cf-Visitor"))
		require.Empty(t, got.Headers.Get("Cdn-Loop"))
		require.Equal(t, "Test/1.0", got.Headers.Get("User-Agent"))
	})

	t.Run("should block proxy and client ip disclosure headers", func(t *testing.T) {
		dest := &Request{
			Headers: http.Header{"Content-Type": []string{"application/json"}},
			Query:   url.Values{},
		}
		src := &Request{
			Headers: http.Header{
				"Forwarded":                  []string{"for=1.2.3.4;proto=https"},
				"Remote-Addr":                []string{"1.2.3.4"},
				"Remote-Host":                []string{"1.2.3.4"},
				"True-Client-Ip":             []string{"1.2.3.4"},
				"Via":                        []string{"1.1 proxy"},
				"Proxy-Connection":           []string{"keep-alive"},
				"X-Client-Ip":                []string{"1.2.3.4"},
				"X-Cluster-Client-Ip":        []string{"1.2.3.4"},
				"X-Envoy-External-Address":   []string{"1.2.3.4"},
				"X-Original-Forwarded-For":   []string{"1.2.3.4"},
				"X-Forwarded-Ssl":            []string{"on"},
				"X-Provider-Feature-Request": []string{"keep-me"},
			},
			Query: url.Values{},
		}

		got := MergeInboundRequest(dest, src)

		for header := range src.Headers {
			if header == "X-Provider-Feature-Request" {
				continue
			}
			require.Empty(t, got.Headers.Get(header), "%s should not be merged", header)
		}
		require.Equal(t, "keep-me", got.Headers.Get("X-Provider-Feature-Request"))
	})

	t.Run("should return dest if src is nil", func(t *testing.T) {
		dest := &Request{Headers: http.Header{"X-Test": []string{"val"}}}
		got := MergeInboundRequest(dest, nil)
		require.Equal(t, dest, got)
	})
}
