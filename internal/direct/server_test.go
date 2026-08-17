package direct

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

func TestIPv6ReverseProxy(t *testing.T) {
	if listener, err := net.Listen("tcp6", "[::1]:0"); err != nil {
		t.Skip("IPv6 loopback is unavailable")
	} else {
		_ = listener.Close()
	}
	targetListener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := targetListener.Addr().(*net.TCPAddr).Port
	targetServer := &http.Server{Handler: http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = writer.Write([]byte("direct"))
	})}
	go targetServer.Serve(targetListener)
	defer targetServer.Close()

	directServer, err := Start(net.ParseIP("::1"), port, fmt.Sprintf("http://127.0.0.1:%d", port))
	if err != nil {
		t.Skipf("platform does not allow IPv4 and IPv6 listeners on the same port: %v", err)
	}
	defer directServer.Stop(context.Background())
	client := &http.Client{Timeout: 2 * time.Second}
	response, err := client.Get(fmt.Sprintf("http://[::1]:%d", port))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if string(body) != "direct" {
		t.Fatalf("unexpected body %q", body)
	}
}

func TestLocalTargetRewritesHostAndOrigin(t *testing.T) {
	var targetAddress string
	target := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if got, want := request.Host, targetAddress; got != want {
			t.Errorf("Host = %q, want %q", got, want)
		}
		if got, want := request.Header.Get("Origin"), "http://"+targetAddress; got != want {
			t.Errorf("Origin = %q, want %q", got, want)
		}
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer target.Close()

	targetURL, err := url.Parse(target.URL)
	if err != nil {
		t.Fatal(err)
	}
	targetAddress = targetURL.Host
	proxy := httptest.NewServer(newReverseProxy(targetURL))
	defer proxy.Close()

	request, err := http.NewRequest(http.MethodGet, proxy.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Host = "100.64.0.1:8088"
	request.Header.Set("Origin", "http://100.64.0.1:8088")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", response.StatusCode, http.StatusNoContent)
	}
}

func TestLocalTargetWebSocketRewritesHostAndOrigin(t *testing.T) {
	targetListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer targetListener.Close()
	targetPort := targetListener.Addr().(*net.TCPAddr).Port
	targetServer := &http.Server{Handler: http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if got, want := request.Host, fmt.Sprintf("127.0.0.1:%d", targetPort); got != want {
			t.Errorf("Host = %q, want %q", got, want)
		}
		if got, want := request.Header.Get("Origin"), fmt.Sprintf("http://127.0.0.1:%d", targetPort); got != want {
			t.Errorf("Origin = %q, want %q", got, want)
		}
		hijacker, ok := writer.(http.Hijacker)
		if !ok {
			t.Fatal("response writer does not support hijacking")
		}
		connection, _, err := hijacker.Hijack()
		if err != nil {
			t.Fatal(err)
		}
		defer connection.Close()
		_, _ = connection.Write([]byte("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\n"))
	})}
	go targetServer.Serve(targetListener)
	defer targetServer.Close()

	targetURL, err := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", targetPort))
	if err != nil {
		t.Fatal(err)
	}
	proxy := httptest.NewServer(newReverseProxy(targetURL))
	defer proxy.Close()
	proxyURL, err := url.Parse(proxy.URL)
	if err != nil {
		t.Fatal(err)
	}
	connection, err := net.DialTimeout("tcp", proxyURL.Host, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	_, err = fmt.Fprintf(connection, "GET /socket HTTP/1.1\r\nHost: 100.64.0.1:8088\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nOrigin: http://100.64.0.1:8088\r\n\r\n")
	if err != nil {
		t.Fatal(err)
	}
	_ = connection.SetReadDeadline(time.Now().Add(2 * time.Second))
	response, err := http.ReadResponse(bufio.NewReader(connection), &http.Request{Method: http.MethodGet})
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("status = %d, want %d", response.StatusCode, http.StatusSwitchingProtocols)
	}
}

func TestRemoteTargetPreservesHostAndOrigin(t *testing.T) {
	target, err := url.Parse("http://example.test:3081")
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "http://100.64.0.1:8088", nil)
	request.Host = "100.64.0.1:8088"
	request.Header.Set("Origin", "http://100.64.0.1:8088")
	proxy := newReverseProxy(target)
	proxy.Director(request)
	if got, want := request.Host, "100.64.0.1:8088"; got != want {
		t.Errorf("Host = %q, want %q", got, want)
	}
	if got, want := request.Header.Get("Origin"), "http://100.64.0.1:8088"; got != want {
		t.Errorf("Origin = %q, want %q", got, want)
	}
}

func TestIsLocalTarget(t *testing.T) {
	for _, test := range []struct {
		target string
		want   bool
	}{
		{target: "http://127.0.0.1:3081", want: true},
		{target: "http://localhost:3081", want: true},
		{target: "http://LOCALHOST:3081", want: true},
		{target: "http://[::1]:3081", want: false},
		{target: "http://example.test:3081", want: false},
	} {
		t.Run(test.target, func(t *testing.T) {
			target, err := url.Parse(test.target)
			if err != nil {
				t.Fatal(err)
			}
			if got := isLocalTarget(target); got != test.want {
				t.Errorf("isLocalTarget(%q) = %v, want %v", test.target, got, test.want)
			}
		})
	}
}
