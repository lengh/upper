package ui

import (
	"bytes"
	"encoding/base64"
	"errors"
	"os"
	"os/exec"
	"unicode/utf16"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

const helpText = `[::b]Navigation[::B]
  Ctrl+K          jump to any channel or DM (fuzzy search)
  Alt+U           jump to the next unread channel (mentions first)
  Alt+↑ / Alt+↓   previous / next channel in the sidebar
  Tab / Shift+Tab cycle focus: sidebar → messages → input
  Ctrl+B          toggle the sidebar
  F1              this help        Ctrl+C / Ctrl+Q  quit

[::b]Composer[::B]
  Enter           send             Ctrl+J or Alt+Enter   new line
  ↑ (empty input) edit your last message
  PgUp            browse messages  Esc         cancel reply/edit

[::b]Messages[::B] (focus with Tab or PgUp)
  ↑ ↓ / k j       select message   g / G       oldest / newest
  r reply   e edit   d delete   y copy text   i or Esc back to input
  Scrolling past the top loads older history.

[::b]Commands[::B]
  /dm <user> [text[]  open a DM with a friend
  /read              mark everything read
  /sidebar           toggle the sidebar
  /logout            forget the stored token and quit
  /quit              quit
  Start a message with // to send a literal slash.`

func (a *App) showHelp() {
	tv := tview.NewTextView().SetDynamicColors(true).SetText(helpText)
	tv.SetBorder(true).SetTitle(" help — Esc to close ").SetBorderColor(color(a.th.border))
	tv.SetBorderPadding(1, 1, 2, 2)
	tv.SetInputCapture(func(ev *tcell.EventKey) *tcell.EventKey {
		if ev.Key() == tcell.KeyEscape || ev.Key() == tcell.KeyF1 || ev.Rune() == 'q' {
			a.pages.RemovePage("help")
			a.focus(a.input)
			return nil
		}
		return ev
	})
	a.pages.AddPage("help", centered(tv, 76, 30), true, true)
	a.tv.SetFocus(tv)
}

// centered places p in the middle of the screen at up to w×h cells,
// shrinking to fit small terminals.
func centered(p tview.Primitive, w, h int) tview.Primitive {
	return &centerBox{Box: tview.NewBox(), p: p, w: w, h: h}
}

type centerBox struct {
	*tview.Box
	p    tview.Primitive
	w, h int
}

func (c *centerBox) Draw(screen tcell.Screen) {
	sw, sh := screen.Size()
	w, h := min(c.w, sw-2), min(c.h, sh-2)
	c.p.SetRect((sw-w)/2, (sh-h)/2, w, h)
	c.p.Draw(screen)
}

func (c *centerBox) Focus(delegate func(tview.Primitive)) { delegate(c.p) }

func (c *centerBox) HasFocus() bool { return c.p.HasFocus() }

func (c *centerBox) InputHandler() func(*tcell.EventKey, func(tview.Primitive)) {
	return c.p.InputHandler()
}

func (c *centerBox) MouseHandler() func(tview.MouseAction, *tcell.EventMouse, func(tview.Primitive)) (bool, tview.Primitive) {
	return c.p.MouseHandler()
}

// copyToClipboard copies text to the system clipboard. Under WSL it uses the
// Windows clipboard via clip.exe; elsewhere wl-copy/xclip, and finally the
// OSC 52 escape sequence, which Windows Terminal and most modern terminals
// support even over SSH.
func copyToClipboard(text string) error {
	if text == "" {
		return errors.New("message has no text to copy")
	}
	if path, err := exec.LookPath("clip.exe"); err == nil {
		// clip.exe reads UTF-16LE when given a BOM; plain UTF-8 garbles
		// anything outside ASCII.
		u := utf16.Encode([]rune(text))
		buf := bytes.NewBuffer([]byte{0xFF, 0xFE})
		for _, c := range u {
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
