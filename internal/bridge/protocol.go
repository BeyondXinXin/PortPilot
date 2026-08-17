// Package bridge implements PortPilot Remote Bridge (PPBR/1).
package bridge

import (
	"bufio"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	Version       = 1
	DefaultPort   = 39090
	DefaultLanes  = 8
	DefaultChunk  = 64 * 1024
	maxFrameBytes = 4 * 1024 * 1024
)

type frameType uint8

const (
	frameHello frameType = iota + 1
	frameHelloAck
	frameAuth
	frameAuthOK
	frameAuthError
	frameRequestHeaders
	frameRequestData
	frameRequestEnd
	frameResponseHeaders
	frameResponseData
	frameResponseEnd
	frameACK
	frameReset
	framePing
	framePong
	frameLaneHello
	frameLaneReady
	frameError
	frameWebSocketHello
)

type frame struct {
	typeID   frameType
	flags    uint16
	streamID uint64
	seq      uint64
	payload  []byte
}

type frameConn struct {
	conn net.Conn
	mu   sync.Mutex
	dead atomic.Bool
}

func (c *frameConn) write(f frame) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.dead.Load() {
		return net.ErrClosed
	}
	if len(f.payload) > maxFrameBytes {
		return fmt.Errorf("PPBR frame too large: %d bytes", len(f.payload))
	}
	header := make([]byte, 28)
	copy(header[:4], "PPBR")
	header[4] = Version
	header[5] = byte(f.typeID)
	binary.BigEndian.PutUint16(header[6:8], f.flags)
	binary.BigEndian.PutUint64(header[8:16], f.streamID)
	binary.BigEndian.PutUint64(header[16:24], f.seq)
	binary.BigEndian.PutUint32(header[24:28], uint32(len(f.payload)))
	if _, err := c.conn.Write(header); err != nil {
		c.dead.Store(true)
		return err
	}
	if len(f.payload) != 0 {
		if _, err := c.conn.Write(f.payload); err != nil {
			c.dead.Store(true)
			return err
		}
	}
	return nil
}

func (c *frameConn) read() (frame, error) {
	var header [28]byte
	if _, err := io.ReadFull(c.conn, header[:]); err != nil {
		c.dead.Store(true)
		return frame{}, err
	}
	if string(header[:4]) != "PPBR" || header[4] != Version {
		return frame{}, errors.New("invalid PPBR protocol header")
	}
	length := binary.BigEndian.Uint32(header[24:28])
	if length > maxFrameBytes {
		return frame{}, fmt.Errorf("PPBR frame exceeds limit: %d", length)
	}
	payload := make([]byte, int(length))
	if _, err := io.ReadFull(c.conn, payload); err != nil {
		c.dead.Store(true)
		return frame{}, err
	}
	return frame{typeID: frameType(header[5]), flags: binary.BigEndian.Uint16(header[6:8]), streamID: binary.BigEndian.Uint64(header[8:16]), seq: binary.BigEndian.Uint64(header[16:24]), payload: payload}, nil
}

func (c *frameConn) close() {
	if c != nil && !c.dead.Swap(true) {
		_ = c.conn.Close()
	}
}

type helloMessage struct {
	SessionID string `json:"sessionID"`
}

type nonceMessage struct {
	Nonce string `json:"nonce"`
}

type authMessage struct {
	Proof string `json:"proof"`
}

type laneMessage struct {
	SessionID string `json:"sessionID"`
	LaneID    int    `json:"laneID"`
	Proof     string `json:"proof"`
}

type webSocketHello struct {
	Nonce   string      `json:"nonce"`
	Proof   string      `json:"proof"`
	Request requestMeta `json:"request"`
}

type requestMeta struct {
	Method        string      `json:"method"`
	Path          string      `json:"path"`
	Header        http.Header `json:"header"`
	ContentLength int64       `json:"contentLength"`
}

type responseMeta struct {
	StatusCode    int         `json:"statusCode"`
	Status        string      `json:"status"`
	Header        http.Header `json:"header"`
	ContentLength int64       `json:"contentLength"`
	Streaming     bool        `json:"streaming"`
}

func encode(value any) ([]byte, error)    { return json.Marshal(value) }
func decode(data []byte, value any) error { return json.Unmarshal(data, value) }

func randomID(bytes int) string {
	buffer := make([]byte, bytes)
	if _, err := rand.Read(buffer); err != nil {
		return fmt.Sprintf("fallback-%d", time.Now().UnixNano())
	}
	return base64.RawURLEncoding.EncodeToString(buffer)
}

// NewPairToken returns a 256-bit pairing secret encoded for configuration files.
func NewPairToken() string { return randomID(32) }

