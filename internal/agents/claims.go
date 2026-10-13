package agents

import (
	"regexp"
	"strings"
)

type Claim struct {
	Kind     string `json:"kind"`
	Text     string `json:"text"`
	Verdict  string `json:"verdict"`
	Evidence string `json:"evidence,omitempty"`
	Note     string `json:"note"`
}

type claimRule struct {
	kind       string
	claim      *regexp.Regexp
	evidence   *regexp.Regexp
	afterEdits bool
}

var claimRules = []claimRule{
	{"tests", regexp.MustCompile(`(?i)\b(tests?|specs?|suite)\b[^.\n]{0,40}\b(pass|passed|passes|passing|green)\b|\b\d+\s*(/\s*\d+\s*)?(tests?\s+)?(pass|passed|passing)\b`),
		regexp.MustCompile(`(?i)\b((npm|pnpm|yarn|bun)\s+(run\s+)?test|node\s+--test|go\s+test|pytest|vitest|jest|cargo\s+test|mvn\s+test|gradle\w*\s+test|dotnet\s+test|rspec|phpunit)\b`), true},
	{"typecheck", regexp.MustCompile(`(?i)\b(typecheck\w*|type[- ]check\w*|tsc)\b[^.\n]{0,30}\b(pass\w*|clean|ok|succeed\w*|green)\b`),
		regexp.MustCompile(`(?i)\b(tsc|typecheck|mypy|pyright|go\s+vet)\b`), true},
	{"build", regexp.MustCompile(`(?i)\bbuild\w*\b[^.\n]{0,25}\b(pass\w*|succeed\w*|ok|clean|works?|green)\b`),
		regexp.MustCompile(`(?i)\b((npm|pnpm|yarn|bun)\s+(run\s+)?build|go\s+build|cargo\s+build|vite\s+build|next\s+build|tsc\b)`), true},
	{"lint", regexp.MustCompile(`(?i)\blint\w*\b[^.\n]{0,25}\b(pass\w*|clean|ok)\b`),
		regexp.MustCompile(`(?i)\b(eslint|ruff|golangci-lint|biome|(npm|pnpm|yarn)\s+(run\s+)?lint|actionlint|shellcheck)\b`), true},
	{"validated", regexp.MustCompile(`(?i)\bvalidat\w*\b[^.\n]{0,30}\b(pass\w*|ok|valid)\b`),
		regexp.MustCompile(`(?i)\bvalidat\w*\b`), true},
	{"pushed", regexp.MustCompile(`(?i)\bpushed\b`),
		regexp.MustCompile(`(?i)\bgit\s+(\S+\s+)*push\b|\bgh\s+repo\s+create\b[^\n]*--push`), false},
	{"committed", regexp.MustCompile(`(?i)\bcommitted\b|\b\d+\s+(small\s+|local\s+|conventional\s+)*commits?\b`),
		regexp.MustCompile(`(?i)\bgit\s+(\S+\s+)*commit\b`), false},
}

var negation = regexp.MustCompile(`(?i)\b(not|never|didn't|did not|no|without|couldn't|could not|can't|cannot|nothing)\b`)

func CheckClaims(final string, events []Event) []Claim {
	claims := []Claim{}
	if strings.TrimSpace(final) == "" {
		return claims
	}
	lastEdit := -1
	for i, e := range events {
		if e.Edit {
			lastEdit = i
		}
	}
	seen := map[string]bool{}
	for _, sentence := range sentences(final) {
		for _, r := range claimRules {
			if seen[r.kind] || !r.claim.MatchString(sentence) || negated(sentence, r.claim) {
				continue
			}
			seen[r.kind] = true
			claims = append(claims, judge(r, strings.TrimSpace(sentence), events, lastEdit))
		}
	}
	return claims
}

func negated(sentence string, re *regexp.Regexp) bool {
	loc := re.FindStringIndex(sentence)
	if loc == nil {
		return false
	}
	start := loc[0] - 40
	if start < 0 {
		start = 0
	}
	return negation.MatchString(sentence[start:loc[1]])
}

func judge(r claimRule, text string, events []Event, lastEdit int) Claim {
	c := Claim{Kind: r.kind, Text: clip(text, 300)}
	lastRun, lastOK, lastFail := -1, -1, -1
	for i, e := range events {
		if e.Kind != "tool" || (e.Tool != "Bash" && e.Tool != "PowerShell") || !r.evidence.MatchString(e.Summary) {
			continue
		}
		lastRun = i
		if e.IsError || failedOutput(e.Output) {
			lastFail = i
		} else {
			lastOK = i
		}
	}
	switch {
	case lastRun < 0:
		c.Verdict, c.Note = "unverified", "no matching command in the transcript"
	case lastOK < 0:
		c.Verdict, c.Note = "contradicted", "every matching command failed"
		c.Evidence = clip(events[lastFail].Summary, 300)
	case r.afterEdits && lastOK < lastEdit:
		c.Verdict, c.Note = "stale", "last successful run happened before the last file edit"
		c.Evidence = clip(events[lastOK].Summary, 300)
	case lastFail > lastOK:
		c.Verdict, c.Note = "contradicted", "the most recent matching command failed"
		c.Evidence = clip(events[lastFail].Summary, 300)
	default:
		c.Verdict, c.Note = "supported", "matching command succeeded after the last edit"
		if !r.afterEdits {
			c.Note = "matching command succeeded"
		}
		c.Evidence = clip(events[lastOK].Summary, 300)
	}
	return c
}

var failMarkers = regexp.MustCompile(`(?im)^\s*(ℹ\s*fail\s+[1-9]|FAIL\b|--- FAIL|npm ERR!|error TS\d+|Tests?:\s+\d+ failed|\d+ failed|failures?:\s*[1-9]|exit code [1-9])`)

func failedOutput(out string) bool { return failMarkers.MatchString(out) }

func sentences(s string) []string {
	s = strings.ReplaceAll(s, "\r", "")
	var out []string
	for _, para := range strings.Split(s, "\n") {
		para = strings.TrimSpace(strings.TrimLeft(para, "-*•# "))
		if para == "" {
			continue
		}
		start := 0
		for i := 0; i < len(para)-1; i++ {
			if (para[i] == '.' || para[i] == '!' || para[i] == '?') && para[i+1] == ' ' {
				out = append(out, para[start:i+1])
				start = i + 2
			}
		}
		out = append(out, para[start:])
	}
	return out
}
