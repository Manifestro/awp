package autostart

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"path/filepath"
	"regexp"
	"text/template"
)

const labelPrefix = "net.manifestro.awp"

var safeID = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

type LaunchdOptions struct {
	BinaryPath string
	ConfigPath string
	StorePath  string
	TokenFile  string
	SessionID  string
	LogPath    string
	PathEnv    string
}

func Label(sessionID string) string {
	clean := safeID.ReplaceAllString(sessionID, "-")
	if clean == "" {
		clean = "client"
	}
	return labelPrefix + "." + clean
}

func Filename(directory, sessionID string) string {
	return filepath.Join(directory, Label(sessionID)+".plist")
}

func RenderLaunchd(options LaunchdOptions) ([]byte, error) {
	if options.BinaryPath == "" || options.ConfigPath == "" || options.StorePath == "" || options.TokenFile == "" || options.SessionID == "" {
		return nil, fmt.Errorf("binary, config, store, token file, and session id are required")
	}
	if options.LogPath == "" {
		return nil, fmt.Errorf("log path is required")
	}
	values := map[string]string{
		"Label":     escape(Label(options.SessionID)),
		"Binary":    escape(options.BinaryPath),
		"Config":    escape(options.ConfigPath),
		"Store":     escape(options.StorePath),
		"TokenFile": escape(options.TokenFile),
		"Session":   escape(options.SessionID),
		"Log":       escape(options.LogPath),
		"PathEnv":   escape(options.PathEnv),
	}
	var output bytes.Buffer
	if err := launchdTemplate.Execute(&output, values); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func escape(value string) string {
	var output bytes.Buffer
	_ = xml.EscapeText(&output, []byte(value))
	return output.String()
}

var launchdTemplate = template.Must(template.New("launchd").Parse(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>{{.Label}}</string>
  <key>ProgramArguments</key>
  <array>
    <string>{{.Binary}}</string>
    <string>connect</string>
    <string>--config</string>
    <string>{{.Config}}</string>
    <string>--store</string>
    <string>{{.Store}}</string>
    <string>--token-file</string>
    <string>{{.TokenFile}}</string>
    <string>--session-id</string>
    <string>{{.Session}}</string>
    <string>--reconnect</string>
    <string>--json</string>
  </array>
  <key>RunAtLoad</key>
  <true/>
  <key>KeepAlive</key>
  <true/>
  <key>ProcessType</key>
  <string>Background</string>
  <key>EnvironmentVariables</key>
  <dict>
    <key>PATH</key>
    <string>{{.PathEnv}}</string>
  </dict>
  <key>StandardOutPath</key>
  <string>{{.Log}}</string>
  <key>StandardErrorPath</key>
  <string>{{.Log}}</string>
</dict>
</plist>
`))
