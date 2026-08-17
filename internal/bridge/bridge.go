package bridge

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ServerConfig describes the machine that can reach the target localhost service.
type ServerConfig struct {
	ListenAddress string
	TargetURL     string
	PairToken     string
	LaneCount     int
	ChunkSize     int
}

// ClientConfig describes the local localhost entry point and the remote server.
type ClientConfig struct {
	ListenAddress string
	RemoteAddress string
	PairToken     string
	LaneCount     int
	ChunkSize     int
}

type Server struct {
	config   ServerConfig
	target   *url.URL
	listener net.Listener
	mu       sync.Mutex
	sessions map[string]*serverSession
	closed   atomic.Bool
	serveErr atomic.Value // error
}

func StartServer(config ServerConfig) (*Server, error) {
	if config.LaneCount <= 0 {
		config.LaneCount = DefaultLanes
	}
	if config.ChunkSize <= 0 {
		config.ChunkSize = DefaultChunk
	}
	if config.ChunkSize > maxFrameBytes {
		return nil, errors.New("bridge chunk size exceeds protocol frame limit")
	}
	if config.PairToken == "" {
		return nil, errors.New("bridge pair token is required")
	}
	target, err := url.Parse(config.TargetURL)
	if err != nil || target.Scheme == "" || target.Host == "" {
		return nil, errors.New("bridge target URL must be a complete http:// or https:// URL")
	}
	listener, err := net.Listen("tcp", config.ListenAddress)
	if err != nil {
		return nil, fmt.Errorf("listen Remote Bridge %s: %w", config.ListenAddress, err)
	}
	server := &Server{config: config, target: target, listener: listener, sessions: make(map[string]*serverSession)}
	go server.acceptLoop()
	return server, nil
}

func (s *Server) acceptLoop() {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			if s.closed.Load() {
				return
			}
			s.serveErr.Store(err)
			return
		}
		go s.accept(conn)
	}
}

func (s *Server) accept(conn net.Conn) {
	connection := &frameConn{conn: buffered(conn)}
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	first, err := connection.read()
	_ = conn.SetReadDeadline(time.Time{})
	if err != nil {
		connection.close()
		return
	}
	switch first.typeID {
	case frameHello:
		s.acceptControl(connection, first)
	case frameLaneHello:
		s.acceptLane(connection, first)
	case frameWebSocketHello:
		s.acceptWebSocket(connection, first)
	default:
		connection.close()
	}
}

func (s *Server) acceptWebSocket(connection *frameConn, first frame) {
	var hello webSocketHello
	if decode(first.payload, &hello) != nil || hello.Nonce == "" || !validProof(s.config.PairToken, "ws:"+hello.Nonce, hello.Proof) {
		connection.close()
		return
	}
	request, err := targetWebSocketRequest(s.target, hello.Request)
	if err != nil {
		connection.close()
		return
	}
	response, err := http.DefaultTransport.RoundTrip(request)
	if err != nil {
		connection.close()
		return
	}
	meta := responseMeta{StatusCode: response.StatusCode, Status: response.Status, Header: response.Header.Clone(), ContentLength: response.ContentLength}
	payload, _ := encode(meta)
	if connection.write(frame{typeID: frameResponseHeaders, payload: payload}) != nil {
		_ = response.Body.Close()
		connection.close()
		return
	}
	if response.StatusCode != http.StatusSwitchingProtocols {
		_ = response.Body.Close()
		connection.close()
		return
	}
	raw, ok := response.Body.(io.ReadWriteCloser)
	if !ok {
		connection.close()
		return
	}
	defer raw.Close()
	done := make(chan struct{})
	go func() { _, _ = io.Copy(raw, connection.conn); close(done) }()
	_, _ = io.Copy(connection.conn, raw)
	<-done
	connection.close()
}

