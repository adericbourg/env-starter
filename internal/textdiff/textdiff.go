// Package textdiff renders a line-based unified diff, used to show what
// changed in a config file since it was last approved.
package textdiff

import (
	"fmt"
	"strings"
)

// context is the number of unchanged lines kept around each change, as in
// `diff -u`.
const context = 3

type op struct {
	kind byte // ' ' unchanged, '-' removed, '+' added
	text string
}

// Unified returns the unified diff between old and new, or "" when they have
// the same lines. A difference limited to a trailing newline is not reported.
func Unified(oldName, newName string, old, new []byte) string {
	ops := diffLines(splitLines(old), splitLines(new))

	// oldPos[k]/newPos[k] count the old/new lines before ops[k].
	oldPos := make([]int, len(ops)+1)
	newPos := make([]int, len(ops)+1)
	for k, o := range ops {
		oldPos[k+1], newPos[k+1] = oldPos[k], newPos[k]
		if o.kind != '+' {
			oldPos[k+1]++
		}
		if o.kind != '-' {
			newPos[k+1]++
		}
	}

	var b strings.Builder
	for k := 0; k < len(ops); {
		if ops[k].kind == ' ' {
			k++
			continue
		}
		start := max(0, k-context)
		end := k
		// Extend the hunk over changes separated by short runs of unchanged
		// lines, so their context does not overlap across two hunks.
		for end < len(ops) {
			if ops[end].kind != ' ' {
				end++
				continue
			}
			run := 0
			for end+run < len(ops) && ops[end+run].kind == ' ' {
				run++
			}
			if end+run == len(ops) || run > 2*context {
				break
			}
			end += run
		}
		stop := min(len(ops), end+context)

		if b.Len() == 0 {
			fmt.Fprintf(&b, "--- %s\n+++ %s\n", oldName, newName)
		}
		fmt.Fprintf(&b, "@@ -%s +%s @@\n",
			hunkRange(oldPos[start], oldPos[stop]-oldPos[start]),
			hunkRange(newPos[start], newPos[stop]-newPos[start]))
		for _, o := range ops[start:stop] {
			b.WriteByte(o.kind)
			b.WriteString(o.text)
			b.WriteByte('\n')
		}
		k = stop
	}
	return b.String()
}

// hunkRange formats a hunk's "start,count" with 1-based line numbers; an
// empty range points at the line before it, per the unified format.
func hunkRange(before, count int) string {
	if count == 0 {
		return fmt.Sprintf("%d,0", before)
	}
	return fmt.Sprintf("%d,%d", before+1, count)
}

func splitLines(b []byte) []string {
	if len(b) == 0 {
		return nil
	}
	return strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
}

// diffLines returns the edit script turning a into b, from their longest
// common subsequence.
// ponytail: O(n·m) LCS table, fine for config-sized files; switch to Myers if
// configs reach tens of thousands of lines.
func diffLines(a, b []string) []op {
	// lcs[i][j] is the LCS length of a[i:] and b[j:].
	lcs := make([][]int32, len(a)+1)
	for i := range lcs {
		lcs[i] = make([]int32, len(b)+1)
	}
	for i := len(a) - 1; i >= 0; i-- {
		for j := len(b) - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}

	ops := make([]op, 0, len(a)+len(b))
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		switch {
		case a[i] == b[j]:
			ops = append(ops, op{' ', a[i]})
			i++
			j++
		case lcs[i+1][j] >= lcs[i][j+1]:
			ops = append(ops, op{'-', a[i]})
			i++
		default:
			ops = append(ops, op{'+', b[j]})
			j++
		}
	}
	for ; i < len(a); i++ {
		ops = append(ops, op{'-', a[i]})
	}
	for ; j < len(b); j++ {
		ops = append(ops, op{'+', b[j]})
	}
	return ops
}
