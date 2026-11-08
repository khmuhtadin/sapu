package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/khmuhtadin/sapu/internal/env"
)

func TestMCPServesOverStdio(t *testing.T) {
	e := env.New(t.TempDir())
	in := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
	}, "\n") + "\n"

	out, errOut, code := sapu(t, e, in, "mcp")
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, errOut)
	}
	// stdout holds protocol messages and nothing else: one per request.
	msgs := strings.Split(strings.TrimSpace(out), "\n")
	if len(msgs) != 2 {
		t.Fatalf("want 2 messages, got %d:\n%s", len(msgs), out)
	}
	for _, m := range msgs {
		var v map[string]any
		if err := json.Unmarshal([]byte(m), &v); err != nil || v["jsonrpc"] != "2.0" {
			t.Errorf("not a JSON-RPC message: %s", m)
		}
	}
	if !strings.Contains(msgs[0], `"name":"sapu"`) || !strings.Contains(msgs[1], `"plan_clean"`) {
		t.Errorf("unexpected replies:\n%s", out)
	}
}

func TestMCPRejectsJSONFlagAndArguments(t *testing.T) {
	e := env.New(t.TempDir())
	for _, args := range [][]string{{"mcp", "--json"}, {"mcp", "extra"}} {
		out, _, code := sapu(t, e, "", args...)
		if code != 2 {
			t.Errorf("sapu %v: exit %d, want 2 (usage)", args, code)
		}
		if strings.Contains(out, `"jsonrpc"`) {
			t.Errorf("sapu %v answered as a server:\n%s", args, out)
		}
	}
}
