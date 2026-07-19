package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/Manifestro/awp/internal/autostart"
	"github.com/Manifestro/awp/internal/config"
	"github.com/Manifestro/awp/internal/sessions"
)

func runAutostart(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "autostart requires a subcommand: enable, status, or disable")
		return 2
	}
	if runtime.GOOS != "darwin" {
		return commandError("autostart."+args[0], "unsupported_platform", fmt.Errorf("autostart currently supports macOS launchd; use awp connect --reconnect on %s", runtime.GOOS), hasJSON(args[1:]), stdout, stderr)
	}
	switch args[0] {
	case "enable":
		return runAutostartEnable(args[1:], stdout, stderr)
	case "status":
		return runAutostartStatus(args[1:], stdout, stderr)
	case "disable":
		return runAutostartDisable(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "unknown autostart subcommand %q\n", args[0])
		return 2
	}
}

type autostartFlags struct {
	sessionID  *string
	configPath *string
	storePath  *string
	directory  *string
	jsonOutput *bool
}

func addAutostartFlags(flags *flag.FlagSet) autostartFlags {
	return autostartFlags{
		sessionID:  flags.String("session-id", "", "AWP session to run in the background"),
		configPath: flags.String("config", "", "config file path"),
		storePath:  flags.String("store", "", "session registry file path"),
		directory:  flags.String("directory", "", "launch agent directory (primarily for testing)"),
		jsonOutput: flags.Bool("json", false, "print machine-readable JSON"),
	}
}

func runAutostartEnable(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("autostart enable", flag.ContinueOnError)
	flags.SetOutput(stderr)
	common := addAutostartFlags(flags)
	startNow := flags.Bool("start-now", false, "load and start the service immediately")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	paths, cfg, err := resolveAutostart(*common.sessionID, *common.configPath, *common.storePath, *common.directory)
	if err != nil {
		return commandError("autostart.enable", "invalid_configuration", err, *common.jsonOutput, stdout, stderr)
	}
	token := strings.TrimSpace(os.Getenv(cfg.TokenEnv))
	if token == "" {
		return commandError("autostart.enable", "token_missing", fmt.Errorf("%s is not set", cfg.TokenEnv), *common.jsonOutput, stdout, stderr)
	}
	if err := secureWrite(paths.token, []byte(token+"\n")); err != nil {
		return commandError("autostart.enable", "token_write", err, *common.jsonOutput, stdout, stderr)
	}
	manifest, err := autostart.RenderLaunchd(autostart.LaunchdOptions{
		BinaryPath: paths.binary, ConfigPath: paths.config, StorePath: paths.store,
		TokenFile: paths.token, SessionID: *common.sessionID, LogPath: paths.log, PathEnv: os.Getenv("PATH"),
	})
	if err != nil {
		return commandError("autostart.enable", "manifest_render", err, *common.jsonOutput, stdout, stderr)
	}
	if err := secureWrite(paths.manifest, manifest); err != nil {
		return commandError("autostart.enable", "manifest_write", err, *common.jsonOutput, stdout, stderr)
	}
	if *startNow {
		domain := "gui/" + strconv.Itoa(os.Getuid())
		_ = exec.Command("launchctl", "bootout", domain, paths.manifest).Run()
		if output, loadErr := exec.Command("launchctl", "bootstrap", domain, paths.manifest).CombinedOutput(); loadErr != nil {
			return commandError("autostart.enable", "launchctl_bootstrap", fmt.Errorf("%w: %s", loadErr, strings.TrimSpace(string(output))), *common.jsonOutput, stdout, stderr)
		}
	}
	data := map[string]any{"enabled": true, "started": *startNow, "session_id": *common.sessionID, "manifest": paths.manifest, "token_file": paths.token}
	if *common.jsonOutput {
		return writeJSON(stdout, result{OK: true, Command: "autostart.enable", Data: data})
	}
	fmt.Fprintf(stdout, "Autostart enabled for %s. started=%t\n", *common.sessionID, *startNow)
	return 0
}

