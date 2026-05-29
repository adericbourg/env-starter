package openfile

import (
	"testing"
)

func TestCommand_ofDarwin_returnsOpen(t *testing.T) {
	// Given / When
	name, args := command("darwin", "/tmp/test.log")

	// Then
	if name != "open" {
		t.Errorf("expected 'open' for darwin, got %q", name)
	}
	if len(args) != 1 || args[0] != "/tmp/test.log" {
		t.Errorf("expected args ['/tmp/test.log'], got %v", args)
	}
}

func TestCommand_ofWindows_returnsCmdStart(t *testing.T) {
	// Given / When
	name, args := command("windows", "C:\\Logs\\test.log")

	// Then
	if name != "cmd" {
		t.Errorf("expected 'cmd' for windows, got %q", name)
	}
	// cmd /c start "" <path> — the empty title arg is required
	want := []string{"/c", "start", "", "C:\\Logs\\test.log"}
	if len(args) != len(want) {
		t.Fatalf("expected args %v, got %v", want, args)
	}
	for i, a := range want {
		if args[i] != a {
			t.Errorf("args[%d]: expected %q, got %q", i, a, args[i])
		}
	}
}

func TestCommand_ofLinux_returnsXdgOpen(t *testing.T) {
	// Given / When
	name, args := command("linux", "/tmp/test.log")

	// Then
	if name != "xdg-open" {
		t.Errorf("expected 'xdg-open' for linux, got %q", name)
	}
	if len(args) != 1 || args[0] != "/tmp/test.log" {
		t.Errorf("expected args ['/tmp/test.log'], got %v", args)
	}
}

func TestCommand_ofUnknownOS_returnsXdgOpen(t *testing.T) {
	// Given / When
	name, args := command("freebsd", "/tmp/test.log")

	// Then
	if name != "xdg-open" {
		t.Errorf("expected 'xdg-open' for unknown OS, got %q", name)
	}
	if len(args) != 1 || args[0] != "/tmp/test.log" {
		t.Errorf("expected args ['/tmp/test.log'], got %v", args)
	}
}
