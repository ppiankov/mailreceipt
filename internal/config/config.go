// Package config reads and writes the optional .mailreceipt.yml file: per-project
// defaults for the check/verify commands so an operator working one mail server
// does not retype --log every run. Explicit flags always override these values.
//
// The format is a deliberately tiny YAML-like subset so the tool needs no YAML
// dependency. Lines that are blank or start with '#' are ignored; unknown keys
// are ignored (forward-compatible).
package config

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// FileName is the config file the commands look for in the working directory.
const FileName = ".mailreceipt.yml"

// Config holds the defaults a project may set. Zero values mean "unset".
type Config struct {
	Log           string              // default --log path
	LogYear       int                 // default --log-year (0 = unset)
	CasePrefix    string              // prefix prepended to --case when set
	ReceiptFilter ReceiptFilterConfig // WO-13: automatic receipt filter authorization defaults
}

// ReceiptFilterConfig defines who may request automatic receipts by email.
type ReceiptFilterConfig struct {
	Domains []string            // WO-13: internal domains accepted as authenticated envelope senders
	Teams   map[string][]string // WO-13: named ownership groups for trigger sender and sent-message owner
}

// Load reads cfgPath. A missing file is not an error: it returns an empty Config
// and ok=false so callers can tell "no config" from "config with empty values".
func Load(cfgPath string) (Config, bool, error) {
	f, err := os.Open(cfgPath)
	if err != nil {
		if os.IsNotExist(err) {
			return Config{}, false, nil
		}
		return Config{}, false, err
	}
	defer f.Close()

	var c Config
	var inReceiptFilter bool
	var inTeams bool
	var currentTeam string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		raw := sc.Text()
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		indent := leadingSpaces(raw)
		key, val, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		key = strings.TrimSpace(key)
		val = unquote(strings.TrimSpace(val))
		if indent == 0 {
			inReceiptFilter = false
			inTeams = false
			currentTeam = ""
		}
		if inReceiptFilter && indent > 0 {
			switch {
			case indent == 2 && key == "domains":
				c.ReceiptFilter.Domains = parseList(val)
			case indent == 2 && key == "teams":
				inTeams = true
				ensureTeams(&c)
			case inTeams && indent == 4:
				currentTeam = key
				ensureTeams(&c)
				if _, ok := c.ReceiptFilter.Teams[currentTeam]; !ok {
					c.ReceiptFilter.Teams[currentTeam] = nil
				}
			case inTeams && indent == 6 && key == "members" && currentTeam != "":
				ensureTeams(&c)
				c.ReceiptFilter.Teams[currentTeam] = parseList(val)
			}
			continue
		}
		switch key {
		case "log":
			c.Log = val
		case "log_year":
			if n, err := strconv.Atoi(val); err == nil {
				c.LogYear = n
			}
		case "case_prefix":
			c.CasePrefix = val
		case "receipt_filter":
			inReceiptFilter = true
		}
	}
	if err := sc.Err(); err != nil {
		return Config{}, false, err
	}
	return c, true, nil
}

// unquote strips one layer of matching surrounding quotes, so `case_prefix: "P-"`
// yields P- not "P-". An unquoted or empty value passes through unchanged.
func unquote(s string) string {
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') {
			return s[1 : len(s)-1]
		}
	}
	return s
}

func leadingSpaces(s string) int {
	for i := 0; i < len(s); i++ {
		if s[i] != ' ' {
			return i
		}
	}
	return len(s)
}

func parseList(s string) []string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "[")
	s = strings.TrimSuffix(s, "]")
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.ToLower(unquote(strings.TrimSpace(p)))
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func ensureTeams(c *Config) {
	if c.ReceiptFilter.Teams == nil {
		c.ReceiptFilter.Teams = map[string][]string{}
	}
}

// Template is the commented scaffold `init` writes. Keys match Load's parser.
const Template = `# mailreceipt project config. Used as defaults; explicit flags override.
# log: path to the Postfix mail log
log: /var/log/mail.log
# log_year: year for year-less BSD syslog timestamps (RFC3339 logs ignore this)
log_year: 2026
# case_prefix: prepended to the --case value on every receipt (optional)
case_prefix: ""
# receipt_filter: optional inbound alias filter for forwarded sent-message requests
# receipt_filter:
#   domains: [example.test]
#   teams:
#     docketing:
#       members: [docketing@example.test, attorney@example.test]
`

// Write creates cfgPath with the template. It refuses to clobber an existing file
// unless force is set, so `init` never silently overwrites a tuned config.
func Write(cfgPath string, force bool) error {
	if !force {
		if _, err := os.Stat(cfgPath); err == nil {
			return fmt.Errorf("%s already exists (use --force to overwrite)", cfgPath)
		}
	}
	return os.WriteFile(cfgPath, []byte(Template), 0o644)
}