func (s *Server) acceptControl(connection *frameConn, first frame) {
	var hello helloMessage
	if decode(first.payload, &hello) != nil || hello.SessionID == "" {
		connection.close()
		return
	}
	nonce := randomID(24)
	payload, _ := encode(nonceMessage{Nonce: nonce})
	if connection.write(frame{typeID: frameHelloAck, payload: payload}) != nil {
		connection.close()
		return
	}
	auth, err := connection.read()
	if err != nil || auth.typeID != frameAuth {
		connection.close()
		return
	}
	var value authMessage
	if decode(auth.payload, &value) != nil || !validProof(s.config.PairToken, "control:"+hello.SessionID+":"+nonce, value.Proof) {
		_ = connection.write(frame{typeID: frameAuthError})
		connection.close()
		return
	}
	if connection.write(frame{typeID: frameAuthOK}) != nil {
		connection.close()
		return
	}
	s.mu.Lock()
	session := s.sessions[hello.SessionID]
	if session == nil {
		session = newServerSession(s, hello.SessionID)
		s.sessions[hello.SessionID] = session
	}
	s.mu.Unlock()
	session.setControl(connection)
	session.controlLoop(connection)
}

func (s *Server) acceptLane(connection *frameConn, first frame) {
	var hello laneMessage
	if decode(first.payload, &hello) != nil || hello.SessionID == "" || hello.LaneID < 0 || !validProof(s.config.PairToken, "lane:"+hello.SessionID+":"+strconv.Itoa(hello.LaneID), hello.Proof) {
		connection.close()
		return
	}
	s.mu.Lock()
	session := s.sessions[hello.SessionID]
	s.mu.Unlock()
	if session == nil {
		connection.close()
		return
	}
	if connection.write(frame{typeID: frameLaneReady}) != nil {
		connection.close()
		return
	}
	session.setLane(hello.LaneID, connection)
	session.laneLoop(hello.LaneID, connection)
}

func (s *Server) Healthy() error {
	if s == nil || s.closed.Load() {
		return errors.New("Remote Bridge server is not running")
	}
	if value := s.serveErr.Load(); value != nil {
		return value.(error)
	}
	return nil
}

func (s *Server) Stop(ctx context.Context) error {
	if s == nil || s.closed.Swap(true) {
		return nil
	}
	err := s.listener.Close()
	s.mu.Lock()
	for _, session := range s.sessions {
		session.close()
	}
	s.sessions = make(map[string]*serverSession)
	s.mu.Unlock()
	return err
}

type serverSession struct {
	server             *Server
	id                 string
	mu                 sync.Mutex
	control            *frameConn
	lanes              map[int]*frameConn
	streams            map[uint64]*serverStream
	pendingRequestData map[uint64][]frame
	pendingRequestEnd  map[uint64]uint64
	closed             bool
}

func newServerSession(server *Server, id string) *serverSession {
	return &serverSession{server: server, id: id, lanes: make(map[int]*frameConn), streams: make(map[uint64]*serverStream), pendingRequestData: make(map[uint64][]frame), pendingRequestEnd: make(map[uint64]uint64)}
}

func (s *serverSession) setControl(connection *frameConn) {
	s.mu.Lock()
	old := s.control
	s.control = connection
	s.mu.Unlock()
	if old != nil && old != connection {
		old.close()
	}
}

func (s *serverSession) setLane(id int, connection *frameConn) {
	s.mu.Lock()
	old := s.lanes[id]
	s.lanes[id] = connection
	s.mu.Unlock()
	if old != nil && old != connection {
		old.close()
	}
}

func (s *serverSession) controlLoop(connection *frameConn) {
	defer s.dropControl(connection)
	for {
		f, err := connection.read()
		if err != nil {
			return
		}
		switch f.typeID {
		case frameRequestHeaders:
			s.handleRequestHeaders(f)
		case frameRequestEnd:
			s.handleRequestEnd(f)
		case frameACK:
			if stream := s.stream(f.streamID); stream != nil {
				stream.ack(f.seq)
			}
		case framePing:
			_ = connection.write(frame{typeID: framePong})
		case frameReset:
			if stream := s.stream(f.streamID); stream != nil {
				stream.close()
			}
		}
	}
}

func (s *serverSession) laneLoop(id int, connection *frameConn) {
	defer s.dropLane(id, connection)
	for {
		f, err := connection.read()
		if err != nil {
			return
		}
		if f.typeID == frameRequestData {
			s.handleRequestData(f)
		}
	}
}

func (s *serverSession) dropControl(connection *frameConn) {
	s.mu.Lock()
	if s.control == connection {
		s.control = nil
	}
	s.mu.Unlock()
}

func (s *serverSession) dropLane(id int, connection *frameConn) {
	s.mu.Lock()
	if s.lanes[id] == connection {
		delete(s.lanes, id)
	}
	s.mu.Unlock()
	go s.retransmitAll()
}

