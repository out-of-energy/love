package lexicon

import (
	"bytes"
	"os"
	"sort"
)

// keysFile is a sorted TSV read by key without parsing the whole thing.
//
// The derivations file is 213k rows and the lemma list is 316k; decoding all of
// that into maps on every invocation of a command line tool would cost more
// than the lookup it is meant to speed up. The files are sorted by their first
// column, so a binary search over line offsets answers a query in a few
// comparisons and touches only the rows that match.
type keysFile struct {
	data   []byte
	starts []int
}

// openKeys reads path and records where each non-empty line begins. A missing
// file is not an error: the data layer is optional.
func openKeys(path string, _ int) *keysFile {
	data, err := os.ReadFile(path)
	if err != nil || len(data) == 0 {
		return &keysFile{}
	}

	var starts []int
	for i := 0; i < len(data); i++ {
		if data[i] == '\n' {
			continue
		}
		if i == 0 || data[i-1] == '\n' {
			starts = append(starts, i)
		}
	}
	return &keysFile{data: data, starts: starts}
}

func (f *keysFile) valid() bool { return f != nil && len(f.starts) > 0 }

// line returns line i without its trailing newline.
func (f *keysFile) line(i int) string {
	start := f.starts[i]
	end := len(f.data)
	if i+1 < len(f.starts) {
		end = f.starts[i+1] - 1
	} else if idx := bytes.IndexByte(f.data[start:], '\n'); idx >= 0 {
		end = start + idx
	}
	return string(f.data[start:end])
}

// keyAt returns the first tab-separated field of line i.
func (f *keysFile) keyAt(i int) string {
	line := f.line(i)
	if idx := bytes.IndexByte([]byte(line), '\t'); idx >= 0 {
		return line[:idx]
	}
	return line
}

// lowerBound returns the first line whose key is not less than key.
func (f *keysFile) lowerBound(key string) int {
	return sort.Search(len(f.starts), func(i int) bool { return f.keyAt(i) >= key })
}

// first returns the first row whose key matches.
func (f *keysFile) first(key string) (string, bool) {
	if !f.valid() {
		return "", false
	}
	i := f.lowerBound(key)
	if i >= len(f.starts) || f.keyAt(i) != key {
		return "", false
	}
	return f.line(i), true
}

// all returns every row whose key matches. Rows sharing a key are contiguous,
// because the builder sorts by key.
func (f *keysFile) all(key string) []string {
	if !f.valid() {
		return nil
	}
	var rows []string
	for i := f.lowerBound(key); i < len(f.starts) && f.keyAt(i) == key; i++ {
		rows = append(rows, f.line(i))
	}
	return rows
}