// PairingCode is the portable configuration value copied from a bridge server
// to a bridge client. It deliberately contains the secret and must be treated
// like a password.
func PairingCode(remoteAddress, token string, lanes int) string {
	payload, err := json.Marshal(struct {
		Remote string `json:"remote"`
		Token  string `json:"token"`
		Lanes  int    `json:"lanes"`
	}{Remote: remoteAddress, Token: token, Lanes: lanes})
	if err != nil {
		return ""
	}
	// A compact, single-token representation survives chat applications better
	// than a URL with query parameters such as '&' and '?'.
	return "PPBR1-" + base64.RawURLEncoding.EncodeToString(payload)
}

// ParsePairingCode validates a pairing code without writing its secret to a log.
func ParsePairingCode(value string) (remoteAddress, token string, lanes int, err error) {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "PPBR1-") {
		payload, decodeErr := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(value, "PPBR1-"))
		if decodeErr != nil {
			return "", "", 0, errors.New("invalid Remote Bridge pairing code")
		}
		var data struct {
			Remote string `json:"remote"`
			Token  string `json:"token"`
			Lanes  int    `json:"lanes"`
		}
		if json.Unmarshal(payload, &data) != nil {
			return "", "", 0, errors.New("invalid Remote Bridge pairing code")
		}
		remoteAddress, token, lanes = data.Remote, data.Token, data.Lanes
	} else {
		// Accept pre-release codes so existing pairings remain importable.
		parsed, parseErr := url.Parse(value)
		if parseErr != nil || parsed.Scheme != "ppbridge" || parsed.Host != "v1" {
			return "", "", 0, errors.New("invalid Remote Bridge pairing code")
		}
		remoteAddress = parsed.Query().Get("remote")
		token = parsed.Query().Get("token")
		lanes = DefaultLanes
		if raw := parsed.Query().Get("lanes"); raw != "" {
			lanes, err = strconv.Atoi(raw)
		}
	}
	if _, _, splitErr := net.SplitHostPort(remoteAddress); splitErr != nil || len(token) < 32 {
		return "", "", 0, errors.New("invalid Remote Bridge pairing information")
	}
	if err != nil || lanes < 1 || lanes > 32 {
		return "", "", 0, errors.New("invalid Remote Bridge lane count")
	}
	return remoteAddress, token, lanes, nil
}

func proof(token, parts string) string {
	mac := hmac.New(sha256.New, []byte(token))
	_, _ = mac.Write([]byte(parts))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func validProof(token, parts, got string) bool {
	expected, err1 := base64.RawURLEncoding.DecodeString(proof(token, parts))
	actual, err2 := base64.RawURLEncoding.DecodeString(got)
	return err1 == nil && err2 == nil && hmac.Equal(expected, actual)
}

func stripHopHeaders(header http.Header) http.Header {
	result := header.Clone()
	for _, name := range []string{"Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization", "Te", "Trailer", "Transfer-Encoding", "Upgrade"} {
		result.Del(name)
	}
	for _, name := range header.Values("Connection") {
		for _, token := range strings.Split(name, ",") {
			result.Del(strings.TrimSpace(token))
		}
	}
	return result
}

func targetRequest(target *url.URL, meta requestMeta, body io.ReadCloser) (*http.Request, error) {
	relative, err := url.Parse(meta.Path)
	if err != nil || !strings.HasPrefix(meta.Path, "/") {
		return nil, errors.New("invalid bridged request path")
	}
	requestURL := target.ResolveReference(relative)
	request, err := http.NewRequest(meta.Method, requestURL.String(), body)
	if err != nil {
		return nil, err
	}
	request.Header = stripHopHeaders(meta.Header)
	request.Host = target.Host
	request.ContentLength = meta.ContentLength
	if request.Header.Get("Origin") != "" && isLocalTarget(target) {
		request.Header.Set("Origin", target.Scheme+"://"+target.Host)
	}
	return request, nil
}

func targetWebSocketRequest(target *url.URL, meta requestMeta) (*http.Request, error) {
	relative, err := url.Parse(meta.Path)
	if err != nil || !strings.HasPrefix(meta.Path, "/") {
		return nil, errors.New("invalid bridged WebSocket path")
	}
	request, err := http.NewRequest(meta.Method, target.ResolveReference(relative).String(), http.NoBody)
	if err != nil {
		return nil, err
	}
	request.Header = meta.Header.Clone()
	request.Host = target.Host
	if request.Header.Get("Origin") != "" && isLocalTarget(target) {
		request.Header.Set("Origin", target.Scheme+"://"+target.Host)
	}
	return request, nil
}

func isLocalTarget(target *url.URL) bool {
	host := target.Hostname()
	return host == "127.0.0.1" || strings.EqualFold(host, "localhost")
}

func buffered(conn net.Conn) net.Conn {
	return &bufferedConn{Conn: conn, reader: bufio.NewReader(conn)}
}

type bufferedConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c *bufferedConn) Read(data []byte) (int, error) { return c.reader.Read(data) }