func (s *serverSession) stream(id uint64) *serverStream {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.streams[id]
}

func (s *serverSession) handleRequestHeaders(f frame) {
	var meta requestMeta
	if decode(f.payload, &meta) != nil {
		return
	}
	s.mu.Lock()
	if _, exists := s.streams[f.streamID]; exists || s.closed {
		s.mu.Unlock()
		return
	}
	stream := newServerStream(s, f.streamID, meta)
	s.streams[f.streamID] = stream
	pendingData := s.pendingRequestData[f.streamID]
	delete(s.pendingRequestData, f.streamID)
	pendingEnd, hasEnd := s.pendingRequestEnd[f.streamID]
	delete(s.pendingRequestEnd, f.streamID)
	s.mu.Unlock()
	for _, pending := range pendingData {
		stream.acceptRequestData(pending.seq, pending.payload)
	}
	if hasEnd {
		stream.endRequest(pendingEnd)
	}
}

func (s *serverSession) handleRequestData(f frame) {
	s.mu.Lock()
	stream := s.streams[f.streamID]
	if stream == nil {
		s.pendingRequestData[f.streamID] = append(s.pendingRequestData[f.streamID], frame{streamID: f.streamID, seq: f.seq, payload: append([]byte(nil), f.payload...)})
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()
	stream.acceptRequestData(f.seq, f.payload)
}

func (s *serverSession) handleRequestEnd(f frame) {
	s.mu.Lock()
	stream := s.streams[f.streamID]
	if stream == nil {
		s.pendingRequestEnd[f.streamID] = f.seq
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()
	stream.endRequest(f.seq)
}

func (s *serverSession) sendControl(f frame) error {
	s.mu.Lock()
	control := s.control
	s.mu.Unlock()
	if control == nil {
		return errors.New("bridge control connection is unavailable")
	}
	return control.write(f)
}

func (s *serverSession) sendResponse(stream *serverStream, f frame) error {
	deadline := time.Now().Add(10 * time.Second)
	for attempts := 0; attempts < 3 && time.Now().Before(deadline); {
		s.mu.Lock()
		keys := make([]int, 0, len(s.lanes))
		for id, lane := range s.lanes {
			if lane != nil && !lane.dead.Load() {
				keys = append(keys, id)
			}
		}
		s.mu.Unlock()
		if len(keys) == 0 {
			time.Sleep(50 * time.Millisecond)
			continue
		}
		sort.Ints(keys)
		index := int(f.seq % uint64(len(keys)))
		laneID := keys[index]
		s.mu.Lock()
		lane := s.lanes[laneID]
		s.mu.Unlock()
		if lane != nil && lane.write(f) == nil {
			return nil
		}
		attempts++
		s.dropLane(laneID, lane)
	}
	return errors.New("all Remote Bridge data lanes are unavailable")
}

func (s *serverSession) retransmitAll() {
	s.mu.Lock()
	streams := make([]*serverStream, 0, len(s.streams))
	for _, stream := range s.streams {
		streams = append(streams, stream)
	}
	s.mu.Unlock()
	for _, stream := range streams {
		stream.retransmit()
	}
}

func (s *serverSession) removeStream(id uint64) {
	s.mu.Lock()
	delete(s.streams, id)
	s.mu.Unlock()
}

func (s *serverSession) close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	control := s.control
	lanes := make([]*frameConn, 0, len(s.lanes))
	for _, lane := range s.lanes {
		lanes = append(lanes, lane)
	}
	streams := make([]*serverStream, 0, len(s.streams))
	for _, stream := range s.streams {
		streams = append(streams, stream)
	}
	s.mu.Unlock()
	if control != nil {
		control.close()
	}
	for _, lane := range lanes {
		lane.close()
	}
	for _, stream := range streams {
		stream.close()
	}
}

type serverStream struct {
	session        *serverSession
	id             uint64
	meta           requestMeta
	mu             sync.Mutex
	requestPending map[uint64][]byte
	requestBody    []byte
	nextRequest    uint64
	requestEnd     *uint64
	requestStarted bool
	unacked        map[uint64][]byte
	acked          uint64
	responseEnd    *uint64
	closed         bool
	window         *sync.Cond
	unackedBytes   int
}

func newServerStream(session *serverSession, id uint64, meta requestMeta) *serverStream {
	stream := &serverStream{session: session, id: id, meta: meta, requestPending: make(map[uint64][]byte), unacked: make(map[uint64][]byte)}
	stream.window = sync.NewCond(&stream.mu)
	return stream
}

func (s *serverStream) roundTrip() {
	s.mu.Lock()
	body := append([]byte(nil), s.requestBody...)
	s.mu.Unlock()
	request, err := targetRequest(s.session.server.target, s.meta, io.NopCloser(bytes.NewReader(body)))
	if err != nil {
		s.sendError(err)
		s.close()
		return
	}
	response, err := http.DefaultTransport.RoundTrip(request)
	if err != nil {
		s.sendError(err)
		s.close()
		return
	}
	defer response.Body.Close()
	meta := responseMeta{StatusCode: response.StatusCode, Status: response.Status, Header: stripHopHeaders(response.Header), ContentLength: response.ContentLength, Streaming: stringsEqualFold(response.Header.Get("Content-Type"), "text/event-stream")}
	payload, _ := encode(meta)
	if err := s.session.sendControl(frame{typeID: frameResponseHeaders, streamID: s.id, payload: payload}); err != nil {
		s.close()
		return
	}
	buffer := make([]byte, s.session.server.config.ChunkSize)
	var seq uint64
	for {
		count, readErr := response.Body.Read(buffer)
		if count > 0 {
			chunk := append([]byte(nil), buffer[:count]...)
			if err := s.sendChunk(seq, chunk); err != nil {
				s.close()
				return
			}
			seq++
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			s.sendError(readErr)
			s.close()
			return
		}
	}
	if err := s.session.sendControl(frame{typeID: frameResponseEnd, streamID: s.id, seq: seq}); err != nil {
		s.close()
		return
	}
	s.mu.Lock()
	s.responseEnd = &seq
	finishedWithoutBody := seq == 0
	s.mu.Unlock()
	if finishedWithoutBody {
		s.close()
		return
	}
	time.AfterFunc(30*time.Second, s.close)
}

func stringsEqualFold(contentType, wanted string) bool {
	return len(contentType) >= len(wanted) && strings.EqualFold(contentType[:len(wanted)], wanted)
}

func (s *serverStream) sendChunk(seq uint64, data []byte) error {
	s.mu.Lock()
	for !s.closed && s.unackedBytes+len(data) > 4*1024*1024 {
		s.window.Wait()
	}
	if s.closed {
		s.mu.Unlock()
		return net.ErrClosed
	}
	s.unacked[seq] = data
	s.unackedBytes += len(data)
	s.mu.Unlock()
	if err := s.session.sendResponse(s, frame{typeID: frameResponseData, streamID: s.id, seq: seq, payload: data}); err != nil {
		return err
	}
	return nil
}

func (s *serverStream) acceptRequestData(seq uint64, data []byte) {
	s.mu.Lock()
	if s.closed || seq < s.nextRequest {
		s.mu.Unlock()
		return
	}
	s.requestPending[seq] = append([]byte(nil), data...)
	for {
		chunk, exists := s.requestPending[s.nextRequest]
		if !exists {
			break
		}
		if len(s.requestBody)+len(chunk) > 4*1024*1024 {
			s.mu.Unlock()
			s.sendError(errors.New("Remote Bridge request body exceeds 4 MB window"))
			s.close()
			return
		}
		s.requestBody = append(s.requestBody, chunk...)
		delete(s.requestPending, s.nextRequest)
		s.nextRequest++
	}
	end := s.requestEnd != nil && s.nextRequest >= *s.requestEnd
	start := end && !s.requestStarted
	if start {
		s.requestStarted = true
	}
	s.mu.Unlock()
	if start {
		go s.roundTrip()
	}
}

func (s *serverStream) endRequest(end uint64) {
	s.mu.Lock()
	s.requestEnd = &end
	start := s.nextRequest >= end && !s.requestStarted
	if start {
		s.requestStarted = true
	}
	s.mu.Unlock()
	if start {
		go s.roundTrip()
	}
}

func (s *serverStream) ack(seq uint64) {
	s.mu.Lock()
	for number, data := range s.unacked {
		if number <= seq {
			delete(s.unacked, number)
			s.unackedBytes -= len(data)
		}
	}
	s.acked = seq
	finished := s.responseEnd != nil && seq+1 >= *s.responseEnd && len(s.unacked) == 0
	s.window.Broadcast()
	s.mu.Unlock()
	if finished {
		s.close()
	}
}

func (s *serverStream) retransmit() {
	s.mu.Lock()
	chunks := make([]struct {
		seq  uint64
		data []byte
	}, 0, len(s.unacked))
	for seq, data := range s.unacked {
		chunks = append(chunks, struct {
			seq  uint64
			data []byte
		}{seq, data})
	}
	s.mu.Unlock()
	sort.Slice(chunks, func(i, j int) bool { return chunks[i].seq < chunks[j].seq })
	for _, chunk := range chunks {
		_ = s.session.sendResponse(s, frame{typeID: frameResponseData, streamID: s.id, seq: chunk.seq, payload: chunk.data})
	}
}

func (s *serverStream) sendError(err error) {
	_ = s.session.sendControl(frame{typeID: frameError, streamID: s.id, payload: []byte(err.Error())})
}

func (s *serverStream) close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	s.window.Broadcast()
	s.mu.Unlock()
	s.session.removeStream(s.id)
}

