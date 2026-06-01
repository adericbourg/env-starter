// Package logbuf captures a command's output: it keeps the last N lines in
// memory for a live TUI view and optionally tees raw bytes to a file.
package logbuf

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"sync"
)

const defaultCapacity = 1000

// Ring is a concurrency-safe ring buffer that retains at most capacity lines.
type Ring struct {
	mu       sync.Mutex
	buf      []string
	capacity int
	// head is the index of the oldest entry; count is the number of valid entries.
	head  int
	count int
}

// NewRing returns a new Ring that retains up to capacity lines.
// If capacity <= 0 it defaults to 1000.
func NewRing(capacity int) *Ring {
	if capacity <= 0 {
		capacity = defaultCapacity
	}
	return &Ring{
		buf:      make([]string, capacity),
		capacity: capacity,
	}
}

// Add appends line, evicting the oldest entry when the buffer is full.
func (r *Ring) Add(line string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.count < r.capacity {
		// Buffer not yet full: write at (head + count) % capacity.
		idx := (r.head + r.count) % r.capacity
		r.buf[idx] = line
		r.count++
	} else {
		// Buffer full: overwrite the oldest slot and advance head.
		r.buf[r.head] = line
		r.head = (r.head + 1) % r.capacity
	}
}

// Lines returns a copy of the retained lines in chronological order.
func (r *Ring) Lines() []string {
	r.mu.Lock()
	defer r.mu.Unlock()

	out := make([]string, r.count)
	for i := 0; i < r.count; i++ {
		out[i] = r.buf[(r.head+i)%r.capacity]
	}
	return out
}

// Len returns the number of lines currently held in the ring.
func (r *Ring) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.count
}

// Writer implements io.Writer and io.Closer. It splits incoming bytes into
// lines and feeds each complete line into a Ring, while also teeing all raw
// bytes to an optional underlying file WriteCloser.
type Writer struct {
	mu      sync.Mutex
	ring    *Ring
	file    io.WriteCloser
	pending bytes.Buffer // bytes since the last newline
}

// NewWriter returns a Writer that feeds lines into ring and, if file is
// non-nil, tees raw bytes to file.
func NewWriter(ring *Ring, file io.WriteCloser) *Writer {
	return &Writer{ring: ring, file: file}
}

// Write splits p into lines, adding each complete line to the ring, and tees
// all bytes to the underlying file. The ring always receives the data; a file
// I/O error is returned but does not suppress ring writes.
func (w *Writer) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	var fileErr error
	if w.file != nil {
		if _, err := w.file.Write(p); err != nil {
			fileErr = err
		}
	}

	w.pending.Write(p)

	// Extract complete lines from the pending buffer.
	for {
		b := w.pending.Bytes()
		idx := bytes.IndexByte(b, '\n')
		if idx < 0 {
			break
		}
		line := string(b[:idx])
		// Advance past the newline.
		w.pending.Next(idx + 1)
		w.ring.Add(line)
	}

	if fileErr != nil {
		return 0, fileErr
	}
	return len(p), nil
}

// Close flushes any trailing partial line into the ring and closes the
// underlying file if one was provided.
func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.pending.Len() > 0 {
		w.ring.Add(w.pending.String())
		w.pending.Reset()
	}

	if w.file != nil {
		return w.file.Close()
	}
	return nil
}

// OpenFile creates any missing parent directories and opens path for writing
// (creating or truncating it), returning the file as an io.WriteCloser.
func OpenFile(path string) (io.WriteCloser, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	return os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
}

// OpenFileAppend creates any missing parent directories and opens path for
// appending (creating it if absent), returning the file as an io.WriteCloser.
// Unlike OpenFile it does not truncate existing content, so subsequent phases
// (e.g. teardown) can be appended after the main run's output.
func OpenFileAppend(path string) (io.WriteCloser, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	return os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
}
