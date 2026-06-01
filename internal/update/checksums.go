package update

import (
	"bufio"
	"bytes"
	"fmt"
	"strings"
)

// archiveSuffix returns the expected filename suffix for the release archive on
// the given platform, matching goreleaser's naming convention.
func archiveSuffix(goos, goarch string) string {
	if goos == "windows" {
		return fmt.Sprintf("_%s_%s.zip", goos, goarch)
	}
	return fmt.Sprintf("_%s_%s.tar.gz", goos, goarch)
}

// findArchive parses a SHA-256 checksums file (as produced by goreleaser) and
// returns the archive filename and expected hex digest for the given platform.
func findArchive(content []byte, goos, goarch string) (name, digest string, err error) {
	suffix := archiveSuffix(goos, goarch)
	sc := bufio.NewScanner(bytes.NewReader(content))
	for sc.Scan() {
		line := sc.Text()
		// goreleaser uses two spaces between digest and filename.
		parts := strings.SplitN(line, "  ", 2)
		if len(parts) != 2 {
			continue
		}
		if strings.HasSuffix(parts[1], suffix) {
			return parts[1], parts[0], nil
		}
	}
	return "", "", fmt.Errorf("no archive for %s/%s (suffix %q) in checksums file", goos, goarch, suffix)
}

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
