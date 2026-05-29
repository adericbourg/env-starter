package update

import (
	"bufio"
	"bytes"
	"fmt"
	"strings"
)

// lookupChecksum parses a SHA-256 checksums file (as produced by goreleaser)
// and returns the expected hex digest for the named file.
//
// Each line has the format: "<sha256>  <filename>".
func lookupChecksum(content []byte, filename string) (string, error) {
	sc := bufio.NewScanner(bytes.NewReader(content))
	for sc.Scan() {
		line := sc.Text()
		// goreleaser uses two spaces between digest and filename.
		parts := strings.SplitN(line, "  ", 2)
		if len(parts) != 2 {
			continue
		}
		if parts[1] == filename {
			return parts[0], nil
		}
	}
	return "", fmt.Errorf("checksum for %q not found in checksums file", filename)
}
