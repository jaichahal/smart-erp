package migrations

import (
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

var gooseVersion = regexp.MustCompile(`^(\d+)_`)

// CheckDir fails when a goose directory has two files with the same version
// or when filename order is not numeric order. Directories are checked separately
// because migrator and superuser goose tables are independent.
func CheckDir(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	type file struct {
		name    string
		version string
		n       int
	}
	var files []file
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		m := gooseVersion.FindStringSubmatch(e.Name())
		if m == nil {
			return fmt.Errorf("goose migration %s has no version prefix", e.Name())
		}
		n, err := strconv.Atoi(m[1])
		if err != nil {
			return fmt.Errorf("goose migration %s: %w", e.Name(), err)
		}
		files = append(files, file{name: e.Name(), version: m[1], n: n})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].name < files[j].name })
	seen := map[int]string{}
	prev := -1
	prevName := ""
	for _, f := range files {
		if other, ok := seen[f.n]; ok {
			return fmt.Errorf("duplicate goose version %s: %s and %s", f.version, other, f.name)
		}
		seen[f.n] = f.name
		if prev >= 0 && f.n < prev {
			return fmt.Errorf("goose versions out of order: %s after %s", f.name, prevName)
		}
		prev = f.n
		prevName = f.name
	}
	return nil
}
