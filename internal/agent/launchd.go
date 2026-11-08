package agent

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/khmuhtadin/sapu/internal/env"
	"github.com/khmuhtadin/sapu/internal/fsop"
	"github.com/khmuhtadin/sapu/internal/safefile"
)

// Launchctl runs /bin/launchctl with args. It returns nil when launchctl
// exits 0. Tests replace it; they must never reach the real launchd.
var Launchctl = func(args ...string) error {
	out, err := exec.Command("/bin/launchctl", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("launchctl %s: %w: %s", strings.Join(args, " "), err, bytes.TrimSpace(out))
	}
	return nil
}

// Executable returns the absolute path of the running sapu, symlinks
// resolved, for the plist. Tests replace it.
var Executable = func() (string, error) {
	p, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(p)
}

func domain() string  { return fmt.Sprintf("gui/%d", os.Getuid()) }
func service() string { return domain() + "/" + Label }

// Plist is the LaunchAgent that runs "<program> agent run" every hour, with
// its output in agent.log and SAPU_HOME set to e's state folder, so the
// agent reads the config that sapu schedule and sapu alerts wrote. When the
// user set SAPU_CONFIG, the plist passes it too, so the agent reads the same
// sapu.toml.
func Plist(e env.Env, program string) string {
	s := func(v string) string {
		var b strings.Builder
		_ = xml.EscapeText(&b, []byte(v)) // a strings.Builder never fails
		return "<string>" + b.String() + "</string>"
	}
	vars := "<key>SAPU_HOME</key>\n\t\t" + s(e.StateDir)
	if e.ConfigFromEnv {
		vars += "\n\t\t<key>SAPU_CONFIG</key>\n\t\t" + s(e.ConfigFile)
	}
	return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	` + s(Label) + `
	<key>ProgramArguments</key>
	<array>
		` + s(program) + `
		<string>agent</string>
		<string>run</string>
	</array>
	<key>StartInterval</key>
	<integer>` + fmt.Sprint(int(Interval.Seconds())) + `</integer>
	<key>RunAtLoad</key>
	<false/>
	<key>ProcessType</key>
	<string>Background</string>
	<key>EnvironmentVariables</key>
	<dict>
		` + vars + `
	</dict>
	<key>StandardOutPath</key>
	` + s(LogPath(e)) + `
	<key>StandardErrorPath</key>
	` + s(LogPath(e)) + `
</dict>
</plist>
`
}

// Install writes the plist for the running sapu and bootstraps it, booting
// out the loaded copy first.
func Install(e env.Env) error {
	program, err := Executable()
	if err != nil {
		return fmt.Errorf("find the sapu program: %w", err)
	}
	if !filepath.IsAbs(program) {
		return fmt.Errorf("the sapu program %q is not an absolute path", program)
	}
	path := PlistPath(e)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if err := safefile.Write(filepath.Dir(path), path, []byte(Plist(e, program)), 0o600); err != nil {
		return err
	}
	if Loaded() {
		if err := Launchctl("bootout", service()); err != nil {
			return err
		}
	}
	return Launchctl("bootstrap", domain(), path)
}

// Uninstall boots the agent out and removes the plist through
// fsop.RemoveOwnLaunchAgent.
func Uninstall(e env.Env) error {
	if Loaded() {
		if err := Launchctl("bootout", service()); err != nil {
			return err
		}
	}
	return fsop.RemoveOwnLaunchAgent(e, Label)
}

// Loaded reports whether launchd has the agent loaded in this user's
// session: launchctl print exits 0. It only reads.
func Loaded() bool {
	return Launchctl("print", service()) == nil
}

// Info is what the LaunchAgent looks like now.
type Info struct {
	Plist          string   `json:"plist"`
	Installed      bool     `json:"installed"`         // the plist exists
	Loaded         bool     `json:"loaded"`            // launchd has it loaded
	Program        string   `json:"program,omitempty"` // the sapu the plist runs
	ProgramMissing bool     `json:"program_missing"`   // that sapu no longer exists
	Warnings       []string `json:"warnings"`
}

// Inspect reads the plist and asks launchd whether the agent is loaded. It
// changes nothing. Warnings say what to run when the agent does not match c.
func Inspect(e env.Env, c Config) (Info, error) {
	info := Info{Plist: PlistPath(e), Loaded: Loaded(), Warnings: []string{}}
	b, err := os.ReadFile(info.Plist)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return info, err
	default:
		info.Installed = true
		info.Program = programOf(b)
		if info.Program == "" {
			info.ProgramMissing = true
		} else if _, err := os.Stat(info.Program); errors.Is(err, fs.ErrNotExist) {
			info.ProgramMissing = true
		}
	}

	again := "sapu alerts on"
	if c.Schedule.Enabled {
		again = "sapu schedule on"
	}
	on := c.Schedule.Enabled || c.Alerts.Enabled
	switch {
	case on && info.ProgramMissing:
		what := "a sapu program"
		if info.Program != "" {
			what = info.Program
		}
		info.Warnings = append(info.Warnings, fmt.Sprintf(
			"The agent runs %s, which no longer exists (moved or upgraded?). Run %s again to use this sapu.", what, again))
	case on && (!info.Installed || !info.Loaded):
		info.Warnings = append(info.Warnings, fmt.Sprintf(
			"The agent is not running, so nothing happens on schedule. Run %s again.", again))
	case !on && (info.Installed || info.Loaded):
		info.Warnings = append(info.Warnings,
			"Schedule and alerts are off, but the agent is still installed. Run sapu schedule off to remove it.")
	}
	return info, nil
}

// programOf returns the first ProgramArguments string of a plist, or "".
func programOf(plist []byte) string {
	d := xml.NewDecoder(bytes.NewReader(plist))
	key := ""
	for {
		tok, err := d.Token()
		if err != nil {
			return ""
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		switch se.Name.Local {
		case "key":
			if err := d.DecodeElement(&key, &se); err != nil {
				return ""
			}
		case "string":
			var s string
			if err := d.DecodeElement(&s, &se); err != nil {
				return ""
			}
			if key == "ProgramArguments" {
				return s
			}
		}
	}
}
