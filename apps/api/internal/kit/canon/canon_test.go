package canon

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSortedKeysAndExclusions(t *testing.T) {
	in := map[string]any{"b": 1, "a": "x", "hash": "zzz", "prev_hash": "yyy", "nested": map[string]any{"z": true, "y": nil}}
	got, err := Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"a":"x","b":1,"nested":{"y":null,"z":true}}`
	if string(got) != want {
		t.Fatalf("got %s want %s", got, want)
	}
}

func TestNumbersNormalise(t *testing.T) {
	a, err := Marshal(json.RawMessage(`{"q":1.0}`))
	if err != nil {
		t.Fatal(err)
	}
	b, err := Marshal(json.RawMessage(`{"q":1}`))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) {
		t.Fatalf("1.0 and 1 must canonicalise identically: %s vs %s", a, b)
	}
}

func TestTimeIsUTC(t *testing.T) {
	loc := time.FixedZone("GST", 4*3600)
	ts := time.Date(2026, 9, 25, 14, 0, 0, 0, loc)
	got, err := Marshal(map[string]any{"at": ts})
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `{"at":"2026-09-25T10:00:00Z"}` {
		t.Fatalf("got %s", got)
	}
}

func TestHashMatchesDefinition(t *testing.T) {
	c := []byte(`{"a":1}`)
	sum := sha256.Sum256(append([]byte(`{"a":1}`), []byte("prev")...))
	if got := Hash(c, "prev"); got != hex.EncodeToString(sum[:]) {
		t.Fatalf("hash must equal sha256(canonical||prev_hash): %s", got)
	}
	if Hash(c, "") == Hash(c, "prev") {
		t.Fatal("prev_hash must change the hash")
	}
}

// Fixtures in contracts/fixtures/canonical are the cross-client test vectors:
// each *.json has {"input": ..., "prev_hash": "...", "canonical": "...", "hash": "..."}.
// Set CANON_WRITE_FIXTURES=1 to regenerate canonical and hash from input.
func TestFixtures(t *testing.T) {
	dir := filepath.Join("..", "..", "..", "..", "..", "contracts", "fixtures", "canonical")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Skipf("no fixtures dir: %v", err)
	}
	type fixture struct {
		Input     json.RawMessage `json:"input"`
		PrevHash  string          `json:"prev_hash"`
		Canonical string          `json:"canonical"`
		Hash      string          `json:"hash"`
	}
	n := 0
	for _, e := range entries {
		if filepath.Ext(e.Name()) != ".json" {
			continue
		}
		path := filepath.Join(dir, e.Name())
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var f fixture
		if err := json.Unmarshal(raw, &f); err != nil {
			t.Fatalf("%s: %v", e.Name(), err)
		}
		canonical, hash, err := MarshalAndHash(f.Input, f.PrevHash)
		if err != nil {
			t.Fatalf("%s: %v", e.Name(), err)
		}
		if os.Getenv("CANON_WRITE_FIXTURES") == "1" {
			f.Canonical, f.Hash = string(canonical), hash
			out, err := json.MarshalIndent(f, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, append(out, '\n'), 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if string(canonical) != f.Canonical {
			t.Errorf("%s canonical\n got %s\nwant %s", e.Name(), canonical, f.Canonical)
		}
		if hash != f.Hash {
			t.Errorf("%s hash got %s want %s", e.Name(), hash, f.Hash)
		}
		n++
	}
	if n == 0 && os.Getenv("CANON_WRITE_FIXTURES") != "1" {
		t.Fatal("no fixtures found; run with CANON_WRITE_FIXTURES=1 to generate")
	}
}
