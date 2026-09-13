// Package schedule installs the daily run as a macOS launch agent.
//
// There is deliberately no daemon. A job that runs once a day does not justify
// a resident process with its own supervision, restart policy, log rotation and
// upgrade story — and a daemon that happens not to be running at 07:30 does
// nothing at all, silently. launchd already solves "run this at 07:30, and
// catch up after the laptop wakes" far better than anything worth writing here,
// and a run that fails is just a run that did not happen today, which tomorrow
// fixes on its own.
package schedule

import (
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Label identifies the agent. It is reverse-DNS so it cannot collide with
// another vendor's agent on the same machine.
const Label = "dev.love.daily"

// Job is the launch agent to install.
type Job struct {
	// Binary is the absolute path to the love executable. launchd consults no
	// PATH of its own, so a bare name would fail to run with a message nobody
	// reads.
	Binary string

	Hour   int
	Minute int

	// Env is passed to the run. It must hold paths to credential files, never
	// credentials themselves: a property list is readable by anything running
	// as the user and is reproduced in full by `launchctl print`.
	Env map[string]string

	// LogDir receives stdout and stderr. launchd will not create it.
	LogDir string
}

// Plist renders the property list.
func (j Job) Plist() ([]byte, error) {
	if j.Binary == "" {
		return nil, errors.New("the path to the love executable is required")
	}
	if !filepath.IsAbs(j.Binary) {
		return nil, fmt.Errorf("the executable path must be absolute, got %q", j.Binary)
	}
	if j.Hour < 0 || j.Hour > 23 || j.Minute < 0 || j.Minute > 59 {
		return nil, fmt.Errorf("invalid time %02d:%02d", j.Hour, j.Minute)
	}

	var b strings.Builder
	b.WriteString(xml.Header)
	b.WriteString(`<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" ` +
		`"http://www.apple.com/DTDs/PropertyList-1.0.dtd">` + "\n")
	b.WriteString("<plist version=\"1.0\">\n<dict>\n")

	writeString(&b, 1, "Label", Label)

	writeKey(&b, 1, "ProgramArguments")
	writeOpen(&b, 1, "array")
	writeElement(&b, 2, "string", j.Binary)
	writeElement(&b, 2, "string", "--daily")
	writeClose(&b, 1, "array")

	writeKey(&b, 1, "StartCalendarInterval")
	writeOpen(&b, 1, "dict")
	writeInteger(&b, 2, "Hour", j.Hour)
	writeInteger(&b, 2, "Minute", j.Minute)
	writeClose(&b, 1, "dict")

	if len(j.Env) > 0 {
		writeKey(&b, 1, "EnvironmentVariables")
		writeOpen(&b, 1, "dict")
		names := make([]string, 0, len(j.Env))
		for name := range j.Env {
			names = append(names, name)
		}
		sort.Strings(names) // a stable file is a diffable file
		for _, name := range names {
			writeString(&b, 2, name, j.Env[name])
		}
		writeClose(&b, 1, "dict")
	}

	// Installing must not immediately send an email, so the job does not run at
	// load. The first run is the next scheduled one.
	writeKey(&b, 1, "RunAtLoad")
	writeSelfClosing(&b, 1, "false")

	if j.LogDir != "" {
		writeString(&b, 1, "StandardOutPath", filepath.Join(j.LogDir, "daily.log"))
		writeString(&b, 1, "StandardErrorPath", filepath.Join(j.LogDir, "daily.err"))
	}

	b.WriteString("</dict>\n</plist>\n")
	return []byte(b.String()), nil
}

// Path returns where the agent belongs.
func Path() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "LaunchAgents", Label+".plist"), nil
}

// Install writes the agent and loads it, replacing any previous version.
func (j Job) Install() (string, error) {
	path, err := Path()
	if err != nil {
		return "", err
	}
	content, err := j.Plist()
	if err != nil {
		return "", err
	}
	if j.LogDir != "" {
		if err := os.MkdirAll(j.LogDir, 0o755); err != nil {
			return "", err
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, content, 0o644); err != nil {
		return "", err
	}

	// Reinstalling has to displace the loaded copy, and bootout fails when
	// nothing is loaded — which is the normal case on a first install.
	_ = bootout()

	if err := launchctl("bootstrap", domain(), path); err != nil {
		return path, fmt.Errorf("wrote %s but could not load it: %w", path, err)
	}
	return path, nil
}

// Uninstall unloads the agent and removes it.
func Uninstall() (removed bool, err error) {
	path, pathErr := Path()
	if pathErr != nil {
		return false, pathErr
	}
	_ = bootout()

	if statErr := os.Remove(path); statErr != nil {
		if os.IsNotExist(statErr) {
			return false, nil
		}
		return false, statErr
	}
	return true, nil
}

// Status reports whether the agent is currently loaded.
func Status() (bool, string) {
	out, err := exec.Command("launchctl", "print", domain()+"/"+Label).CombinedOutput()
	if err != nil {
		return false, strings.TrimSpace(string(out))
	}
	return true, strings.TrimSpace(string(out))
}

// RunNow triggers the agent immediately, which is how an install is verified
// without waiting until tomorrow morning.
func RunNow() error {
	return launchctl("kickstart", "-k", domain()+"/"+Label)
}

func bootout() error {
	return launchctl("bootout", domain()+"/"+Label)
}

// domain is the per-user launchd domain, which is gui/<uid> for a logged-in
// session.
func domain() string {
	return "gui/" + strconv.Itoa(os.Getuid())
}

func launchctl(args ...string) error {
	cmd := exec.Command("launchctl", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(out))
		if message == "" {
			message = err.Error()
		}
		return fmt.Errorf("launchctl %s: %s", strings.Join(args, " "), message)
	}
	return nil
}

// ---------------------------------------------------------------------------
// A very small property list writer. The document is a dozen lines long, so
// encoding it correctly here is cheaper than owning a library for it.
// ---------------------------------------------------------------------------

func writeKey(b *strings.Builder, level int, name string) {
	writeIndent(b, level)
	b.WriteString("<key>")
	xml.EscapeText(b, []byte(name))
	b.WriteString("</key>\n")
}

func writeElement(b *strings.Builder, level int, tag, value string) {
	writeIndent(b, level)
	b.WriteString("<" + tag + ">")
	xml.EscapeText(b, []byte(value))
	b.WriteString("</" + tag + ">\n")
}

func writeSelfClosing(b *strings.Builder, level int, tag string) {
	writeIndent(b, level)
	b.WriteString("<" + tag + "/>\n")
}

func writeOpen(b *strings.Builder, level int, tag string) {
	writeIndent(b, level)
	b.WriteString("<" + tag + ">\n")
}

func writeClose(b *strings.Builder, level int, tag string) {
	writeIndent(b, level)
	b.WriteString("</" + tag + ">\n")
}

func writeIndent(b *strings.Builder, level int) {
	b.WriteString(strings.Repeat("\t", level))
}

func writeString(b *strings.Builder, level int, name, value string) {
	writeKey(b, level, name)
	writeElement(b, level, "string", value)
}

func writeInteger(b *strings.Builder, level int, name string, value int) {
	writeKey(b, level, name)
	writeElement(b, level, "integer", strconv.Itoa(value))
}
