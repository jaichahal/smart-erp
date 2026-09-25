package e2e

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// coveredByDedicated is asserted in its own Test function so a pass is visible
// separately from the gap catalog.
var coveredByDedicated = map[string]bool{
	"A1":  true,
	"A17": true,
	"B1":  true,
}

func TestAcceptanceCatalog(t *testing.T) {
	root := repoRoot()
	tasks := loadTasks(t, filepath.Join(root, "docs/spec/07-tracks-and-tasks.md"))
	lines := loadAcceptance(t, filepath.Join(root, "docs/spec/08-acceptance-tests.md"))
	s := newStack(t)
	seen := map[string]bool{}
	for _, task := range tasks {
		if len(task.IDs) == 0 {
			if task.ID == "P0.1" {
				continue
			}
			task := task
			t.Run(task.ID+"/accept", func(t *testing.T) {
				status, code, raw := s.call(t, "POST", "/api/v1/not-implemented/"+task.ID, `{}`, nil)
				t.Fatalf("%s not satisfied (%s): POST /api/v1/not-implemented/%s -> %d %s %s", task.ID, task.Accept, task.ID, status, code, trim(raw))
			})
			continue
		}
		for _, id := range task.IDs {
			if coveredByDedicated[id] {
				seen[id] = true
				continue
			}
			line, ok := lines[id]
			if !ok {
				id, task := id, task
				t.Run(task.ID+"/"+id, func(t *testing.T) {
					t.Fatalf("%s lists %s but docs/spec/08-acceptance-tests.md has no such case", task.ID, id)
				})
				continue
			}
			seen[id] = true
			id, task, line := id, task, line
			t.Run(task.ID+"/"+id, func(t *testing.T) {
				probe(t, s, task.ID, id, line)
			})
		}
	}
	for id, line := range lines {
		if seen[id] || coveredByDedicated[id] {
			continue
		}
		id, line := id, line
		t.Run("unmapped/"+id, func(t *testing.T) {
			probe(t, s, "unmapped", id, line)
		})
	}
}

func probe(t *testing.T, s *stack, task, id, sentence string) {
	t.Helper()
	method, path, want := probeFor(id, sentence)
	status, code, raw := s.call(t, method, path, `{}`, nil)
	if want != "" && code == want {
		return
	}
	t.Fatalf("%s/%s not satisfied: %s\n%s %s -> %d code %q (want %s) body %s", task, id, sentence, method, path, status, code, want, trim(raw))
}

func probeFor(id, sentence string) (method, path, wantCode string) {
	wantCode = firstCode(sentence)
	method = "POST"
	switch {
	case strings.HasPrefix(id, "A"):
		path = "/api/v1/auth/session"
	case strings.HasPrefix(id, "B"):
		path = "/api/v1/audit/verify"
	case id == "C5":
		method = "GET"
		path = "/api/v1/holiday-calendar"
	case strings.HasPrefix(id, "C") && idNum(id) <= 5:
		path = "/api/v1/periods/00000000-0000-0000-0000-000000000001/soft-close"
	case strings.HasPrefix(id, "C"):
		path = "/api/v1/journals"
	case strings.HasPrefix(id, "D"):
		path = "/api/v1/approvals/00000000-0000-0000-0000-000000000001/approve"
	case strings.HasPrefix(id, "E") && idNum(id) <= 15:
		method = "GET"
		path = "/api/v1/notifications"
	case strings.HasPrefix(id, "E"):
		method = "GET"
		path = "/api/v1/analytics/snapshot"
	case strings.HasPrefix(id, "F"):
		method = "GET"
		path = "/api/v1/me"
	case strings.HasPrefix(id, "G"):
		method = "GET"
		path = "/api/v1/work-queue"
	case strings.HasPrefix(id, "H"):
		method = "GET"
		path = "/api/v1/stock"
	case strings.HasPrefix(id, "I"):
		method = "GET"
		path = "/api/v1/status"
	case strings.HasPrefix(id, "K"):
		path = "/api/v1/bank/reconciliation"
	case strings.HasPrefix(id, "P"):
		path = "/api/v1/purchase-orders"
	case strings.HasPrefix(id, "R"):
		path = "/api/v1/receipts"
	case strings.HasPrefix(id, "S"):
		path = "/api/v1/sales-orders"
	default:
		path = "/api/v1/not-implemented/" + id
	}
	return method, path, wantCode
}

var codeRe = regexp.MustCompile("`([A-Z][A-Z0-9_]+)`")

func firstCode(sentence string) string {
	m := codeRe.FindStringSubmatch(sentence)
	if m == nil {
		return ""
	}
	return m[1]
}

func idNum(id string) int {
	i := 1
	for i < len(id) && (id[i] < '0' || id[i] > '9') {
		i++
	}
	n, _ := strconv.Atoi(id[i:])
	return n
}

func trim(b []byte) string {
	s := string(b)
	if len(s) > 240 {
		return s[:240]
	}
	return s
}

type task struct {
	ID     string
	Accept string
	IDs    []string
}

func loadTasks(t *testing.T, path string) []task {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []task
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, "- P") {
			continue
		}
		id := strings.Fields(line)[1]
		id = strings.TrimRight(id, ":")
		if !taskIDRe.MatchString(id) && id != "Pre-go-live" {
			continue
		}
		acc := ""
		if i := strings.Index(line, "Accept:"); i >= 0 {
			acc = strings.TrimSpace(line[i+len("Accept:"):])
		}
		out = append(out, task{ID: id, Accept: acc, IDs: expandIDs(acc)})
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

var taskIDRe = regexp.MustCompile(`^P\d+\.\d+$`)

func expandIDs(accept string) []string {
	if accept == "" {
		return nil
	}
	var ids []string
	used := map[string]bool{}
	rangeRe := regexp.MustCompile(`([A-Z])(\d+)\s+to\s+([A-Z])(\d+)`)
	for _, m := range rangeRe.FindAllStringSubmatch(accept, -1) {
		if m[1] != m[3] {
			continue
		}
		from, _ := strconv.Atoi(m[2])
		to, _ := strconv.Atoi(m[4])
		for n := from; n <= to; n++ {
			id := m[1] + strconv.Itoa(n)
			if !used[id] {
				used[id] = true
				ids = append(ids, id)
			}
		}
	}
	stripped := rangeRe.ReplaceAllString(accept, " ")
	singleRe := regexp.MustCompile(`\b([A-Z])(\d+)\b`)
	for _, m := range singleRe.FindAllStringSubmatch(stripped, -1) {
		id := m[1] + m[2]
		if !used[id] {
			used[id] = true
			ids = append(ids, id)
		}
	}
	return ids
}

func loadAcceptance(t *testing.T, path string) map[string]string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	out := map[string]string{}
	sc := bufio.NewScanner(f)
	lineRe := regexp.MustCompile(`^- ([A-Z]\d+)\s+(.*)$`)
	for sc.Scan() {
		m := lineRe.FindStringSubmatch(strings.TrimSpace(sc.Text()))
		if m == nil {
			continue
		}
		out[m[1]] = m[2]
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func repoRoot() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Clean(filepath.Join(filepath.Dir(file), "../../.."))
}
