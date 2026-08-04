package localserver

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Server struct {
	httpServer *http.Server
	listener   net.Listener
}

func StartStatic(directory string, port int) (*Server, error) {
	listener, err := net.Listen("tcp4", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return nil, err
	}
	server := &http.Server{
		Handler:           http.FileServer(http.Dir(directory)),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	instance := &Server{httpServer: server, listener: listener}
	go func() {
		_ = server.Serve(listener)
	}()
	return instance, nil
}

func (s *Server) Stop(ctx context.Context) error {
	if s == nil || s.httpServer == nil {
		return nil
	}
	if err := s.httpServer.Shutdown(ctx); err != nil {
		if closeErr := s.httpServer.Close(); closeErr != nil {
			return fmt.Errorf("优雅停止失败: %v; 强制关闭失败: %w", err, closeErr)
		}
	}
	return nil
}

func CheckEndpoint(address string, timeout time.Duration) error {
	parsed, err := url.Parse(address)
	if err != nil {
		return err
	}
	host := parsed.Host
	if !strings.Contains(host, ":") {
		if parsed.Scheme == "https" {
			host += ":443"
		} else {
			host += ":80"
		}
	}
	connection, err := net.DialTimeout("tcp", host, timeout)
	if err != nil {
		return fmt.Errorf("本地服务不可访问: %w", err)
	}
	return connection.Close()
}
