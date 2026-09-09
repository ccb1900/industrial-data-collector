package source

import (
	"bytes"
	"io"
	"os"
	"strings"

	appencoding "gocordis-csv-collector/app/encoding"
)

// sniffLimit caps how many bytes content detection reads per file. Discovery
// opens every candidate file once; 8 KiB is enough to judge tabular text
// without meaningfully reading remote files.
const sniffLimit = 8 * 1024

// looksLikeCSV reports whether the leading bytes of the file at path are
// consistent with delimited text under the source's configured encoding.
// Discovery uses it so collection does not depend on file extensions: a
// "20260908.dat" export with GBK or UTF-16 CSV content is collected, a
// renamed binary blob is not.
//
// The check is deliberately conservative on rejection (NUL bytes, undecodable
// samples) and lenient on acceptance (any repeated field separator across at
// least two lines). Whether the bytes really parse is a parse-time decision,
// and character decoding is the parser's job — sniffing only needs to see
// the structure through the same encoding lens.
func (s *Source) looksLikeCSV(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	prefix := make([]byte, sniffLimit)
	n, err := io.ReadFull(f, prefix)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return false
	}
	data := prefix[:n]
	if len(data) == 0 {
		return false
	}
	name, err := appencoding.Normalize(s.Encoding)
	if err != nil {
		return false
	}
	decoded := appencoding.DecodeSample(data, name)
	if len(decoded) == 0 {
		return false
	}
	// NUL bytes indicate binary content in every supported text encoding.
	if bytes.IndexByte(decoded, 0x00) >= 0 {
		return false
	}
	lines := strings.Split(strings.TrimSuffix(string(decoded), "\n"), "\n")
	if len(lines) < 2 {
		// A one-line file cannot show tabular structure; accept it only when a
		// known delimiter is present so header-only exports still pass.
		return strings.ContainsAny(lines[0], ",;\t")
	}
	sep := fieldSeparator(lines)
	if sep == "" {
		return false
	}
	counts := make(map[int]int, len(lines))
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		counts[strings.Count(line, sep)]++
	}
	// The dominant per-line separator count must cover most non-empty lines;
	// quoted newlines inside fields make exact equality too strict for sniffing.
	dominant, occurrences := 0, 0
	for count, seen := range counts {
		if seen > occurrences || (seen == occurrences && count > dominant) {
			dominant, occurrences = count, seen
		}
	}
	nonEmpty := 0
	for _, seen := range counts {
		nonEmpty += seen
	}
	return occurrences*4 >= nonEmpty*3
}

// fieldSeparator picks the delimiter that appears on the most lines, breaking
// ties toward the conventional comma.
func fieldSeparator(lines []string) string {
	scores := map[string]int{",": 0, ";": 0, "\t": 0, "|": 0}
	for _, line := range lines {
		for sep := range scores {
			if strings.Contains(line, sep) {
				scores[sep]++
			}
		}
	}
	best, bestCount := "", 0
	for _, sep := range []string{",", ";", "\t", "|"} {
		if scores[sep] > bestCount {
			best, bestCount = sep, scores[sep]
		}
	}
	if bestCount == 0 {
		return ""
	}
	return best
}
