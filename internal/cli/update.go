package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/Manifestro/awp/internal/updater"
)

func runUpdate(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "update requires a subcommand: check, install, or auto")
		return 2
	}
	switch args[0] {
	case "check":
		return runUpdateCheck(args[1:], stdout, stderr)
	case "install":
		return runUpdateInstall(args[1:], stdout, stderr)
	case "auto":
		return runUpdateAuto(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "unknown update subcommand %q\n", args[0])
		return 2
	}
}

func runUpdateCheck(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("update check", flag.ContinueOnError)
	flags.SetOutput(stderr)
	jsonOutput := flags.Bool("json", false, "print machine-readable JSON")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	status, err := updater.DefaultClient().Check(context.Background(), Version)
	if err != nil {
		return commandError("update.check", "update_check_failed", err, *jsonOutput, stdout, stderr)
	}
	if *jsonOutput {
		return writeJSON(stdout, result{OK: true, Command: "update.check", Data: status})
	}
	if status.UpdateAvailable {
		fmt.Fprintf(stdout, "AWP %s is available (current %s): %s\n", status.LatestVersion, status.CurrentVersion, status.ReleaseURL)
	} else {
		fmt.Fprintf(stdout, "AWP %s is up to date.\n", status.CurrentVersion)
	}
	return 0
}

func runUpdateInstall(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("update install", flag.ContinueOnError)
	flags.SetOutput(stderr)
	jsonOutput := flags.Bool("json", false, "print machine-readable JSON")
	executable := flags.String("executable", "", "executable to replace (primarily for testing)")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	client := updater.DefaultClient()
	status, err := client.Check(context.Background(), Version)
	if err != nil {
		return commandError("update.install", "update_check_failed", err, *jsonOutput, stdout, stderr)
	}
	if !status.UpdateAvailable {
		if *jsonOutput {
			return writeJSON(stdout, result{OK: true, Command: "update.install", Data: map[string]any{"updated": false, "version": Version}})
		}
		fmt.Fprintf(stdout, "AWP %s is already up to date.\n", Version)
		return 0
	}
	target := *executable
	if target == "" {
		target, err = os.Executable()
		if err != nil {
			return commandError("update.install", "executable_path", err, *jsonOutput, stdout, stderr)
		}
	}
	if err := client.Install(context.Background(), status.LatestVersion, target); err != nil {
		return commandError("update.install", "update_install_failed", err, *jsonOutput, stdout, stderr)
	}
	if *jsonOutput {
		return writeJSON(stdout, result{OK: true, Command: "update.install", Data: map[string]any{"updated": true, "version": status.LatestVersion, "executable": target}})
	}
	fmt.Fprintf(stdout, "AWP %s installed. Restart running AWP daemons to use it.\n", status.LatestVersion)
	return 0
}

func runUpdateAuto(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "update auto requires enable, disable, or status")
		return 2
	}
	command := args[0]
	flags := flag.NewFlagSet("update auto "+command, flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", "", "config path used to locate update policy")
	policyPathFlag := flags.String("policy", "", "update policy file path")
	interval := flags.Int("interval-hours", 24, "automatic update check interval in hours")
	jsonOutput := flags.Bool("json", false, "print machine-readable JSON")
	if err := flags.Parse(args[1:]); err != nil {
		return 2
	}
	path, err := updater.PolicyPath(*configPath, *policyPathFlag)
	if err != nil {
		return commandError("update.auto."+command, "policy_path", err, *jsonOutput, stdout, stderr)
	}
	policy, err := updater.LoadPolicy(path)
	if err != nil {
		return commandError("update.auto."+command, "policy_read", err, *jsonOutput, stdout, stderr)
	}
	switch command {
	case "enable":
		if *interval < 1 {
			return commandError("update.auto.enable", "invalid_interval", fmt.Errorf("--interval-hours must be positive"), *jsonOutput, stdout, stderr)
		}
		policy.Enabled = true
		policy.IntervalHours = *interval
	case "disable":
		policy.Enabled = false
	case "status":
	default:
		fmt.Fprintf(stderr, "unknown update auto subcommand %q\n", command)
		return 2
	}
	if command != "status" {
		if err := updater.SavePolicy(path, policy); err != nil {
			return commandError("update.auto."+command, "policy_write", err, *jsonOutput, stdout, stderr)
		}
	}
	if *jsonOutput {
		return writeJSON(stdout, result{OK: true, Command: "update.auto." + command, Data: map[string]any{"path": path, "policy": policy}})
	}
	fmt.Fprintf(stdout, "Automatic updates: enabled=%t interval=%dh\n", policy.Enabled, policy.IntervalHours)
	return 0
}

func runAutomaticUpdate(currentVersion, configPath, policyPathFlag string, output io.Writer) {
	path, err := updater.PolicyPath(configPath, policyPathFlag)
	if err != nil {
		fmt.Fprintf(output, "AWP automatic update: %v\n", err)
		return
	}
	policy, err := updater.LoadPolicy(path)
	if err != nil || !updater.Due(policy, time.Now()) {
		if err != nil {
			fmt.Fprintf(output, "AWP automatic update: %v\n", err)
		}
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	client := updater.DefaultClient()
	comparisonVersion := currentVersion
	if policy.LastInstalledVersion != "" {
		comparisonVersion = policy.LastInstalledVersion
	}
	status, checkErr := client.Check(ctx, comparisonVersion)
	policy.LastCheckedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if checkErr == nil && status.UpdateAvailable {
		target, pathErr := os.Executable()
		if pathErr != nil {
			checkErr = pathErr
		} else if installErr := client.Install(ctx, status.LatestVersion, target); installErr != nil {
			checkErr = installErr
		} else {
			policy.LastInstalledVersion = status.LatestVersion
			fmt.Fprintf(output, "AWP automatic update installed %s; restart the daemon to activate it.\n", status.LatestVersion)
		}
	}
	if saveErr := updater.SavePolicy(path, policy); checkErr == nil {
		checkErr = saveErr
	}
	if checkErr != nil {
		fmt.Fprintf(output, "AWP automatic update: %v\n", checkErr)
	}
}