type Client struct {
	config     ClientConfig
	listener   net.Listener
	httpServer *http.Server
	sessionID  string
	mu         sync.Mutex
	control    *frameConn
	lanes      map[int]*frameConn
	streams    map[uint64]*clientStream
	nextID     atomic.Uint64
	closed     chan struct{}
	closeOnce  sync.Once
	serveErr   atomic.Value // error
}

func StartClient(config ClientConfig) (*Client, error) {
	if config.LaneCount <= 0 {
		config.LaneCount = DefaultLanes
	}
	if config.ChunkSize <= 0 {
		config.ChunkSize = DefaultChunk
	}
	if config.ChunkSize > maxFrameBytes {
		return nil, errors.New("bridge chunk size exceeds protocol frame limit")
	}
	if config.PairToken == "" {
		return nil, errors.New("bridge pair token is required")
	}
	if _, _, err := net.SplitHostPort(config.RemoteAddress); err != nil {
		return nil, fmt.Errorf("invalid remote bridge address: %w", err)
	}
	listener, err := net.Listen("tcp", config.ListenAddress)
	if err != nil {
		return nil, fmt.Errorf("listen local Remote Bridge %s: %w", config.ListenAddress, err)
	}
	client := &Client{config: config, listener: listener, sessionID: randomID(16), lanes: make(map[int]*frameConn), streams: make(map[uint64]*clientStream), closed: make(chan struct{})}
	client.httpServer = &http.Server{Handler: http.HandlerFunc(client.serveHTTP), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 90 * time.Second}
	go client.serve()
	go client.controlReconnectLoop()
	for laneID := 0; laneID < config.LaneCount; laneID++ {
		go client.laneReconnectLoop(laneID)
	}
	return client, nil
}

