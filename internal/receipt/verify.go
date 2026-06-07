package receipt

import (
	"encoding/json"
	"os"
	"strings"
)

// LoadJSON reads a JSON receipt from disk.
func LoadJSON(path string) (Receipt, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Receipt{}, err
	}
	var r Receipt
	if err := json.Unmarshal(b, &r); err != nil {
		return Receipt{}, err
	}
	return r, nil
}

// CitationCount is the number of recipient outcomes that carry a cited log line.
func (r Receipt) CitationCount() int {
	n := 0
	for _, rr := range r.Result.Recipients {
		if rr.Citation != "" {
			n++
		}
	}
	return n
}

// VerifyCitations checks that every cited log line appears verbatim in logText.
// It returns the citations that were NOT found (empty slice means all present).
// This is what makes the receipt auditable: a fabricated or edited citation
// cannot pass, because the literal line must exist in the source log.
func (r Receipt) VerifyCitations(logText string) []string {
	logLines := completeLogLines(logText)
	var missing []string
	for _, rr := range r.Result.Recipients {
		if rr.Citation == "" {
			continue
		}
		if _, ok := logLines[normalizeLogLine(rr.Citation)]; !ok {
			missing = append(missing, rr.Citation)
		}
	}
	return missing
}

func completeLogLines(logText string) map[string]struct{} {
	lines := map[string]struct{}{}
	for _, line := range strings.Split(logText, "\n") {
		line = normalizeLogLine(line)
		if line == "" {
			continue
		}
		lines[line] = struct{}{}
	}
	return lines
}

func normalizeLogLine(line string) string {
	return strings.TrimSpace(line)
}
