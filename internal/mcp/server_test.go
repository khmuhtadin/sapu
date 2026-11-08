package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/khmuhtadin/sapu/internal/env"
)

type reply struct {
	ID     json.RawMessage `json:"id"`
	Result map[string]any  `json:"result"`
	Error  *rpcError       `json:"error"`
}

// serve feeds the lines to a server on e and returns its replies. stdout
// must hold nothing but one JSON message per line.
func serve(t *testing.T, e env.Env, input string) []reply {
	t.Helper()
	var out, logs bytes.Buffer
	s := Server{Env: e, Version: "test", Log: &logs}
	if err := s.Serve(context.Background(), strings.NewReader(input), &out); err != nil {
		t.Fatalf("Serve: %v", err)
	}
	var replies []reply
	for _, l := range strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n") {
		if l == "" {
			continue
		}
		var r reply
		if err := json.Unmarshal([]byte(l), &r); err != nil {
			t.Fatalf("stdout line is not a JSON message: %v\n%s", err, l)
		}
		replies = append(replies, r)
	}
	return replies
}

func lines(msgs ...string) string { return strings.Join(msgs, "\n") + "\n" }

// writeLines sends one JSON message per line to the server's stdin.
func writeLines(t *testing.T, w io.Writer, msgs ...string) {
	t.Helper()
	if _, err := io.WriteString(w, lines(msgs...)); err != nil {
		t.Fatal(err)
	}
}

func TestProtocol(t *testing.T) {
	cases := []struct {
		name  string
		in    string
		code  int    // JSON-RPC error code, 0 for a result
		check string // text the reply must hold
	}{
		{"initialize", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`, 0, `"protocolVersion":"2025-06-18"`},
		{"initialize with a newer client", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2030-01-01"}}`, 0, `"protocolVersion":"2025-06-18"`},
		{"initialize without params", `{"jsonrpc":"2.0","id":1,"method":"initialize"}`, codeInvalidParams, ""},
		{"ping", `{"jsonrpc":"2.0","id":1,"method":"ping"}`, 0, `{}`},
		{"tools/list", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, 0, `"plan_clean"`},
		{"string id", `{"jsonrpc":"2.0","id":"abc","method":"ping"}`, 0, `{}`},
		{"unknown method", `{"jsonrpc":"2.0","id":1,"method":"resources/list"}`, codeMethodNotFound, ""},
		{"tools/call without params", `{"jsonrpc":"2.0","id":1,"method":"tools/call"}`, codeInvalidParams, ""},
		{"tools/call without a name", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"arguments":{}}}`, codeInvalidParams, ""},
		{"unknown tool", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"apply"}}`, codeInvalidParams, "unknown tool"},
		{"arguments not an object", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"history","arguments":[1]}}`, codeInvalidParams, ""},
		{"unknown argument", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"history","arguments":{"yes":true}}}`, codeInvalidParams, "yes"},
		{"wrong argument type", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"analyze","arguments":{"top":"many"}}}`, codeInvalidParams, ""},
		{"parse error", `{"jsonrpc":"2.0","id":1,"method":`, codeParse, ""},
		{"batch", `[{"jsonrpc":"2.0","id":1,"method":"ping"}]`, codeInvalidRequest, ""},
		{"request without a method", `{"jsonrpc":"2.0","id":1}`, codeInvalidRequest, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			replies := serve(t, env.New(t.TempDir()), lines(c.in))
			if len(replies) != 1 {
				t.Fatalf("got %d replies, want 1: %+v", len(replies), replies)
			}
			r := replies[0]
			if c.code == 0 {
				if r.Error != nil || r.Result == nil {
					t.Fatalf("want a result, got %+v", r)
				}
			} else if r.Error == nil || r.Error.Code != c.code {
				t.Fatalf("want error %d, got %+v", c.code, r)
			}
			b, _ := json.Marshal(r)
			if !strings.Contains(string(b), c.check) {
				t.Errorf("reply lacks %q: %s", c.check, b)
			}
		})
	}
}

func TestReplyKeepsTheRequestID(t *testing.T) {
	replies := serve(t, env.New(t.TempDir()), lines(
		`{"jsonrpc":"2.0","id":"abc","method":"ping"}`,
		`{"jsonrpc":"2.0","id":7,"method":"ping"}`,
		`{"jsonrpc":"2.0","id":1,"method":"nope"}`,
	))
	var ids []string
	for _, r := range replies {
		ids = append(ids, string(r.ID))
	}
	if got := strings.Join(ids, " "); got != `"abc" 7 1` {
		t.Errorf("reply ids %s", got)
	}
}

func TestInitializeResult(t *testing.T) {
	replies := serve(t, env.New(t.TempDir()), lines(
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`))
	r := replies[0].Result
	info := r["serverInfo"].(map[string]any)
	if info["name"] != "sapu" || info["version"] != "test" {
		t.Errorf("serverInfo %v", info)
	}
	if _, ok := r["capabilities"].(map[string]any)["tools"]; !ok {
		t.Errorf("capabilities %v lack tools", r["capabilities"])
	}
	text := r["instructions"].(string)
	for _, want := range []string{"never deletes", "sapu apply", "read-only"} {
		if !strings.Contains(text, want) {
			t.Errorf("instructions lack %q: %s", want, text)
		}
	}
}

func TestNotificationsGetNoReply(t *testing.T) {
	replies := serve(t, env.New(t.TempDir()), lines(
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":99}}`,
		`{"jsonrpc":"2.0","method":"notifications/cancelled","params":"not an object"}`,
		`{"jsonrpc":"2.0","method":"something/else"}`,
		``,
		`{"jsonrpc":"2.0","id":1,"method":"ping"}`,
	))
	if len(replies) != 1 {
		t.Fatalf("got %d replies, want only the ping's: %+v", len(replies), replies)
	}
}