func (c *Client) serve() {
	err := c.httpServer.Serve(c.listener)
	if !errors.Is(err, http.ErrServerClosed) {
		c.serveErr.Store(err)
	}
}

func (c *Client) controlReconnectLoop() {
	for delay := time.Duration(0); ; delay = nextDelay(delay) {
		if delay > 0 {
			if !waitOrClosed(c.closed, delay) {
				return
			}
		}
		connection, err := c.connectControl()
		if err != nil {
			continue
		}
		c.mu.Lock()
		old := c.control
		c.control = connection
		c.mu.Unlock()
		if old != nil {
			old.close()
		}
		delay = 0
		c.controlLoop(connection)
		c.failAll(errors.New("Remote Bridge control connection lost; browser request can be retried"))
	}
}

func (c *Client) connectControl() (*frameConn, error) {
	conn, err := net.DialTimeout("tcp", c.config.RemoteAddress, 5*time.Second)
	if err != nil {
		return nil, err
	}
	connection := &frameConn{conn: buffered(conn)}
	payload, _ := encode(helloMessage{SessionID: c.sessionID})
	if err := connection.write(frame{typeID: frameHello, payload: payload}); err != nil {
		connection.close()
		return nil, err
	}
	ack, err := connection.read()
	if err != nil || ack.typeID != frameHelloAck {
		connection.close()
		return nil, errors.New("Remote Bridge server did not accept control connection")
	}
	var nonce nonceMessage
	if decode(ack.payload, &nonce) != nil {
		connection.close()
		return nil, errors.New("invalid Remote Bridge nonce")
	}
	authPayload, _ := encode(authMessage{Proof: proof(c.config.PairToken, "control:"+c.sessionID+":"+nonce.Nonce)})
	if err := connection.write(frame{typeID: frameAuth, payload: authPayload}); err != nil {
		connection.close()
		return nil, err
	}
	result, err := connection.read()
	if err != nil || result.typeID != frameAuthOK {
		connection.close()
		return nil, errors.New("Remote Bridge pairing authentication failed")
	}
	return connection, nil
}

