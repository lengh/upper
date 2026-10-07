package ui

import (
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

type helpSection struct {
	title string
	keys  [][2]string
}

var helpSections = []helpSection{
	{"Getting around", [][2]string{
		{"Ctrl+K", "jump to any channel or DM"},
		{"Alt+A", "next conversation with activity"},
		{"Alt+1…9", "open activity item 1–9"},
		{"Alt+/", "back to the previous channel"},
		{"Alt+↑ ↓", "previous / next channel"},
		{"Ctrl+P N", "the same, IRC style"},
		{"Tab", "focus sidebar, then messages"},
		{"Ctrl+B", "hide or show the sidebar"},
	}},
	{"Writing", [][2]string{
		{"Enter", "send"},
		{"Ctrl+J", "new line (also Alt/Shift+Enter)"},
		{"Tab", "complete @name #channel :emoji:"},
		{"↑", "edit your last message"},
		{"s/old/new", "fix your last message"},
		{"+:emoji:", "react to the last message"},
		{"Esc", "cancel reply or edit"},
	}},
	{"Jump labels: any message in two keys", [][2]string{
		{"Alt+R", "reply to a message on screen"},
		{"Alt+E", "react to one"},
		{"Alt+O", "open a link from one"},
		{"Alt+Y", "copy one"},
		{"f", "select one (message mode)"},
	}},
	{"Reading", [][2]string{
		{"PgUp PgDn", "scroll (or mouse wheel)"},
		{"Ctrl+F", "find in channel · ↑↓ step"},
		{"Alt+U", "jump to the first unread"},
		{"Alt+N  Alt+P", "next / previous mention of you"},
		{"Alt+< >", "oldest / newest"},
		{"Esc", "jump back to the present"},
	}},
	{"On a message (Ctrl+↑ or click)", [][2]string{
		{"↑ ↓  j k", "move between messages"},
		{"r  Enter", "reply"},
		{"e  d", "edit · delete"},
		{"a  y  o", "react · copy · open link"},
		{"?", "this help (also in the sidebar)"},
	}},
	{"Commands", [][2]string{
		{"/dm name", "open a direct message"},
		{"/me /shrug", "actions and classics"},
		{"/read [all]", "mark read"},
		{"/theme", "dark, light or mono"},
		{"/time", "toggle timestamps"},
		{"/update", "update now (also on launch)"},
		{"/logout /quit", "leave"},
	}},
	{"App", [][2]string{
		{"F1", "this help"},
		{"Ctrl+L", "redraw the screen"},
		{"Ctrl+C ×2", "quit (once clears input)"},
		{"Shift+drag", "select text with the mouse"},
	}},
}

// helpView shows every key in two columns, grouped by intent.
type helpView struct {
	*tview.Box
	a      *App
	offset int
}

func (a *App) showHelp() {
	h := &helpView{Box: tview.NewBox(), a: a}
	a.pages.AddPage("help", h, true, true)
	a.tv.SetFocus(h)
}

func (h *helpView) lines(colW int) [][]line {
	th := h.a.th
	keyW := 13
	var cols [][]line
	for _, sec := range helpSections {
		var ls []line
		ls = append(ls, line{{text: sec.title, style: th.accent().Bold(true)}})
		for _, kv := range sec.keys {
			ls = append(ls, line{
				{text: padRight(kv[0], keyW), style: th.fg(th.Text).Bold(true)},
				{text: truncate(kv[1], colW-keyW), style: th.fg(th.Subtle)},
			})
		}
		ls = append(ls, nil)
		cols = append(cols, ls)
	}
	return cols
}

func padRight(s string, w int) string {
	for textWidth(s) < w {
		s += " "
	}
	return s
}

func (h *helpView) Draw(scr tcell.Screen) {
	th := h.a.th
	dimBackground(scr, th)
	sw, _ := scr.Size()
	wide := sw >= 96
	pw := 62
	if wide {
		pw = 100
	}
	x, y, w, ph := panel(scr, th, pw, 32, "Keys & commands", "Esc close · ↑↓ scroll")
	colW := w
	if wide {
		colW = (w - 4) / 2
	}
	secs := h.lines(colW)
	// Flow sections into one or two columns.
	var left, right []line
	if wide {
		total := 0
		for _, s := range secs {
			total += len(s)
		}
		for _, s := range secs {
			if len(left) < total/2 {
				left = append(left, s...)
			} else {
				right = append(right, s...)
			}
		}
	} else {
		for _, s := range secs {
			left = append(left, s...)
		}
	}
	maxOff := max(0, max(len(left), len(right))-ph)
	h.offset = max(0, min(h.offset, maxOff))
	for r := 0; r < ph; r++ {
		if i := h.offset + r; i < len(left) {
			drawLine(scr, x, y+r, colW, left[i], true, nil)
		}
		if i := h.offset + r; wide && i < len(right) {
			drawLine(scr, x+colW+4, y+r, colW, right[i], true, nil)
		}
	}
}

func (h *helpView) InputHandler() func(*tcell.EventKey, func(tview.Primitive)) {
	return h.WrapInputHandler(func(ev *tcell.EventKey, _ func(tview.Primitive)) {
		switch {
		case ev.Key() == tcell.KeyUp || ev.Rune() == 'k':
			h.offset--
		case ev.Key() == tcell.KeyDown || ev.Rune() == 'j':
			h.offset++
		case ev.Key() == tcell.KeyPgUp:
			h.offset -= 10
		case ev.Key() == tcell.KeyPgDn:
			h.offset += 10
		default:
			h.a.pages.RemovePage("help")
			h.a.focusComposer()
		}
	})
}

func (h *helpView) MouseHandler() func(tview.MouseAction, *tcell.EventMouse, func(tview.Primitive)) (bool, tview.Primitive) {
	return h.WrapMouseHandler(func(action tview.MouseAction, _ *tcell.EventMouse, _ func(tview.Primitive)) (bool, tview.Primitive) {
		switch action {
		case tview.MouseScrollUp:
			h.offset--
		case tview.MouseScrollDown:
			h.offset++
		case tview.MouseLeftClick:
			h.a.pages.RemovePage("help")
			h.a.focusComposer()
		}
		return true, nil
	})
}
