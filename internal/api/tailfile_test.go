package api

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTailFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.log")

	// non-existent → ([], nil)
	if lines, err := tailFile(path, 10); err != nil || lines != nil {
		t.Fatalf("missing file: got %v, %v; want nil, nil", lines, err)
	}

	// 100 lines, request last 10 → returns last 10 in order
	var b strings.Builder
	for i := 0; i < 100; i++ {
		b.WriteString("line-")
		b.WriteByte(byte('0' + (i % 10)))
		b.WriteString("\n")
	}
	if err := os.WriteFile(path, []byte(b.String()), 0644); err != nil {
		t.Fatal(err)
	}
	got, err := tailFile(path, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 10 {
		t.Fatalf("len=%d want 10", len(got))
	}
	if got[9] != "line-9" {
		t.Fatalf("last line %q want line-9", got[9])
	}
	if got[0] != "line-0" {
		t.Fatalf("first kept line %q want line-0 (lines 90-99)", got[0])
	}

	// request more than exists → returns all
	got, err = tailFile(path, 1000)
	if err != nil { t.Fatal(err) }
	if len(got) != 100 {
		t.Fatalf("len=%d want 100", len(got))
	}
}