func (c *Client) controlLoop(connection *frameConn) {
	for {
		f, err := connection.read()
		if err != nil {
			return
		}
		switch f.typeID {
		case frameResponseHeaders:
			var meta responseMeta
			if decode(f.payload, &meta) == nil {
				if stream := c.stream(f.streamID); stream != nil {
					stream.responseHeaders(meta)
				}
			}
		case frameResponseEnd:
			if stream := c.stream(f.streamID); stream != nil {
				stream.responseEnd(f.seq)
			}
		case frameError, frameReset:
			if stream := c.stream(f.streamID); stream != nil {
				stream.fail(errors.New(string(f.payload)))
			}
		case framePing:
			_ = connection.write(frame{typeID: framePong})
		}
	}
}

func (c *Client) laneReconnectLoop(id int) {
	for delay := time.Duration(0); ; delay = nextDelay(delay) {
		if delay > 0 {
			if !waitOrClosed(c.closed, delay) {
				return
			}
		}
		connection, err := c.connectLane(id)
		if err != nil {
			continue
		}
		c.mu.Lock()
		old := c.lanes[id]
		c.lanes[id] = connection
		c.mu.Unlock()
		if old != nil {
			old.close()
		}
		delay = 0
		for {
			f, readErr := connection.read()
			if readErr != nil {
				break
			}
			if f.typeID == frameResponseData {
				if stream := c.stream(f.streamID); stream != nil {
					stream.responseData(f.seq, f.payload)
				}
			}
		}
		c.mu.Lock()
		if c.lanes[id] == connection {
			delete(c.lanes, id)
		}
		c.mu.Unlock()
	}
}

func (c *Client) connectLane(id int) (*frameConn, error) {
	conn, err := net.DialTimeout("tcp", c.config.RemoteAddress, 5*time.Second)
	if err != nil {
		return nil, err
	}
	connection := &frameConn{conn: buffered(conn)}
	payload, _ := encode(laneMessage{SessionID: c.sessionID, LaneID: id, Proof: proof(c.config.PairToken, "lane:"+c.sessionID+":"+strconv.Itoa(id))})
	if err := connection.write(frame{typeID: frameLaneHello, payload: payload}); err != nil {
		connection.close()
		return nil, err
	}
	result, err := connection.read()
	if err != nil || result.typeID != frameLaneReady {
		connection.close()
		return nil, errors.New("Remote Bridge server did not accept data lane")
	}
	return connection, nil
}

func nextDelay(previous time.Duration) time.Duration {
	switch previous {
	case 0:
		return 500 * time.Millisecond
	case 500 * time.Millisecond:
		return time.Second
	case time.Second:
		return 2 * time.Second
	default:
		return 5 * time.Second
	}
}

func waitOrClosed(closed <-chan struct{}, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-closed:
		return false
	case <-timer.C:
		return true
	}
}

func (c *Client) serveHTTP(writer http.ResponseWriter, request *http.Request) {
	if strings.EqualFold(request.Header.Get("Upgrade"), "websocket") {
		c.serveWebSocket(writer, request)
		return
	}
	stream := c.newStream()
	defer c.removeStream(stream.id)
	meta := requestMeta{Method: request.Method, Path: request.URL.RequestURI(), Header: stripHopHeaders(request.Header), ContentLength: request.ContentLength}
	payload, _ := encode(meta)
	if err := c.sendControl(frame{typeID: frameRequestHeaders, streamID: stream.id, payload: payload}); err != nil {
		http.Error(writer, "Remote Bridge is reconnecting", http.StatusBadGateway)
		return
	}
	sequence := uint64(0)
	buffer := make([]byte, c.config.ChunkSize)
	for {
		count, readErr := request.Body.Read(buffer)
		if count > 0 {
			if err := c.sendRequestData(frame{typeID: frameRequestData, streamID: stream.id, seq: sequence, payload: append([]byte(nil), buffer[:count]...)}); err != nil {
				stream.fail(err)
				break
			}
			sequence++
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			stream.fail(readErr)
			break
		}
	}
	if err := c.sendControl(frame{typeID: frameRequestEnd, streamID: stream.id, seq: sequence}); err != nil {
		stream.fail(err)
	}
	if err := stream.waitHeaders(request.Context()); err != nil {
		http.Error(writer, err.Error(), http.StatusBadGateway)
		return
	}
	stream.attach(writer)
	if err := stream.waitDone(request.Context()); err != nil && !errors.Is(err, context.Canceled) {
		return
	}
}

