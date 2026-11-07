package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/term"
)

// term opens a dedicated window: its own class (for WM rules / launcher matching)
// and the machine's alacritty settings.
func (a *app) term(name string) error {
	m, err := a.m.Machine(name)
	if err != nil {
		return err
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	args := []string{"--class", "sacabox,sacabox-" + name, "--title", name}
	for _, o := range flatten(m.Terminal.Settings, nil) {
		args = append(args, "-o", o)
	}
	args = append(args, "-e", "sh", "-c",
		`"$0" sh "$1" || { printf "\n[sacabox sh failed, press enter to close] "; read -r _; }`, self, name)
	c := exec.Command(a.m.Terminal.Alacritty, args...)
	c.Env = append(os.Environ(), "SACABOX_TERM=1")
	c.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := c.Start(); err != nil {
		return err
	}
	return c.Process.Release()
}

// flatten turns the settings attrset into `a.b.c=<toml value>` overrides
// (JSON scalars and arrays are valid TOML values).
func flatten(settings map[string]any, prefix []string) []string {
	keys := make([]string, 0, len(settings))
	for k := range settings {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var out []string
	for _, k := range keys {
		path := append(append([]string{}, prefix...), k)
		if sub, ok := settings[k].(map[string]any); ok {
			out = append(out, flatten(sub, path)...)
			continue
		}
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		_ = enc.Encode(settings[k])
		out = append(out, strings.Join(path, ".")+"="+strings.TrimSpace(buf.String()))
	}
	return out
}

func isTerminal(f *os.File) bool { return term.IsTerminal(int(f.Fd())) }

// banner prints a full-width coloured bar (plain SGR, works in any terminal) and sets the window title.
func banner(name, color, what string) {
	cols := 80
	if c, err := strconv.Atoi(os.Getenv("COLUMNS")); err == nil && c > 0 {
		cols = c
	} else if w, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil && w > 0 {
		cols = w
	}
	title := ""
	if what == "enter" {
		title = "[" + name + "]"
	}
	hex := strings.TrimPrefix(color, "#")
	rgb := func(i int) int64 { v, _ := strconv.ParseInt(hex[i:i+2], 16, 0); return v }
	if len(hex) != 6 {
		return
	}
	fmt.Printf("\x1b]2;%s\a", title)
	fmt.Printf("\x1b[48;2;%d;%d;%dm\x1b[1;97m%-*s\x1b[0m\n", rgb(0), rgb(2), rgb(4), cols, fmt.Sprintf(" %s  %s", what, name))
}
