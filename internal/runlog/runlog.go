package runlog

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type Event struct {
	Time      time.Time
	ServiceID string
	Message   string
}

type Logger struct {
	Path        string
	file        *os.File
	log         *log.Logger
	mu          sync.Mutex
	subscribers map[chan Event]struct{}
}

func Open(baseDir string) (*Logger, error) {
	dir := filepath.Join(baseDir, "logs")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "PortPilot.log")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return nil, err
	}
	return &Logger{Path: path, file: file, log: log.New(file, "", log.LstdFlags), subscribers: make(map[chan Event]struct{})}, nil
}

func (l *Logger) Servicef(serviceID string, format string, args ...any) {
	if l == nil {
		return
	}
	message := fmt.Sprintf(format, args...)
	l.mu.Lock()
	l.log.Printf("[%s] %s", serviceID, message)
	event := Event{Time: time.Now(), ServiceID: serviceID, Message: message}
	for subscriber := range l.subscribers {
		select {
		case subscriber <- event:
		default:
		}
	}
	l.mu.Unlock()
}

func (l *Logger) Printf(format string, args ...any) {
	l.Servicef("system", format, args...)
}

func (l *Logger) Subscribe() (<-chan Event, func()) {
	channel := make(chan Event, 64)
	l.mu.Lock()
	l.subscribers[channel] = struct{}{}
	l.mu.Unlock()
	return channel, func() {
		l.mu.Lock()
		if _, exists := l.subscribers[channel]; exists {
			delete(l.subscribers, channel)
			close(channel)
		}
		l.mu.Unlock()
	}
}

func (l *Logger) Close() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	for subscriber := range l.subscribers {
		close(subscriber)
		delete(l.subscribers, subscriber)
	}
	if l.file == nil {
		return nil
	}
	return l.file.Close()
}