func (c *Client) serveWebSocket(writer http.ResponseWriter, request *http.Request) {
	hijacker, ok := writer.(http.Hijacker)
	if !ok {
		http.Error(writer, "WebSocket is unavailable", http.StatusInternalServerError)
		return
	}
	nonce := randomID(16)
	conn, err := net.DialTimeout("tcp", c.config.RemoteAddress, 5*time.Second)
	if err != nil {
		http.Error(writer, "Remote Bridge is reconnecting", http.StatusBadGateway)
		return
	}
	remote := &frameConn{conn: buffered(conn)}
	payload, _ := encode(webSocketHello{Nonce: nonce, Proof: proof(c.config.PairToken, "ws:"+nonce), Request: requestMeta{Method: request.Method, Path: request.URL.RequestURI(), Header: request.Header.Clone(), ContentLength: request.ContentLength}})
	if err := remote.write(frame{typeID: frameWebSocketHello, payload: payload}); err != nil {
		remote.close()
		http.Error(writer, "Remote Bridge WebSocket failed", http.StatusBadGateway)
		return
	}
	responseFrame, err := remote.read()
	if err != nil || responseFrame.typeID != frameResponseHeaders {
		remote.close()
		http.Error(writer, "Remote Bridge WebSocket failed", http.StatusBadGateway)
		return
	}
	var meta responseMeta
	if decode(responseFrame.payload, &meta) != nil {
		remote.close()
		http.Error(writer, "Remote Bridge WebSocket failed", http.StatusBadGateway)
		return
	}
	clientConn, bufferedWriter, err := hijacker.Hijack()
	if err != nil {
		remote.close()
		return
	}
	response := &http.Response{StatusCode: meta.StatusCode, Status: meta.Status, ProtoMajor: 1, ProtoMinor: 1, Header: meta.Header}
	if err := response.Write(bufferedWriter); err != nil {
		_ = clientConn.Close()
		remote.close()
		return
	}
	if err := bufferedWriter.Flush(); err != nil {
		_ = clientConn.Close()
		remote.close()
		return
	}
	if meta.StatusCode != http.StatusSwitchingProtocols {
		_ = clientConn.Close()
		remote.close()
		return
	}
	done := make(chan struct{})
	go func() { _, _ = io.Copy(remote.conn, clientConn); close(done) }()
	_, _ = io.Copy(clientConn, remote.conn)
	<-done
	_ = clientConn.Close()
	remote.close()
}

func (c *Client) newStream() *clientStream {
	id := c.nextID.Add(1)
	stream := &clientStream{client: c, id: id, headers: make(chan struct{}), done: make(chan struct{}), pending: make(map[uint64][]byte)}
	c.mu.Lock()
	c.streams[id] = stream
	c.mu.Unlock()
	return stream
}

func (c *Client) stream(id uint64) *clientStream {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.streams[id]
}
func (c *Client) removeStream(id uint64) { c.mu.Lock(); delete(c.streams, id); c.mu.Unlock() }

func (c *Client) sendControl(f frame) error {
	c.mu.Lock()
	control := c.control
	c.mu.Unlock()
	if control == nil {
		return errors.New("Remote Bridge server is not connected")
	}
	return control.write(f)
}

func (c *Client) sendRequestData(f frame) error {
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		c.mu.Lock()
		lanes := make([]*frameConn, 0, len(c.lanes))
		for _, lane := range c.lanes {
			if lane != nil && !lane.dead.Load() {
				lanes = append(lanes, lane)
			}
		}
		c.mu.Unlock()
		if len(lanes) > 0 {
			return lanes[int(f.seq%uint64(len(lanes)))].write(f)
		}
		if !waitOrClosed(c.closed, 50*time.Millisecond) {
			return net.ErrClosed
		}
	}
	return errors.New("Remote Bridge data lanes are not connected")
}

