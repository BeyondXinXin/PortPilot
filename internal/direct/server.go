package direct

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"
)

type Server struct {
	httpServer *http.Server
	listener   net.Listener
	mu         sync.RWMutex
	serveErr   error
}

func Start(ip net.IP, port int, targetAddress string) (*Server, error) {
	target, err := url.Parse(targetAddress)
	if err != nil {
		return nil, err
	}
	network := "tcp6"
	if ip.To4() != nil {
		network = "tcp4"
		ip = ip.To4()
	}
	listenAddress := net.JoinHostPort(ip.String(), fmt.Sprintf("%d", port))
	listener, err := net.Listen(network, listenAddress)
	if err != nil {
		return nil, fmt.Errorf("监听 Direct 地址 %s 失败: %w", listenAddress, err)
	}
	proxy := newReverseProxy(target)
	server := &http.Server{
		Handler:           proxy,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	instance := &Server{httpServer: server, listener: listener}
	go func() {
		serveErr := server.Serve(listener)
		if errors.Is(serveErr, http.ErrServerClosed) {
			serveErr = nil
		}
		instance.mu.Lock()
		instance.serveErr = serveErr
		instance.mu.Unlock()
	}()
	return instance, nil
}

func newReverseProxy(target *url.URL) *httputil.ReverseProxy {
	proxy := httputil.NewSingleHostReverseProxy(target)
	if isLocalTarget(target) {
		director := proxy.Director
		targetOrigin := target.Scheme + "://" + target.Host
		proxy.Director = func(request *http.Request) {
			director(request)
			request.Host = target.Host
			if request.Header.Get("Origin") != "" {
				request.Header.Set("Origin", targetOrigin)
			}
		}
	}
	proxy.ErrorHandler = func(writer http.ResponseWriter, request *http.Request, proxyErr error) {
		http.Error(writer, "PortPilot target unavailable", http.StatusBadGateway)
	}
	return proxy
}

func isLocalTarget(target *url.URL) bool {
	host := target.Hostname()
	return host == "127.0.0.1" || strings.EqualFold(host, "localhost")
}

func (s *Server) Healthy() error {
	if s == nil {
		return errors.New("Direct listener is not running")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.serveErr
}

func (s *Server) Stop(ctx context.Context) error {
	if s == nil || s.httpServer == nil {
		return nil
	}
	if err := s.httpServer.Shutdown(ctx); err != nil {
		if closeErr := s.httpServer.Close(); closeErr != nil {
			return fmt.Errorf("停止 Direct 监听失败: %v; 强制关闭失败: %w", err, closeErr)
		}
	}
	return nil
}
