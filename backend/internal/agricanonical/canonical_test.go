package agricanonical

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
)

func TestGolden(t *testing.T) {
	raw, err := os.ReadFile("../../../tests/agriculture/fixtures/identity-canonical.json")
	if err != nil {
		t.Fatal(err)
	}
	type vector struct {
		Name   string `json:"name"`
		Input  string `json:"input"`
		Output string `json:"output_hex"`
		Hex    string `json:"input_hex"`
	}
	var fixture struct {
		Valid      []vector `json:"valid"`
		Invalid    []vector `json:"invalid"`
		InvalidHex []vector `json:"invalid_hex"`
	}
	if err = json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	for _, v := range fixture.Valid {
		t.Run(v.Name, func(t *testing.T) {
			out, e := Canonicalize([]byte(v.Input))
			if e != nil || hex.EncodeToString(out) != v.Output {
				t.Fatalf("canonical mismatch: %x error=%v", out, e)
			}
		})
	}
	for _, v := range fixture.Invalid {
		t.Run(v.Name, func(t *testing.T) {
			out, e := Canonicalize([]byte(v.Input))
			if e == nil || out != nil {
				t.Fatal("invalid input accepted")
			}
		})
	}
	for _, v := range fixture.InvalidHex {
		t.Run(v.Name, func(t *testing.T) {
			raw, _ := hex.DecodeString(v.Hex)
			if _, e := Canonicalize(raw); e == nil {
				t.Fatal("invalid bytes accepted")
			}
		})
	}
}

func TestBounds(t *testing.T) {
	check := func(name, s string, valid bool) {
		t.Helper()
		t.Run(name, func(t *testing.T) {
			_, e := Canonicalize([]byte(s))
			if (e == nil) != valid {
				t.Fatalf("valid=%v err=%v", valid, e)
			}
		})
	}
	check("bytes_at_limit", `{"v":"`+strings.Repeat("a", (1<<20)-8)+`"}`, true)
	check("bytes_over_limit", `{"v":"`+strings.Repeat("a", (1<<20)-7)+`"}`, false)
	check("depth32", `{"v":`+strings.Repeat("[", 30)+`0`+strings.Repeat("]", 30)+`}`, true)
	check("depth33", `{"v":`+strings.Repeat("[", 31)+`0`+strings.Repeat("]", 31)+`}`, false)
	for _, n := range []int{256, 257} {
		entries := []string{}
		for i := 0; i < n; i++ {
			entries = append(entries, fmt.Sprintf(`"k%d":0`, i))
		}
		check(fmt.Sprint("keys", n), "{"+strings.Join(entries, ",")+"}", n == 256)
	}
	for _, n := range []int{9998, 9999} {
		check(fmt.Sprint("nodes", n+2), `{"v":[`+strings.TrimSuffix(strings.Repeat("0,", n), ",")+`]}`, n == 9998)
	}
}

func FuzzCanonicalize(f *testing.F) {
	for _, s := range []string{`{}`, `{"n":1e-9}`, `{"s":"\ud800"}`, `{"a":1,"a":2}`} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		out, err := Canonicalize(raw)
		if err == nil {
			again, e := Canonicalize(out)
			if e != nil || string(again) != string(out) {
				t.Fatal("not idempotent")
			}
		}
	})
}