func runAutostartStatus(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("autostart status", flag.ContinueOnError)
	flags.SetOutput(stderr)
	common := addAutostartFlags(flags)
	if err := flags.Parse(args); err != nil {
		return 2
	}
	paths, _, err := resolveAutostart(*common.sessionID, *common.configPath, *common.storePath, *common.directory)
	if err != nil {
		return commandError("autostart.status", "invalid_configuration", err, *common.jsonOutput, stdout, stderr)
	}
	_, statErr := os.Stat(paths.manifest)
	enabled := statErr == nil
	if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return commandError("autostart.status", "manifest_read", statErr, *common.jsonOutput, stdout, stderr)
	}
	domainTarget := "gui/" + strconv.Itoa(os.Getuid()) + "/" + autostart.Label(*common.sessionID)
	loaded := exec.Command("launchctl", "print", domainTarget).Run() == nil
	data := map[string]any{"enabled": enabled, "loaded": loaded, "session_id": *common.sessionID, "manifest": paths.manifest}
	if *common.jsonOutput {
		return writeJSON(stdout, result{OK: true, Command: "autostart.status", Data: data})
	}
	fmt.Fprintf(stdout, "Autostart for %s: enabled=%t loaded=%t\n", *common.sessionID, enabled, loaded)
	return 0
}

func runAutostartDisable(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("autostart disable", flag.ContinueOnError)
	flags.SetOutput(stderr)
	common := addAutostartFlags(flags)
	if err := flags.Parse(args); err != nil {
		return 2
	}
	paths, _, err := resolveAutostart(*common.sessionID, *common.configPath, *common.storePath, *common.directory)
	if err != nil {
		return commandError("autostart.disable", "invalid_configuration", err, *common.jsonOutput, stdout, stderr)
	}
	domain := "gui/" + strconv.Itoa(os.Getuid())
	_ = exec.Command("launchctl", "bootout", domain, paths.manifest).Run()
	removed := false
	if removeErr := os.Remove(paths.manifest); removeErr == nil {
		removed = true
	} else if !errors.Is(removeErr, os.ErrNotExist) {
		return commandError("autostart.disable", "manifest_remove", removeErr, *common.jsonOutput, stdout, stderr)
	}
	data := map[string]any{"enabled": false, "removed": removed, "session_id": *common.sessionID, "manifest": paths.manifest, "token_file": paths.token}
	if *common.jsonOutput {
		return writeJSON(stdout, result{OK: true, Command: "autostart.disable", Data: data})
	}
	fmt.Fprintf(stdout, "Autostart disabled for %s. The protected token file remains at %s.\n", *common.sessionID, paths.token)
	return 0
}

type autostartPaths struct{ binary, config, store, token, log, manifest string }

func resolveAutostart(sessionID, configPath, storePath, directory string) (autostartPaths, config.Config, error) {
	if sessionID == "" {
		return autostartPaths{}, config.Config{}, fmt.Errorf("--session-id is required")
	}
	resolvedConfig, err := config.Path(configPath)
	if err != nil {
		return autostartPaths{}, config.Config{}, err
	}
	cfg, err := config.Load(resolvedConfig)
	if err != nil {
		return autostartPaths{}, config.Config{}, err
	}
	if err := config.Validate(cfg); err != nil {
		return autostartPaths{}, config.Config{}, err
	}
	resolvedStore, err := sessions.Path(resolvedConfig, storePath)
	if err != nil {
		return autostartPaths{}, config.Config{}, err
	}
	registry, err := sessions.Load(resolvedStore)
	if err != nil {
		return autostartPaths{}, config.Config{}, err
	}
	if _, found := sessions.Get(registry, sessionID); !found {
		return autostartPaths{}, config.Config{}, fmt.Errorf("AWP session %s is not bound locally", sessionID)
	}
	binary, err := os.Executable()
	if err != nil {
		return autostartPaths{}, config.Config{}, err
	}
	if directory == "" {
		home, homeErr := os.UserHomeDir()
		if homeErr != nil {
			return autostartPaths{}, config.Config{}, homeErr
		}
		directory = filepath.Join(home, "Library", "LaunchAgents")
	}
	stateDir := filepath.Dir(resolvedConfig)
	return autostartPaths{
		binary: binary, config: resolvedConfig, store: resolvedStore,
		token:    filepath.Join(stateDir, "autostart.token"),
		log:      filepath.Join(stateDir, "autostart.log"),
		manifest: autostart.Filename(directory, sessionID),
	}, cfg, nil
}

func secureWrite(path string, contents []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".awp-*.tmp")
	if err != nil {
		return err
	}
	name := temporary.Name()
	defer os.Remove(name)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(contents); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

func hasJSON(args []string) bool {
	for _, arg := range args {
		if arg == "--json" {
			return true
		}
	}
	return false
}
