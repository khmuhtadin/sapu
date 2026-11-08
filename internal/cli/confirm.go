package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/khmuhtadin/sapu/internal/output"
)

var (
	errNoAnswer = usageErr(errors.New("no answer on stdin, so nothing was changed; use --yes to go ahead without asking"))
	errDeclined = usageErr(errors.New("not confirmed, so nothing was changed; use --yes to skip the question"))
)

// humanOut is where human text goes: stdout, or nowhere with --json, so that
// stdout holds exactly one envelope.
func humanOut(cmd *cobra.Command) io.Writer {
	if jsonOut {
		return io.Discard
	}
	return cmd.OutOrStdout()
}

// confirmYes asks a yes/no question on stderr and reads the answer from
// stdin. Only "y" or "yes" is a yes; end of input is a no.
func confirmYes(cmd *cobra.Command, question string) error {
	fmt.Fprintf(cmd.ErrOrStderr(), "%s [y/N] ", question)
	answer, err := readAnswer(cmd)
	if err != nil {
		return err
	}
	if a := strings.ToLower(answer); a == "y" || a == "yes" {
		return nil
	}
	return errDeclined
}

// confirmDelete asks for the word "delete". Nothing else counts.
func confirmDelete(cmd *cobra.Command, question string) error {
	fmt.Fprintf(cmd.ErrOrStderr(), "%s Type \"delete\" to go on: ", question)
	answer, err := readAnswer(cmd)
	if err != nil {
		return err
	}
	if answer == "delete" {
		return nil
	}
	return errDeclined
}

func readAnswer(cmd *cobra.Command) (string, error) {
	line, err := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
	if err != nil && line == "" {
		fmt.Fprintln(cmd.ErrOrStderr())
		return "", errNoAnswer
	}
	return strings.TrimSpace(line), nil
}

// reported marks an error whose result was already printed (a data envelope
// with --json), so finish does not print a second envelope.
type reported struct{ error }

func (r reported) Unwrap() error { return r.error }

// finish turns an error of command name into an error envelope on stdout
// when --json is set, and returns err unchanged so the exit code is kept.
func finish(cmd *cobra.Command, name string, err error) error {
	if err == nil || !jsonOut {
		return err
	}
	var done reported
	if errors.As(err, &done) {
		return err
	}
	code := "error"
	var ee *exitError
	switch {
	case errors.Is(err, context.Canceled):
		code = "cancelled"
	case errors.As(err, &ee) && ee.code == output.ExitUsage:
		code = "usage"
	}
	text, friendlyCode := friendlyErr(err)
	if friendlyCode != "" {
		code = friendlyCode
	}
	if jerr := output.JSONError(cmd.OutOrStdout(), name, code, text); jerr != nil {
		return errors.Join(err, jerr)
	}
	return err
}

// printData prints the envelope of a successful command with --json.
func printData(cmd *cobra.Command, name string, data any) error {
	if !jsonOut {
		return nil
	}
	return output.JSON(cmd.OutOrStdout(), name, data)
}
