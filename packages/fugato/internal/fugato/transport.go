package fugato

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"
)

const (
	controlRequestTimeout = 6 * time.Minute
	requestBodyTimeout    = 10 * time.Second
	streamWriteTimeout    = 15 * time.Second
	shutdownTimeout       = 10 * time.Second
	// LogStatusTrailer is mandatory on successfully opened CLI log streams.
	// At EOF, "ok" means completion and "error" means backend failure.
	LogStatusTrailer = "Fugato-Log-Status"
)

func previewTransport() *http.Transport {
	return &http.Transport{
		Proxy:                 nil,
		DialContext:           (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ResponseHeaderTimeout: 30 * time.Second,
		TLSHandshakeTimeout:   5 * time.Second,
		ExpectContinueTimeout: time.Second,
		IdleConnTimeout:       time.Minute,
		MaxIdleConns:          64, MaxIdleConnsPerHost: 4, MaxConnsPerHost: 16,
	}
}

// deadlineWriter resets only the write phase; quiet streams have no total lifetime
// limit. Unwrap preserves ResponseController support, including Hijack.
type deadlineWriter struct {
	http.ResponseWriter
	timeout time.Duration
}

func (w *deadlineWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *deadlineWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	conn, rw, err := http.NewResponseController(w.ResponseWriter).Hijack()
	if err == nil {
		err = conn.SetDeadline(time.Time{})
		if err != nil {
			_ = conn.Close()
		}
	}
	return conn, rw, err
}
func (w *deadlineWriter) Write(p []byte) (int, error) {
	_ = http.NewResponseController(w.ResponseWriter).SetWriteDeadline(time.Now().Add(w.timeout))
	return w.ResponseWriter.Write(p)
}
func (w *deadlineWriter) FlushError() error {
	c := http.NewResponseController(w.ResponseWriter)
	_ = c.SetWriteDeadline(time.Now().Add(w.timeout))
	return c.Flush()
}

func boundedControl(next http.Handler) http.Handler {
	return boundedControlWithBodyTimeout(next, requestBodyTimeout)
}

func boundedControlWithBodyTimeout(next http.Handler, bodyTimeout time.Duration) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil && r.Body != http.NoBody && r.ContentLength != 0 {
			_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(bodyTimeout))
		}
		// Only follow logs are intentionally unbounded. All other control operations
		// include body reads, dispatch, backend work, and response encoding in a budget.
		if !(r.Method == http.MethodGet && r.URL.Query().Get("follow") == "1" && len(r.URL.Path) > 5 && r.URL.Path[len(r.URL.Path)-5:] == "/logs") {
			ctx, cancel := context.WithTimeout(r.Context(), controlRequestTimeout)
			defer cancel()
			r = r.WithContext(ctx)
		}
		next.ServeHTTP(&deadlineWriter{w, streamWriteTimeout}, r)
	})
}

// connectionSet owns every accepted connection, including those net/http hands
// to an upgraded protocol. Closing a hijacked connection terminates both proxy
// copy loops; net/http's Shutdown alone does not own those connections.
type connectionSet struct {
	mu     sync.Mutex
	conns  map[*ownedConn]struct{}
	closed bool
}
type ownedConn struct {
	net.Conn
	owner *connectionSet
	once  sync.Once
}

func (c *ownedConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(func() { c.owner.mu.Lock(); delete(c.owner.conns, c); c.owner.mu.Unlock() })
	return err
}

type ownedListener struct {
	net.Listener
	owner *connectionSet
}

func (l ownedListener) Accept() (net.Conn, error) {
	for {
		c, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		wrapped := &ownedConn{Conn: c, owner: l.owner}
		l.owner.mu.Lock()
		if l.owner.closed {
			l.owner.mu.Unlock()
			_ = c.Close()
			return nil, net.ErrClosed
		}
		if len(l.owner.conns) >= 128 {
			l.owner.mu.Unlock()
			_ = c.Close()
			continue
		}
		if l.owner.conns == nil {
			l.owner.conns = make(map[*ownedConn]struct{})
		}
		l.owner.conns[wrapped] = struct{}{}
		l.owner.mu.Unlock()
		return wrapped, nil
	}
}
func (s *connectionSet) closeAll() {
	s.mu.Lock()
	s.closed = true
	conns := make([]*ownedConn, 0, len(s.conns))
	for c := range s.conns {
		conns = append(conns, c)
	}
	s.mu.Unlock()
	for _, c := range conns {
		_ = c.Close()
	}
}

// stop prevents new WaitGroup additions before the shutdown owner calls Wait.
type requestSet struct {
	mu      sync.Mutex
	stopped bool
	active  int
	wg      sync.WaitGroup
}

func (s *requestSet) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		if s.stopped || s.active >= 64 {
			s.mu.Unlock()
			_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(streamWriteTimeout))
			http.Error(w, "daemon stopping or busy", http.StatusServiceUnavailable)
			return
		}
		s.active++
		s.wg.Add(1)
		s.mu.Unlock()
		defer func() { s.mu.Lock(); s.active--; s.mu.Unlock(); s.wg.Done() }()
		next.ServeHTTP(w, r)
	})
}
func (s *requestSet) stop() { s.mu.Lock(); s.stopped = true; s.mu.Unlock() }

// finishShutdown transfers cleanup to one join owner if the caller's shutdown
// budget expires. Resources remain alive and the instance lock remains held.
func finishShutdown(ctx context.Context, done <-chan struct{}, cleanup func()) error {
	cleaned := make(chan struct{})
	go func() { <-done; cleanup(); close(cleaned) }()
	select {
	case <-cleaned:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("daemon shutdown exceeded deadline; cleanup awaits workers: %w", ctx.Err())
	}
}
