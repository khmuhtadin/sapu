package plan

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/khmuhtadin/sapu/internal/rules"
)

func TestWriteRead(t *testing.T) {
	in := &Plan{
		SchemaVersion: SchemaVersion,
		Kind:          KindClean,
		CreatedAt:     time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC),
		Home:          "/h",
		Items: []Item{{
			ID: "0001", Source: "test.rule", Path: "/h/Library/Caches/x", Bytes: 4096,
			Risk: rules.RiskSafe, Regenerates: true, Explain: "x", Action: rules.ActionQuarantine,
			Identity: Identity{Dev: 1, Ino: 2, Mode: 3, Size: 4, MtimeNs: 5}, Selected: true,
		}},
		Skipped: []Skip{{Path: "/h/.claude", Source: "test.rule", Reason: ReasonProtected}},
	}
	var buf bytes.Buffer
	if err := Write(&buf, in); err != nil {
		t.Fatal(err)
	}
	out, err := Read(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(out, in) {
		t.Fatalf("round trip changed the plan:\n%+v\n%+v", in, out)
	}
}

func TestReadRejectsOtherVersions(t *testing.T) {
	_, err := Read(strings.NewReader(`{"schema_version": 99, "kind": "clean"}`))
	if err == nil {
		t.Fatal("expected an error for schema_version 99")
	}
	_, err = Read(strings.NewReader(`{"schema_version": 1, "surprise": true}`))
	if err == nil {
		t.Fatal("expected an error for an unknown field")
	}
}

func TestIdentityOf(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "a")
	if err := os.WriteFile(f, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	a, err := IdentityOf(f)
	if err != nil {
		t.Fatal(err)
	}
	if a.Size != 5 || a.Ino == 0 {
		t.Fatalf("unexpected identity %+v", a)
	}
	b, _ := IdentityOf(f)
	if a != b {
		t.Fatal("identity of an unchanged file changed")
	}
	if err := os.WriteFile(f, []byte("hello world"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, _ := IdentityOf(f)
	if a == c {
		t.Fatal("identity did not change after a write")
	}
	if _, err := IdentityOf(filepath.Join(dir, "missing")); !os.IsNotExist(err) {
		t.Fatalf("want not-exist error, got %v", err)
	}
}
