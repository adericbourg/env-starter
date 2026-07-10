package daemon

import (
	"net"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestCheckPeerUID(t *testing.T) {
	if err := checkPeerUID(os.Getuid()); err != nil {
		t.Errorf("same uid: want accept, got %v", err)
	}
	if err := checkPeerUID(os.Getuid() + 1); err == nil {
		t.Error("different uid: want reject, got nil")
	}
}

func TestCheckPeerAcceptsSameUser(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "peer.sock")
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	accepted := make(chan net.Conn, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			t.Errorf("accept: %v", err)
			return
		}
		accepted <- conn
	}()

	client, err := net.Dial("unix", socketPath)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close()

	server := <-accepted
	defer server.Close()

	if err := checkPeer(server); err != nil {
		t.Errorf("connection from the same user: want accept, got %v", err)
	}
}

func TestCheckPeerRejectsNonUnixConn(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("checkPeer is a no-op without a peer-credential API")
	}
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()

	if err := checkPeer(a); err == nil {
		t.Error("non-unix connection: want an error, got nil")
	}
}
