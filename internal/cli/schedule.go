package cli

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/khmuhtadin/sapu/internal/agent"
	"github.com/khmuhtadin/sapu/internal/env"
	"github.com/khmuhtadin/sapu/internal/output"
)

const scheduleLong = `Scheduled cleanup moves caches that are marked safe to quarantine, every
day or every week, and shows a notification with the undo command.

It never touches moderate or risky items, never deletes for good and never
runs other programs. It does not expire the quarantine either: older runs
are deleted only when you run sapu yourself (clean, purge, apply, quarantine
expire, ...). A LaunchAgent (dev.sapu.agent) checks every hour whether a
clean is due; it is removed when scheduled cleanup and alerts are both off.`

func newScheduleCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "schedule",
		Short: "Clean safe caches on a schedule, always through quarantine",
		Long:  scheduleLong,
	}
	var every string
	on := &cobra.Command{
		Use:   "on",
		Short: "Turn on scheduled cleanup of safe caches",
		Long:  scheduleLong,
		RunE: func(cmd *cobra.Command, args []string) error {
			return finish(cmd, "schedule-on", runScheduleOn(cmd, args, every))
		},
	}
	on.Flags().StringVar(&every, "every", agent.EveryWeekly, "how often to clean: daily or weekly")
	cmd.AddCommand(on,
		&cobra.Command{
			Use:   "off",
			Short: "Turn off scheduled cleanup",
			RunE: func(cmd *cobra.Command, args []string) error {
				return finish(cmd, "schedule-off", runScheduleOff(cmd, args))
			},
		},
		&cobra.Command{
			Use:   "status",
			Short: "Show what is scheduled and how the last clean went",
			RunE: func(cmd *cobra.Command, args []string) error {
				return finish(cmd, "schedule-status", runAgentStatus(cmd, args, "schedule-status"))
			},
		},
	)
	return cmd
}

func runScheduleOn(cmd *cobra.Command, args []string, every string) error {
	if len(args) > 0 {
		return usageErr(fmt.Errorf("schedule on takes no arguments, got %q", args[0]))
	}
	if _, err := agent.Period(every); err != nil {
		return usageErr(fmt.Errorf("--every must be %s or %s, not %q", agent.EveryDaily, agent.EveryWeekly, every))
	}
	e, err := loadEnv()
	if err != nil {
		return err
	}
	c := configToChange(cmd, e)
	if cmd.Flags().Changed("every") {
		c.Schedule.Every = every
	}
	c.Schedule.Enabled = true
	if err := turnOn(e, c); err != nil {
		return err
	}
	fmt.Fprintf(humanOut(cmd), "Scheduled cleanup is on: %s, safe caches only, always into quarantine.\n\n", c.Schedule.Every)
	return printAgentStatus(cmd, e, "schedule-on")
}

func runScheduleOff(cmd *cobra.Command, args []string) error {
	if len(args) > 0 {
		return usageErr(fmt.Errorf("schedule off takes no arguments, got %q", args[0]))
	}
	e, err := loadEnv()
	if err != nil {
		return err
	}
	c := configToChange(cmd, e)
	c.Schedule.Enabled = false
	if err := turnOff(e, c); err != nil {
		return err
	}
	fmt.Fprintln(humanOut(cmd), "Scheduled cleanup is off.")
	fmt.Fprintln(humanOut(cmd))
	return printAgentStatus(cmd, e, "schedule-off")
}

// configToChange reads agent.json for on and off. An unreadable file is
// replaced by the defaults, with a note, so "on" can always repair it.
func configToChange(cmd *cobra.Command, e env.Env) agent.Config {
	c, err := agent.LoadConfig(e)
	if err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "sapu: %v\nStarting from the default settings.\n", err)
		return agent.DefaultConfig()
	}
	return c
}

// turnOn saves the config and installs (or updates) the agent for this sapu.
func turnOn(e env.Env, c agent.Config) error {
	if err := agent.SaveConfig(e, c); err != nil {
		return err
	}
	if err := agent.Install(e); err != nil {
		return fmt.Errorf("the settings are saved, but the agent could not be started: %w", err)
	}
	return nil
}

// turnOff saves the config and removes the agent when schedule and alerts
// are both off. While one stays on the agent is left alone: it reads the
// config on every run.
func turnOff(e env.Env, c agent.Config) error {
	if err := agent.SaveConfig(e, c); err != nil {
		return err
	}
	if c.Schedule.Enabled || c.Alerts.Enabled {
		return nil
	}
	if err := agent.Uninstall(e); err != nil {
		return fmt.Errorf("the settings are saved, but the agent could not be removed: %w", err)
	}
	return nil
}

// scheduleData is the data of schedule on, off and status with --json.
type scheduleData struct {
	Enabled   bool         `json:"enabled"`
	Every     string       `json:"every"`
	NextClean *time.Time   `json:"next_clean,omitempty"` // when on: earliest time the next clean runs
	LastClean *agent.Clean `json:"last_clean,omitempty"`
	LastCheck *time.Time   `json:"last_check,omitempty"` // last hourly run of the agent
	Agent     agent.Info   `json:"agent"`
}