func TestEOF(t *testing.T) {
	// No input: the server ends at once without a reply.
	if replies := serve(t, env.New(t.TempDir()), ""); len(replies) != 0 {
		t.Errorf("replies %+v", replies)
	}
	// A last message without a newline is still answered.
	replies := serve(t, env.New(t.TempDir()), `{"jsonrpc":"2.0","id":1,"method":"ping"}`)
	if len(replies) != 1 {
		t.Errorf("got %d replies, want 1", len(replies))
	}
}

// EOF right after a tool call must not drop the call's answer.
func TestEOFWaitsForToolCalls(t *testing.T) {
	replies := serve(t, env.New(t.TempDir()), lines(
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"history"}}`))
	if len(replies) != 1 || replies[0].Result == nil {
		t.Fatalf("replies %+v", replies)
	}
}

// blockingTool is added for one test: it runs until it is cancelled.
func addBlockingTool(t *testing.T, started chan<- struct{}) {
	t.Helper()
	old := tools
	t.Cleanup(func() { tools = old })
	tools = append(old[:len(old):len(old)], tool{
		name:   "block",
		schema: noArgs,
		run: func(ctx context.Context, e env.Env, args json.RawMessage) (toolResult, error) {
			started <- struct{}{}
			<-ctx.Done()
			return toolResult{}, ctx.Err()
		},
	})
}

func TestCancelledToolCallGetsNoReply(t *testing.T) {
	started := make(chan struct{})
	addBlockingTool(t, started)

	pr, pw := io.Pipe()
	var out bytes.Buffer
	done := make(chan error)
	go func() {
		s := Server{Env: env.New(t.TempDir()), Version: "test"}
		done <- s.Serve(context.Background(), pr, &out)
	}()

	writeLines(t, pw, `{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"block"}}`)
	<-started
	writeLines(t, pw,
		`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":5,"reason":"test"}}`,
		`{"jsonrpc":"2.0","id":6,"method":"ping"}`)
	if err := pw.Close(); err != nil {
		t.Fatal(err)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Serve did not end after the call was cancelled and stdin closed")
	}
	if got := strings.TrimSpace(out.String()); !strings.HasPrefix(got, `{"jsonrpc":"2.0","id":6`) || strings.Count(got, "\n") != 0 {
		t.Errorf("want only the ping's reply, got:\n%s", got)
	}
}

func TestCancelledContextEndsServe(t *testing.T) {
	started := make(chan struct{})
	addBlockingTool(t, started)

	pr, pw := io.Pipe()
	defer func() { _ = pw.Close() }()
	var out bytes.Buffer
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error)
	go func() {
		s := Server{Env: env.New(t.TempDir()), Version: "test"}
		done <- s.Serve(ctx, pr, &out)
	}()

	writeLines(t, pw, `{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"block"}}`)
	<-started
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Error("Serve returned nil for a cancelled context")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Serve did not end after its context was cancelled")
	}
	if out.Len() != 0 {
		t.Errorf("a cancelled call answered: %s", out.String())
	}
}

func TestToolList(t *testing.T) {
	replies := serve(t, env.New(t.TempDir()), lines(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	list := replies[0].Result["tools"].([]any)

	var names []string
	for _, v := range list {
		tl := v.(map[string]any)
		names = append(names, tl["name"].(string))
		if tl["description"].(string) == "" || tl["title"].(string) == "" {
			t.Errorf("%v needs a title and a description", tl["name"])
		}
		schema := tl["inputSchema"].(map[string]any)
		if schema["type"] != "object" || schema["additionalProperties"] != false {
			t.Errorf("%v: inputSchema %v must be a closed object", tl["name"], schema)
		}
		ann := tl["annotations"].(map[string]any)
		writesPlan := strings.HasPrefix(tl["name"].(string), "plan_")
		if ann["readOnlyHint"] == writesPlan || ann["destructiveHint"] != false {
			t.Errorf("%v: annotations %v", tl["name"], ann)
		}
	}
	want := "summary explain analyze plan_clean plan_purge history quarantine"
	if got := strings.Join(names, " "); got != want {
		t.Errorf("tools %q, want exactly %q", got, want)
	}
}

// No tool may suggest that it deletes, and no tool takes a confirmation flag.
func TestNoToolSuggestsDeleting(t *testing.T) {
	badName := regexp.MustCompile(`(?i)delete|remove|apply|undo|empty|expire|erase|wipe|trash|run|exec|confirm|yes`)
	badText := regexp.MustCompile(`(?i)\b(delete[sd]?|remov(e|es|ed|ing)|erase[sd]?|wipe[sd]?|destroy(s|ed)?|free up)\b`)
	badArg := regexp.MustCompile(`(?i)yes|confirm|force|apply|permanent|run|command`)
	for _, tl := range tools {
		if badName.MatchString(tl.name) {
			t.Errorf("tool name %q suggests a change", tl.name)
		}
		if m := badText.FindString(tl.description + " " + tl.title); m != "" {
			t.Errorf("tool %s: description says %q", tl.name, m)
		}
		var schema struct {
			Properties map[string]json.RawMessage `json:"properties"`
		}
		if err := json.Unmarshal([]byte(tl.schema), &schema); err != nil {
			t.Fatalf("%s: schema is not JSON: %v", tl.name, err)
		}
		for arg := range schema.Properties {
			if badArg.MatchString(arg) {
				t.Errorf("tool %s takes %q, which looks like a confirmation or action", tl.name, arg)
			}
		}
	}
}
