package mcp

import (
	"context"
	"encoding/json"

	"github.com/khmuhtadin/sapu/internal/env"
)

// tool is one MCP tool. Every tool is read-only. The plan tools write one
// plan file, so they are the only ones with readOnly false.
type tool struct {
	name        string
	title       string
	description string
	readOnly    bool
	schema      string // JSON Schema of the arguments
	run         func(ctx context.Context, e env.Env, args json.RawMessage) (toolResult, error)
}

// toolResult is what a tool returns: text for the model to read and the same
// facts as a JSON object.
type toolResult struct {
	text string
	data any
}

func (r toolResult) message() map[string]any {
	return map[string]any{
		"content":           []map[string]any{{"type": "text", "text": r.text}},
		"structuredContent": r.data,
		"isError":           false,
	}
}

func toolErrorResult(err error) map[string]any {
	return map[string]any{
		"content": []map[string]any{{"type": "text", "text": err.Error()}},
		"isError": true,
	}
}

const noArgs = `{"type":"object","properties":{},"additionalProperties":false}`

const riskProperty = `"max_risk":{"type":"string","enum":["safe","moderate"],"default":"safe",` +
	`"description":"Highest risk to include. safe: caches and logs that apps rebuild. moderate: rebuilt too, but the next launch may be slower. Risky items are never included."}`

var tools = []tool{
	{
		name:  "summary",
		title: "Disk summary",
		description: "Show free disk space, how much Sapu could reclaim by risk level (safe, moderate, risky), " +
			"how many items are protected, and whether Full Disk Access is on. Only reads. Takes up to a minute.",
		readOnly: true,
		schema:   noArgs,
		run:      runSummary,
	},
	{
		name:  "explain",
		title: "Explain System Data",
		description: "Break down what fills macOS \"System Data\" (Time Machine snapshots, Xcode, Docker, iOS backups, " +
			"caches and more) with the size of each part and the official way to reclaim it. " +
			"Commands in the result are for the user to run. Sapu never runs them. Only reads. Can take a minute.",
		readOnly: true,
		schema:   noArgs,
		run:      runExplain,
	},
	{
		name:  "analyze",
		title: "Largest folders and files",
		description: "List the largest folders and files under a path, as a tree. Defaults to the home folder, " +
			"top 20 entries per folder, 2 levels deep. Only reads.",
		readOnly: true,
		schema: `{"type":"object","additionalProperties":false,"properties":{` +
			`"path":{"type":"string","description":"Absolute path, or ~ for the home folder. Default: the home folder."},` +
			`"top":{"type":"integer","minimum":1,"maximum":100,"default":20,"description":"Entries to keep per folder."},` +
			`"depth":{"type":"integer","minimum":1,"maximum":4,"default":2,"description":"Folder levels to show below the path."}}}`,
		run: runAnalyze,
	},
	{
		name:  "plan_clean",
		title: "Prepare a cleanup plan",
		description: "Find caches and logs that apps rebuild and write a locked plan file listing exactly what a cleanup " +
			"would move to Sapu's quarantine. This tool moves nothing. Protected, whitelisted and in-use items are left out. " +
			"The result names the file and the command the user must run themselves to apply it: sapu apply FILE. " +
			"Only safe and moderate items can be planned. Takes up to a minute.",
		readOnly: false,
		schema:   `{"type":"object","additionalProperties":false,"properties":{` + riskProperty + `}}`,
		run:      runPlanClean,
	},
	{
		name:  "plan_purge",
		title: "Prepare a build-artifact plan",
		description: "Find build artifacts (node_modules, target, .venv, build folders and more) inside project folders and " +
			"write a locked plan file listing exactly what would move to Sapu's quarantine. This tool moves nothing. " +
			"Source files are never selected. The result names the file and the command the user must run themselves to apply it: sapu apply FILE.",
		readOnly: false,
		schema: `{"type":"object","additionalProperties":false,"required":["paths"],"properties":{` +
			`"paths":{"type":"array","minItems":1,"items":{"type":"string"},"description":"Absolute project folders to search, or paths starting with ~."},` +
			`"older_than":{"type":"string","description":"Only projects not touched for this long, e.g. 30d or 2w (units m, h, d, w)."},` +
			riskProperty + `}}`,
		run: runPlanPurge,
	},
	{
		name:        "history",
		title:       "Past runs",
		description: "List past Sapu runs: id, kind, time, items, size and status (done, interrupted, undone, expired). Only reads.",
		readOnly:    true,
		schema:      noArgs,
		run:         runHistory,
	},
	{
		name:  "quarantine",
		title: "Quarantine contents",
		description: "Show what is held in Sapu's quarantine: one row per run with its size and the earliest time it may expire. " +
			"Only reads.",
		readOnly: true,
		schema:   noArgs,
		run:      runQuarantine,
	},
}

func findTool(name string) *tool {
	for i := range tools {
		if tools[i].name == name {
			return &tools[i]
		}
	}
	return nil
}

func toolList() []map[string]any {
	list := make([]map[string]any, 0, len(tools))
	for _, t := range tools {
		list = append(list, map[string]any{
			"name":        t.name,
			"title":       t.title,
			"description": t.description,
			"inputSchema": json.RawMessage(t.schema),
			"annotations": map[string]any{
				"title":           t.title,
				"readOnlyHint":    t.readOnly,
				"destructiveHint": false,
				"idempotentHint":  t.readOnly,
				"openWorldHint":   false,
			},
		})
	}
	return list
}
