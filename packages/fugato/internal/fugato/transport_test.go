package fugato

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestShutdownRetainsResourcesUntilWorkersJoin(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	done, cleaned := make(chan struct{}), make(chan struct{})
	err := finishShutdown(ctx, done, func() { close(cleaned) })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("shutdown: %v", err)
	}
	select {
	case <-cleaned:
		t.Fatal("resources released while worker active")
	default:
	}
	close(done)
	select {
	case <-cleaned:
	case <-time.After(time.Second):
		t.Fatal("worker exited without cleanup")
	}
}
func TestRequestAdmissionRejectsWithoutQueue(t *testing.T) {
	requests := &requestSet{}
	entered, release := make(chan struct{}, 64), make(chan struct{})
	handler := requests.wrap(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { entered <- struct{}{}; <-release }))
	var wg sync.WaitGroup
	for range 64 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil))
		}()
	}
	for range 64 {
		<-entered
	}
	// Preview saturation must not consume the independent control admission budget.
	controlRequests := &requestSet{}
	controlResponse := httptest.NewRecorder()
	controlRequests.wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })).ServeHTTP(controlResponse, httptest.NewRequest("GET", "/health", nil))
	if controlResponse.Code != http.StatusOK {
		t.Errorf("control unavailable while preview saturated: %d", controlResponse.Code)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest("GET", "/", nil))
	if response.Code != http.StatusServiceUnavailable {
		t.Errorf("overload status %d", response.Code)
	}
	close(release)
	wg.Wait()
}
func TestStreamWriteDeadlineBoundsBlockedConsumer(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()
	writer := &deadlineWriter{ResponseWriter: &pipeResponse{conn: server}, timeout: 20 * time.Millisecond}
	_, err := writer.Write([]byte("blocked"))
	var timeout net.Error
	if !errors.As(err, &timeout) || !timeout.Timeout() {
		t.Fatalf("write: %v", err)
	}
}

type pipeResponse struct{ conn net.Conn }

func (p *pipeResponse) Header() http.Header                { return make(http.Header) }
func (p *pipeResponse) WriteHeader(int)                    {}
func (p *pipeResponse) Write(b []byte) (int, error)        { return p.conn.Write(b) }
func (p *pipeResponse) SetWriteDeadline(t time.Time) error { return p.conn.SetWriteDeadline(t) }

func TestControlBodyReadDeadlineAndCancellation(t *testing.T) {
	entered := make(chan struct{})
	ctx, cancel := context.WithCancel(t.Context())
	server := httptest.NewUnstartedServer(boundedControl(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-r.Context().Done()
	})))
	server.Config.BaseContext = func(net.Listener) context.Context { return ctx }
	server.Start()
	defer server.Close()
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		res, err := server.Client().Get(server.URL)
		if err == nil {
			_, _ = io.Copy(io.Discard, res.Body)
			res.Body.Close()
		}
	}()
	<-entered
	cancel()
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("handler ignored root cancellation")
	}
}

func TestQuietFollowHasNoBodyReadDeadline(t *testing.T) {
	handler := boundedControlWithBodyTimeout(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := http.NewResponseController(w).Flush(); err != nil {
			return
		}
		select {
		case <-time.After(60 * time.Millisecond):
			_, _ = io.WriteString(w, "still following")
		case <-r.Context().Done():
			t.Error("quiet follow was cancelled by body deadline")
		}
	}), 10*time.Millisecond)
	server := httptest.NewUnstartedServer(handler)
	server.Config.ReadTimeout = 10 * time.Millisecond
	server.Start()
	defer server.Close()
	response, err := server.Client().Get(server.URL + "/v1/runtimes/1234567890abcdef/logs?follow=1")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil || string(body) != "still following" {
		t.Fatalf("quiet follow: %q %v", body, err)
	}
}
func TestPreviewResponseHeadersHaveDeadline(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer upstream.Close()
	transport := previewTransport()
	defer transport.CloseIdleConnections()
	transport.ResponseHeaderTimeout = 20 * time.Millisecond
	request, _ := http.NewRequestWithContext(t.Context(), "GET", upstream.URL, nil)
	_, err := transport.RoundTrip(request)
	var timeout net.Error
	if !errors.As(err, &timeout) || !timeout.Timeout() {
		t.Fatalf("header wait: %v", err)
	}
}
func TestControlRejectsStalledBodyRead(t *testing.T) {
	server := httptest.NewServer(boundedControlWithBodyTimeout(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, err := io.ReadAll(r.Body)
		if err == nil {
			http.Error(w, "unexpected complete body", http.StatusInternalServerError)
			return
		}
		http.Error(w, "body timeout", http.StatusRequestTimeout)
	}), 20*time.Millisecond))
	defer server.Close()
	conn, err := net.Dial("tcp", server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(time.Second))
	_, err = io.WriteString(conn, "POST / HTTP/1.1\r\nHost: localhost\r\nContent-Length: 100\r\n\r\nx")
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusRequestTimeout {
		t.Fatalf("status %d", response.StatusCode)
	}
}

func TestLogWriteFailureCancelsProducer(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	writer := &flushWriter{ResponseWriter: &deadlineWriter{ResponseWriter: &pipeResponse{conn: server}, timeout: 20 * time.Millisecond}, cancel: cancel}
	if _, err := writer.Write([]byte("blocked")); err == nil {
		t.Fatal("blocked write succeeded")
	}
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatal("failed stream did not cancel producer")
	}
}