// alertsData is the data of alerts on, off and status with --json.
type alertsData struct {
	Enabled         bool                 `json:"enabled"`
	MinFreeBytes    int64                `json:"min_free_bytes"`
	DropBytesPerDay int64                `json:"drop_bytes_per_day"`
	LastCheck       *time.Time           `json:"last_check,omitempty"`
	FreeBytes       *int64               `json:"free_bytes,omitempty"` // free space at the last sample
	Notified        map[string]time.Time `json:"notified,omitempty"`   // alert kind -> last notification
	Agent           agent.Info           `json:"agent"`
}

func runAgentStatus(cmd *cobra.Command, args []string, name string) error {
	if len(args) > 0 {
		return usageErr(errors.New("status takes no arguments"))
	}
	e, err := loadEnv()
	if err != nil {
		return err
	}
	return printAgentStatus(cmd, e, name)
}

// printAgentStatus shows the schedule, the alerts and the agent, or with
// --json the data for name ("schedule-..." or "alerts-..."). It only reads.
func printAgentStatus(cmd *cobra.Command, e env.Env, name string) error {
	c, err := agent.LoadConfig(e)
	if err != nil {
		return err
	}
	st, err := agent.LoadState(e)
	if err != nil {
		return err
	}
	info, err := agent.Inspect(e, c)
	if err != nil {
		return err
	}
	now := time.Now()
	next := nextClean(c, st, now)
	writeAgentStatus(humanOut(cmd), e, c, st, info, next, now)

	if strings.HasPrefix(name, "alerts") {
		d := alertsData{Enabled: c.Alerts.Enabled, MinFreeBytes: c.Alerts.MinFreeBytes,
			DropBytesPerDay: c.Alerts.DropBytesPerDay, LastCheck: st.LastCheck, Notified: st.Notified, Agent: info}
		if n := len(st.Samples); n > 0 {
			d.FreeBytes = &st.Samples[n-1].FreeBytes
		}
		return printData(cmd, name, d)
	}
	return printData(cmd, name, scheduleData{Enabled: c.Schedule.Enabled, Every: c.Schedule.Every,
		NextClean: next, LastClean: st.LastClean, LastCheck: st.LastCheck, Agent: info})
}

// nextClean is when the next scheduled clean runs at the earliest: one
// period after the last one, or now when it is due. Nil when off.
func nextClean(c agent.Config, st agent.State, now time.Time) *time.Time {
	if !c.Schedule.Enabled {
		return nil
	}
	period, err := agent.Period(c.Schedule.Every)
	if err != nil {
		return nil
	}
	next := now
	if st.LastClean != nil && st.LastClean.Time.Add(period).After(now) {
		next = st.LastClean.Time.Add(period)
	}
	return &next
}

func writeAgentStatus(w io.Writer, e env.Env, c agent.Config, st agent.State, info agent.Info, next *time.Time, now time.Time) {
	row := func(label, format string, args ...any) {
		fmt.Fprintf(w, "%-19s%s\n", label, fmt.Sprintf(format, args...))
	}
	when := func(t time.Time) string { return t.Local().Format("Mon 2 Jan 15:04") }

	if c.Schedule.Enabled {
		row("Scheduled cleanup", "on, %s: safe caches only, always into quarantine", c.Schedule.Every)
		if next != nil && next.After(now) {
			row("  Next clean", "after %s", when(*next))
		} else {
			row("  Next clean", "due; runs at the next hourly check")
		}
	} else {
		row("Scheduled cleanup", "off (turn on: sapu schedule on)")
	}
	if lc := st.LastClean; lc != nil {
		switch lc.Result {
		case agent.CleanMoved:
			row("  Last clean", "%s, moved %s (%s), run %s", when(lc.Time), output.Bytes(lc.Bytes), plural(lc.Items, "item"), lc.RunID)
			row("", "undo: sapu undo %s", lc.RunID)
		case agent.CleanNothing:
			row("  Last clean", "%s, nothing to move", when(lc.Time))
		default:
			row("  Last clean", "%s, failed: %s", when(lc.Time), lc.Error)
		}
	}

	if c.Alerts.Enabled {
		row("Alerts", "on: %s", alertRules(c.Alerts))
	} else {
		row("Alerts", "off (turn on: sapu alerts on)")
	}
	if st.LastCheck != nil {
		text := when(*st.LastCheck)
		if n := len(st.Samples); n > 0 {
			text += ", " + output.Bytes(st.Samples[n-1].FreeBytes) + " free"
		}
		row("Last check", "%s", text)
	}

	plist := output.Tilde(e.Home, info.Plist)
	switch {
	case info.Installed && info.Loaded:
		row("Agent", "%s, loaded", plist)
	case info.Installed:
		row("Agent", "%s, not loaded", plist)
	case info.Loaded:
		row("Agent", "loaded, but %s is missing", plist)
	default:
		row("Agent", "not installed")
	}
	if info.Program != "" {
		row("  Runs", "%s", info.Program)
	}
	for _, warn := range info.Warnings {
		fmt.Fprintf(w, "\nWarning: %s\n", warn)
	}
}

// alertRules says when alerts notify.
func alertRules(a agent.Alerts) string {
	switch {
	case a.MinFreeBytes > 0 && a.DropBytesPerDay > 0:
		return fmt.Sprintf("below %s free, or a drop of more than %s in a day", output.Bytes(a.MinFreeBytes), output.Bytes(a.DropBytesPerDay))
	case a.MinFreeBytes > 0:
		return fmt.Sprintf("below %s free", output.Bytes(a.MinFreeBytes))
	default:
		return fmt.Sprintf("a drop of more than %s in a day", output.Bytes(a.DropBytesPerDay))
	}
}