func (c *Client) failAll(err error) {
	c.mu.Lock()
	streams := make([]*clientStream, 0, len(c.streams))
	for _, stream := range c.streams {
		streams = append(streams, stream)
	}
	c.mu.Unlock()
	for _, stream := range streams {
		stream.fail(err)
	}
}

func (c *Client) Healthy() error {
	if c == nil {
		return errors.New("Remote Bridge client is not running")
	}
	if value := c.serveErr.Load(); value != nil {
		return value.(error)
	}
	return nil
}

// Connected reports whether the authenticated control connection is live.
func (c *Client) Connected() bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.control != nil && !c.control.dead.Load()
}

// ActiveLanes reports currently connected data lanes.
func (c *Client) ActiveLanes() int {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.lanes)
}

func (c *Client) Stop(ctx context.Context) error {
	if c == nil {
		return nil
	}
	c.closeOnce.Do(func() { close(c.closed) })
	c.mu.Lock()
	control := c.control
	lanes := make([]*frameConn, 0, len(c.lanes))
	for _, lane := range c.lanes {
		lanes = append(lanes, lane)
	}
	c.mu.Unlock()
	if control != nil {
		control.close()
	}
	for _, lane := range lanes {
		lane.close()
	}
	c.failAll(net.ErrClosed)
	return c.httpServer.Shutdown(ctx)
}

type clientStream struct {
	client      *Client
	id          uint64
	mu          sync.Mutex
	headers     chan struct{}
	done        chan struct{}
	headersOnce sync.Once
	doneOnce    sync.Once
	meta        responseMeta
	writer      http.ResponseWriter
	pending     map[uint64][]byte
	next        uint64
	end         *uint64
	err         error
	attached    bool
	ackSeq      uint64
}

func (s *clientStream) responseHeaders(meta responseMeta) {
	s.mu.Lock()
	s.meta = meta
	s.mu.Unlock()
	s.headersOnce.Do(func() { close(s.headers) })
}

func (s *clientStream) waitHeaders(ctx context.Context) error {
	select {
	case <-s.headers:
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.err
	case <-s.done:
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *clientStream) attach(writer http.ResponseWriter) {
	s.mu.Lock()
	if s.err != nil {
		s.mu.Unlock()
		return
	}
	s.writer = writer
	s.attached = true
	for key, values := range s.meta.Header {
		writer.Header()[key] = append([]string(nil), values...)
	}
	writer.WriteHeader(s.meta.StatusCode)
	s.flushLocked()
	s.mu.Unlock()
}

func (s *clientStream) responseData(seq uint64, data []byte) {
	s.mu.Lock()
	if s.err != nil || seq < s.next {
		s.mu.Unlock()
		return
	}
	s.pending[seq] = append([]byte(nil), data...)
	s.flushLocked()
	s.mu.Unlock()
}

func (s *clientStream) responseEnd(end uint64) {
	s.mu.Lock()
	s.end = &end
	s.flushLocked()
	s.finishIfReadyLocked()
	s.mu.Unlock()
}

func (s *clientStream) flushLocked() {
	if !s.attached {
		return
	}
	for {
		chunk, exists := s.pending[s.next]
		if !exists {
			break
		}
		delete(s.pending, s.next)
		if _, err := s.writer.Write(chunk); err != nil {
			s.err = err
			s.doneOnce.Do(func() { close(s.done) })
			return
		}
		s.next++
		if s.next-s.ackSeq >= 8 {
			s.ackSeq = s.next
			go s.client.sendControl(frame{typeID: frameACK, streamID: s.id, seq: s.next - 1})
		}
	}
	s.finishIfReadyLocked()
}

func (s *clientStream) finishIfReadyLocked() {
	if s.end != nil && s.next >= *s.end {
		if s.next > 0 {
			go s.client.sendControl(frame{typeID: frameACK, streamID: s.id, seq: s.next - 1})
		}
		s.doneOnce.Do(func() { close(s.done) })
	}
}

func (s *clientStream) fail(err error) {
	s.mu.Lock()
	if s.err == nil {
		s.err = err
	}
	s.mu.Unlock()
	s.headersOnce.Do(func() { close(s.headers) })
	s.doneOnce.Do(func() { close(s.done) })
}

func (s *clientStream) waitDone(ctx context.Context) error {
	select {
	case <-s.done:
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.err
	case <-ctx.Done():
		return ctx.Err()
	}
}
