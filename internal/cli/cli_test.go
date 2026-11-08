package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/khmuhtadin/sapu/internal/env"
)

// sapu runs one command line against env e, with stdin as input, and
// returns stdout, stderr and the exit code. Tests in this package must not
// use t.Parallel: the global flags are shared.
func sapu(t *testing.T, e env.Env, stdin string, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	old := loadEnv
	loadEnv = func() (env.Env, error) { return e, nil }
	t.Cleanup(func() { loadEnv = old })

	var out, errOut bytes.Buffer
	code = runArgs(context.Background(), args, strings.NewReader(stdin), &out, &errOut)
	return out.String(), errOut.String(), code
}

// validate checks that out is one envelope matching schema/v1/envelope.json
// and that its data matches schema/v1/<name>.json.
func validate(t *testing.T, name, out string) {
	t.Helper()
	var doc map[string]any
	dec := json.NewDecoder(strings.NewReader(out))
	if err := dec.Decode(&doc); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, out)
	}
	if dec.More() {
		t.Fatalf("output has more than one JSON document:\n%s", out)
	}
	check(t, "envelope", doc)
	if data, ok := doc["data"]; ok {
		check(t, name, data)
	}
}

func check(t *testing.T, name string, v any) {
	t.Helper()
	path, err := filepath.Abs(filepath.Join("..", "..", "schema", "v1", name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("schema %s: %v", name, err)
	}
	defer func() { _ = f.Close() }()
	doc, err := jsonschema.UnmarshalJSON(f)
	if err != nil {
		t.Fatalf("schema %s: %v", name, err)
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource(path, doc); err != nil {
		t.Fatalf("schema %s: %v", name, err)
	}
	sch, err := c.Compile(path)
	if err != nil {
		t.Fatalf("schema %s: %v", name, err)
	}
	// Round-trip through JSON so numbers and maps have the types the validator expects.
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	if err := sch.Validate(inst); err != nil {
		t.Fatalf("output does not match schema %s: %v", name, err)
	}
}

func TestVersionJSON(t *testing.T) {
	out, _, code := sapu(t, env.New(t.TempDir()), "", "version", "--json")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	validate(t, "version", out)
}

func TestUsageErrorsExit2(t *testing.T) {
	e := env.New(t.TempDir())
	if _, _, code := sapu(t, e, "", "no-such-command"); code != 2 {
		t.Errorf("unknown command: exit %d, want 2", code)
	}
	if _, _, code := sapu(t, e, "", "version", "--no-such-flag"); code != 2 {
		t.Errorf("unknown flag: exit %d, want 2", code)
	}
}
