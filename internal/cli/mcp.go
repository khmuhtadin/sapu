package cli

import (
	"errors"

	"github.com/spf13/cobra"

	"github.com/khmuhtadin/sapu/internal/mcp"
)

func newMCPCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "mcp",
		Short: "Serve Sapu's read-only tools to AI agents over MCP (stdio)",
		Long: `Serve Sapu's tools to AI agents (Claude Code, Codex, Cursor) over the Model
Context Protocol on stdin and stdout.

The server is read-only. It can explain, analyze and write a locked plan
file, but it has no tool that deletes, moves, undoes or runs anything. You
apply a plan yourself with "sapu apply FILE".

Add it to Claude Code with: claude mcp add sapu -- sapu mcp`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if jsonOut {
				return usageErr(errors.New("mcp always speaks JSON-RPC on stdout; drop --json"))
			}
			e, err := loadEnv()
			if err != nil {
				return err
			}
			s := mcp.Server{Env: e, Version: Version, Log: cmd.ErrOrStderr()}
			return s.Serve(cmd.Context(), cmd.InOrStdin(), cmd.OutOrStdout())
		},
	}
}
