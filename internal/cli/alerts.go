package cli

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/khmuhtadin/sapu/internal/finders"
)

const alertsLong = `Alerts show a macOS notification when free space drops below --min-free,
or falls by more than --drop within 24 hours. Each alert is shown at most
once a day.

A LaunchAgent (dev.sapu.agent) checks free space every hour; it is removed
when alerts and scheduled cleanup are both off. Sizes are base 1000, as
Sapu prints them: 10GB is 10,000,000,000 bytes. 0 turns that alert off.`

func newAlertsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "alerts",
		Short: "Get a notification when free space runs low or drops fast",
		Long:  alertsLong,
	}
	var minFree, drop string
	on := &cobra.Command{
		Use:   "on",
		Short: "Turn on free-space alerts",
		Long:  alertsLong,
		RunE: func(cmd *cobra.Command, args []string) error {
			return finish(cmd, "alerts-on", runAlertsOn(cmd, args, minFree, drop))
		},
	}
	on.Flags().StringVar(&minFree, "min-free", "10GB", "alert when free space is below this size")
	on.Flags().StringVar(&drop, "drop", "5GB", "alert when free space falls by more than this in 24 hours")
	cmd.AddCommand(on,
		&cobra.Command{
			Use:   "off",
			Short: "Turn off free-space alerts",
			RunE: func(cmd *cobra.Command, args []string) error {
				return finish(cmd, "alerts-off", runAlertsOff(cmd, args))
			},
		},
		&cobra.Command{
			Use:   "status",
			Short: "Show the alert settings and the last check",
			RunE: func(cmd *cobra.Command, args []string) error {
				return finish(cmd, "alerts-status", runAgentStatus(cmd, args, "alerts-status"))
			},
		},
	)
	return cmd
}

func runAlertsOn(cmd *cobra.Command, args []string, minFree, drop string) error {
	if len(args) > 0 {
		return usageErr(fmt.Errorf("alerts on takes no arguments, got %q", args[0]))
	}
	minBytes, err := finders.ParseSize(minFree)
	if err != nil {
		return usageErr(fmt.Errorf("--min-free: %w", err))
	}
	dropBytes, err := finders.ParseSize(drop)
	if err != nil {
		return usageErr(fmt.Errorf("--drop: %w", err))
	}
	e, err := loadEnv()
	if err != nil {
		return err
	}
	c := configToChange(cmd, e)
	if cmd.Flags().Changed("min-free") {
		c.Alerts.MinFreeBytes = minBytes
	}
	if cmd.Flags().Changed("drop") {
		c.Alerts.DropBytesPerDay = dropBytes
	}
	if c.Alerts.MinFreeBytes == 0 && c.Alerts.DropBytesPerDay == 0 {
		return usageErr(errors.New("--min-free and --drop are both 0, so there is nothing to alert on"))
	}
	c.Alerts.Enabled = true
	if err := turnOn(e, c); err != nil {
		return err
	}
	fmt.Fprintf(humanOut(cmd), "Alerts are on: %s.\n\n", alertRules(c.Alerts))
	return printAgentStatus(cmd, e, "alerts-on")
}

func runAlertsOff(cmd *cobra.Command, args []string) error {
	if len(args) > 0 {
		return usageErr(fmt.Errorf("alerts off takes no arguments, got %q", args[0]))
	}
	e, err := loadEnv()
	if err != nil {
		return err
	}
	c := configToChange(cmd, e)
	c.Alerts.Enabled = false
	if err := turnOff(e, c); err != nil {
		return err
	}
	fmt.Fprintln(humanOut(cmd), "Alerts are off.")
	fmt.Fprintln(humanOut(cmd))
	return printAgentStatus(cmd, e, "alerts-off")
}
