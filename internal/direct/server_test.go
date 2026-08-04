package direct

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
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
