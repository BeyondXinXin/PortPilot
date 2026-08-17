package bridge

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"testing"
	"time"
)

func TestHTTPBridgeAcrossMultipleLanes(t *testing.T) {
	targetListener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	target := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/post" {
			body, _ := io.ReadAll(r.Body)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(append([]byte("received:"), body...))
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(bytes.Repeat([]byte("0123456789abcdef"), 80*1024))
	})}
	go target.Serve(targetListener)
	defer target.Close()

	remoteListener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	remoteAddress := remoteListener.Addr().String()
	_ = remoteListener.Close()
	token := NewPairToken()
	server, err := StartServer(ServerConfig{ListenAddress: remoteAddress, TargetURL: "http://" + targetListener.Addr().String(), PairToken: token, LaneCount: 4, ChunkSize: 16 * 1024})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Stop(context.Background())

	localListener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	localAddress := localListener.Addr().String()
	_ = localListener.Close()
	client, err := StartClient(ClientConfig{ListenAddress: localAddress, RemoteAddress: remoteAddress, PairToken: token, LaneCount: 4, ChunkSize: 16 * 1024})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Stop(context.Background())

	httpClient := &http.Client{Timeout: 15 * time.Second}
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if client.Connected() && client.ActiveLanes() == 4 {
			break
		}
	}
	if !client.Connected() || client.ActiveLanes() != 4 {
		t.Fatalf("client did not establish data lanes: connected=%v lanes=%d", client.Connected(), client.ActiveLanes())
	}
	var response *http.Response
	response, err = httpClient.Get("http://" + localAddress + "/large")
	if err != nil || response == nil {
		client.mu.Lock()
		laneCount, control := len(client.lanes), client.control != nil
		clientStreams := ""
		for _, stream := range client.streams {
			stream.mu.Lock()
			clientStreams = fmt.Sprintf("next=%d end=%v pending=%d attached=%v err=%v", stream.next, stream.end, len(stream.pending), stream.attached, stream.err)
			stream.mu.Unlock()
		}
		client.mu.Unlock()
		server.mu.Lock()
		sessionCount := len(server.sessions)
		streamCount, streamState := 0, ""
		for _, session := range server.sessions {
			session.mu.Lock()
			streamCount = len(session.streams)
			for _, stream := range session.streams {
				stream.mu.Lock()
				streamState = fmt.Sprintf("nextRequest=%d end=%v unacked=%d", stream.nextRequest, stream.requestEnd, len(stream.unacked))
				stream.mu.Unlock()
			}
			session.mu.Unlock()
		}
		server.mu.Unlock()
		t.Logf("bridge state: control=%v lanes=%d clientStream=%s sessions=%d streams=%d %s clientHealth=%v serverHealth=%v", control, laneCount, clientStreams, sessionCount, streamCount, streamState, client.Healthy(), server.Healthy())
		t.Fatalf("GET through bridge: %v", err)
	}
	body, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := len(body), 16*80*1024; got != want {
		t.Fatalf("GET body length = %d, want %d", got, want)
	}

	response, err = httpClient.Post("http://"+localAddress+"/post", "text/plain", bytes.NewBufferString("through-the-bridge"))
	if err != nil {
		t.Fatalf("POST through bridge: %v", err)
	}
	body, _ = io.ReadAll(response.Body)
	_ = response.Body.Close()
	if string(body) != "received:through-the-bridge" {
		t.Fatalf("POST body = %q", body)
	}
}

func TestPairingCodeRoundTrip(t *testing.T) {
	code := PairingCode("100.1.2.3:39090", "12345678901234567890123456789012", 8)
	remote, token, lanes, err := ParsePairingCode(code)
	if err != nil {
		t.Fatal(err)
	}
	if remote != "100.1.2.3:39090" || token != "12345678901234567890123456789012" || lanes != 8 {
		t.Fatalf("unexpected pairing code: %s %d", remote, lanes)
	}
}

func TestWebSocketRawTunnel(t *testing.T) {
	targetListener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	target := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Upgrade") != "websocket" {
			http.Error(w, "upgrade required", http.StatusBadRequest)
			return
		}
		conn, rw, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		_, _ = rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\n")
		_ = rw.Flush()
		_, _ = io.Copy(conn, conn)
		_ = conn.Close()
	})}
	go target.Serve(targetListener)
	defer target.Close()
	remotePort := freeTCPAddress(t)
	localPort := freeTCPAddress(t)
	token := NewPairToken()
	server, err := StartServer(ServerConfig{ListenAddress: remotePort, TargetURL: "http://" + targetListener.Addr().String(), PairToken: token})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Stop(context.Background())
	client, err := StartClient(ClientConfig{ListenAddress: localPort, RemoteAddress: remotePort, PairToken: token})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Stop(context.Background())
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline) && (!client.Connected() || client.ActiveLanes() == 0); time.Sleep(20 * time.Millisecond) {
	}
	conn, err := net.Dial("tcp", localPort)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_, _ = io.WriteString(conn, "GET /events HTTP/1.1\r\nHost: 127.0.0.1\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Key: test\r\n\r\n")
	response, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("status %d", response.StatusCode)
	}
	_, _ = conn.Write([]byte("ping"))
	buffer := make([]byte, 4)
	if _, err := io.ReadFull(conn, buffer); err != nil || string(buffer) != "ping" {
		t.Fatalf("echo = %q, %v", buffer, err)
	}
}

func freeTCPAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	_ = listener.Close()
	return address
}
