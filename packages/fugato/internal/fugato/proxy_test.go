package fugato

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestProxyRoutesUpgradeToOwnedRuntime(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/socket" || !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			http.Error(w, "bad upgrade", http.StatusBadRequest)
			return
		}
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		_, _ = io.WriteString(conn, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n")
		_, _ = io.Copy(conn, conn)
	}))
	defer upstream.Close()
	state, err := OpenStore(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	_, portText, _ := net.SplitHostPort(listener.Addr().String())
	var port int
	if _, err := fmt.Sscan(portText, &port); err != nil {
		t.Fatal(err)
	}
	const id = "1234567890abcdef"
	runtime := Runtime{ID: id, ProjectDir: "/tmp/example", Worktree: "/tmp/example", Repository: "/tmp/repo", ComposeProject: "fugato-" + id, Backend: "compose", Desired: "running", Observed: "running"}
	if err := state.Save(context.Background(), &runtime); err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: proxyHandler(NewManager(state, &fakeBackend{endpoint: strings.TrimPrefix(upstream.URL, "http://")}, port), port)}
	defer server.Close()
	connections := &connectionSet{}
	go func() { _ = server.Serve(ownedListener{listener, connections}) }()
	conn, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	request := fmt.Sprintf("GET /socket HTTP/1.1\r\nHost: site.%s.localhost:%d\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\n", id, port)
	if _, err := io.WriteString(conn, request); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(conn)
	line, err := reader.ReadString('\n')
	if err != nil || !strings.Contains(line, "101 Switching Protocols") {
		t.Fatalf("upgrade response: %q, %v", line, err)
	}
	for {
		line, err = reader.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if line == "\r\n" {
			break
		}
	}
	if _, err := io.WriteString(conn, "echo-me"); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len("echo-me"))
	if _, err := io.ReadFull(reader, got); err != nil || string(got) != "echo-me" {
		t.Fatalf("upgrade tunnel: %q, %v", got, err)
	}
	connections.closeAll()
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := reader.ReadByte(); err == nil {
		t.Fatal("upgrade connection survived shutdown")
	}
}
