package ui

import (
	"bytes"
	"encoding/base64"
	"errors"
	"os"
	"os/exec"
	"strings"
	"unicode/utf16"
)

// isWSL reports whether we run under Windows Subsystem for Linux.
func isWSL() bool {
	if os.Getenv("WSL_DISTRO_NAME") != "" {
		return true
	}
	data, err := os.ReadFile("/proc/version")
	return err == nil && strings.Contains(strings.ToLower(string(data)), "microsoft")
}

// openURL opens a link in the user's browser. Under WSL that means the
// Windows default browser.
func openURL(u string) error {
	if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
		return errors.New("only web links can be opened")
	}
	var candidates [][]string
	if isWSL() {
		candidates = append(candidates, []string{"wslview", u}, []string{"explorer.exe", u})
	}
	candidates = append(candidates, []string{"xdg-open", u}, []string{"open", u})
	for _, c := range candidates {
		path, err := exec.LookPath(c[0])
		if err != nil {
			continue
		}
		cmd := exec.Command(path, c[1:]...)
		if err := cmd.Start(); err != nil {
			continue
		}
		go func() { _ = cmd.Wait() }()
		return nil
	}
	return errors.New("no browser opener found (install wslu for wslview, or xdg-utils)")
}

// copyToClipboard copies text to the system clipboard. Under WSL it uses the
// Windows clipboard via clip.exe; elsewhere wl-copy/xclip, and finally the
// OSC 52 escape sequence, which Windows Terminal and most modern terminals
// support even over SSH.
func copyToClipboard(text string) error {
	if text == "" {
		return errors.New("this message has no text to copy")
	}
	if path, err := exec.LookPath("clip.exe"); err == nil {
		// clip.exe reads UTF-16LE when given a BOM; plain UTF-8 garbles
		// anything outside ASCII.
		buf := bytes.NewBuffer([]byte{0xFF, 0xFE})
		for _, c := range utf16.Encode([]rune(text)) {
			buf.WriteByte(byte(c))
			buf.WriteByte(byte(c >> 8))
		}
		cmd := exec.Command(path)
		cmd.Stdin = buf
		if cmd.Run() == nil {
			return nil
		}
	}
	for _, c := range [][]string{{"wl-copy"}, {"xclip", "-selection", "clipboard"}, {"xsel", "-ib"}} {
		if _, err := exec.LookPath(c[0]); err == nil {
			cmd := exec.Command(c[0], c[1:]...)
			cmd.Stdin = bytes.NewBufferString(text)
			if cmd.Run() == nil {
				return nil
			}
		}
	}
	tty, err := os.OpenFile("/dev/tty", os.O_WRONLY, 0)
	if err != nil {
		return errors.New("no clipboard available")
	}
	defer tty.Close()
	_, err = tty.WriteString("\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte(text)) + "\a")
	return err
}
