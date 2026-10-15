package mail

import (
	"regexp"
	"sort"
	"strings"
	"time"
)

// reReplyPrefix matches the Re:, Fwd: and localised prefixes clients put
// before a subject, repeated or counted as in "Re[2]:".
var reReplyPrefix = regexp.MustCompile(`(?i)^\s*((re|fwd?|fw|aw|sv|wg|tr)(\[\d+\])?\s*:\s*)+`)

// normSubject is a subject without its reply prefixes, for comparing.
func normSubject(subject string) (norm string, prefixed bool) {
	rest := reReplyPrefix.ReplaceAllString(subject, "")
	return strings.ToLower(strings.Join(strings.Fields(rest), " ")), len(rest) != len(subject)
}

// subjectGap is how long after the last mail with its subject a reply still
// joins that conversation by subject alone.
const subjectGap = 30 * 24 * time.Hour

// threadRows groups mails into conversations: a mail joins the one holding
// the mail it answers. One whose original is not cached, or whose subject
// says it is a reply, joins the conversation with the same subject instead,
// unless its last mail is older than subjectGap.
// Each conversation is oldest first; the conversations newest first.
func threadRows(rows []row) [][]row {
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Date.Before(rows[j].Date) })
	parent := make([]int, len(rows))
	for i := range parent {
		parent[i] = i
	}
	var find func(int) int
	find = func(i int) int {
		if parent[i] != i {
			parent[i] = find(parent[i])
		}
		return parent[i]
	}
	union := func(a, b int) {
		if ra, rb := find(a), find(b); ra != rb {
			parent[max(ra, rb)] = min(ra, rb)
		}
	}
	byID := make(map[string]int, len(rows))
	for i, r := range rows {
		byID[r.MessageID] = i
	}
	last := make(map[string]int) // the newest mail so far with each subject
	for i, r := range rows {
		norm, prefixed := normSubject(r.Subject)
		p, answered := byID[r.InReplyTo]
		switch prev, ok := last[norm]; {
		case answered && p != i:
			union(i, p)
		case ok && norm != "" && (prefixed || r.InReplyTo != "") &&
			r.Date.Sub(rows[prev].Date) <= subjectGap:
			union(i, prev)
		}
		if norm != "" {
			last[norm] = i
		}
	}
	groups := make(map[int][]row)
	var roots []int
	for i, r := range rows {
		root := find(i)
		if _, ok := groups[root]; !ok {
			roots = append(roots, root)
		}
		groups[root] = append(groups[root], r)
	}
	out := make([][]row, 0, len(roots))
	for _, root := range roots {
		out = append(out, groups[root])
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i][len(out[i])-1].Date.After(out[j][len(out[j])-1].Date)
	})
	return out
}
