package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/lengh/upper/internal/discord"
	"github.com/lengh/upper/internal/state"
	"github.com/rivo/tview"
)

var spinner = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// channelGlyph is the one-character type marker used in every list.
func channelGlyph(t discord.ChannelType) string {
	switch t {
	case discord.ChannelDM:
		return "@"
	case discord.ChannelGroupDM:
		return "◇"
	case discord.ChannelAnnounceThrd, discord.ChannelPublicThread, discord.ChannelPrivateThread:
		return "↳"
	case discord.ChannelAnnouncement:
		return "»"
	}
	return "#"
}

// ---- Title bar ---------------------------------------------------------------

// titleBar names where you are and how the connection is doing. The
// connection indicator stays a quiet dot while healthy and only spells
// things out when something is wrong.
type titleBar struct {
	*tview.Box
	a *App
}

func newTitleBar(a *App) *titleBar { return &titleBar{Box: tview.NewBox(), a: a} }

func (t *titleBar) Draw(scr tcell.Screen) {
	a, th := t.a, t.a.th
	x, y, w, _ := t.GetRect()
	bar := th.bar()
	fill(scr, x, y, w, bar)

	// Right side first so the left can truncate around it.
	var right []span
	switch {
	case !a.conn.ok:
		right = append(right, span{text: spinner[a.spin%len(spinner)] + " " + a.conn.message, style: bar.Foreground(th.Warn)})
	default:
		if a.current != 0 {
			if ch, ok := a.st.Channel(a.current); ok && ch.GuildID != 0 {
				right = append(right, span{text: a.st.GuildName(ch.GuildID) + "  ", style: bar.Foreground(th.Muted)})
			} else if ok {
				right = append(right, span{text: "direct message  ", style: bar.Foreground(th.Muted)})
			}
		}
		right = append(right, span{text: "●", style: bar.Foreground(th.OK)})
	}
	rw := lineWidth(right) + 1
	drawLine(scr, x+w-rw, y, rw, right, true, nil)

	left := x + 1
	maxL := w - rw - 2
	if a.current == 0 {
		used := drawText(scr, left, y, maxL, "upper", bar.Foreground(th.Accent).Bold(true))
		drawText(scr, left+used, y, maxL-used, "  terminal Discord", bar.Foreground(th.Muted))
		return
	}
	ch, _ := a.st.Channel(a.current)
	used := drawText(scr, left, y, maxL, channelGlyph(ch.Type)+" ", bar.Foreground(th.Accent).Bold(true))
	name := a.st.ChannelTitle(a.current)
	if ch.GuildID != 0 {
		name = ch.Name
	} else {
		name = strings.TrimPrefix(name, "@")
	}
	used += drawText(scr, left+used, y, maxL-used, truncate(name, maxL-used), bar.Foreground(th.Text).Bold(true))

	var extra string
	if u, ok := a.st.DMRecipient(a.current); ok {
		parts := []string{}
		if u.Username != "" && !strings.EqualFold(u.Username, name) {
			parts = append(parts, u.Tag())
		}
		if p := a.st.Presence(u.ID); p != "" {
			parts = append(parts, presenceWord(p))
		}
		extra = strings.Join(parts, " · ")
	} else if ch.Topic != "" {
		extra = strings.Join(strings.Fields(plainContent(ch.Topic, ch.GuildID, a.st)), " ")
	}
	if extra != "" && maxL-used > 6 {
		used += drawText(scr, left+used, y, 3, "  ", bar)
		drawText(scr, left+used, y, maxL-used, truncate(extra, maxL-used), bar.Foreground(th.Subtle))
	}
}

func presenceWord(p string) string {
	switch p {
	case "online":
		return "online"
	case "idle":
		return "away"
	case "dnd":
		return "do not disturb"
	}
	return ""
}

// ---- Status bar --------------------------------------------------------------

// statusBar sits between the conversation and the composer. The left half
// is the hotlist (numbered for Alt+1…9); the right half is context: what
// the app needs from you or wants to tell you right now, most urgent first.
type statusBar struct {
	*tview.Box
	a *App
}

func newStatusBar(a *App) *statusBar { return &statusBar{Box: tview.NewBox(), a: a} }

func (s *statusBar) Draw(scr tcell.Screen) {
	a, th := s.a, s.a.th
	x, y, w, _ := s.GetRect()
	bar := th.bar()
	fill(scr, x, y, w, bar)

	right := s.context()
	rw := lineWidth(right)
	if rw > w-12 {
		right = line{{text: truncate(spansText(right), w-12), style: right[0].style}}
		rw = lineWidth(right)
	}
	drawLine(scr, x+w-rw-1, y, rw, right, true, nil)

	// Hotlist.
	left := x + 1
	avail := w - rw - 4
	used := drawText(scr, left, y, avail, time.Now().Format("15:04")+" ", bar.Foreground(th.Muted))
	if len(a.hotlist) == 0 {
		return
	}
	used += drawText(scr, left+used, y, avail-used, "│ ", bar.Foreground(th.Faint))
	for i, c := range a.hotlist {
		item := s.hotItem(i, c)
		iw := lineWidth(item)
		if used+iw > avail {
			more := fmt.Sprintf("+%d", len(a.hotlist)-i)
			drawText(scr, left+used, y, avail-used, more, bar.Foreground(th.Muted))
			break
		}
		drawLine(scr, left+used, y, iw, item, true, nil)
		used += iw
	}
}

func spansText(l line) string {
	var b strings.Builder
	for _, s := range l {
		b.WriteString(s.text)
	}
	return b.String()
}

func (s *statusBar) hotItem(i int, c state.ChannelInfo) line {
	th := s.a.th
	bar := th.bar()
	var l line
	if i < 9 {
		l = append(l, span{text: fmt.Sprint(i+1) + ":", style: bar.Foreground(th.Muted)})
	}
	name := c.Name
	if !c.Type.IsPrivate() {
		name = "#" + name
	}
	name = truncate(name, 18)
	st := bar.Foreground(th.Text)
	switch {
	case c.Mentions > 0:
		st = bar.Foreground(th.Mention).Bold(true)
	case c.Type.IsPrivate():
		st = bar.Foreground(th.Accent).Bold(true)
	}
	l = append(l, span{text: name, style: st})
	if c.Mentions > 0 {
		l = append(l, span{text: fmt.Sprintf("(%d)", c.Mentions), style: bar.Foreground(th.Mention)})
	}
	l = append(l, span{text: "  ", style: bar})
	return l
}

// context picks the single most relevant right-hand message.
func (s *statusBar) context() line {
	a, th := s.a, s.a.th
	bar := th.bar()
	key := func(k string) span { return span{text: k, style: bar.Foreground(th.Text).Bold(true)} }
	txt := func(t string) span { return span{text: t, style: bar.Foreground(th.Muted)} }

	if a.confirm != nil {
		return line{{text: a.confirm.question + " ", style: bar.Foreground(th.Warn).Bold(true)}, key("y"), txt(" yes · any key cancels")}
	}
	if a.view.hint != nil {
		return a.view.hintStatus()
	}
	if a.flashMsg != "" && time.Now().Before(a.flashUntil) {
		st := bar.Foreground(th.BarText)
		prefix := ""
		switch a.flashLevel {
		case levelError:
			st, prefix = bar.Foreground(th.Error), "✕ "
		case levelWarn:
			st, prefix = bar.Foreground(th.Warn), "! "
		case levelOK:
			st, prefix = bar.Foreground(th.OK), "✓ "
		}
		return line{{text: prefix + a.flashMsg, style: st}}
	}
	if c := a.comp; c != nil && len(c.items) > 1 {
		var l line
		for i, it := range c.items {
			if i >= 8 {
				l = append(l, txt(fmt.Sprintf("+%d", len(c.items)-8)))
				break
			}
			st := bar.Foreground(th.Subtle)
			if i == c.index {
				st = bar.Foreground(th.Accent).Bold(true).Reverse(th.mono)
			}
			l = append(l, span{text: it.label, style: st}, txt("  "))
		}
		return l
	}
	if a.mode == modeSearch {
		return a.view.searchStatus()
	}
	text := a.input.GetText()
	if u := commandUsage(text); u != "" && a.input.HasFocus() {
		return line{txt(u)}
	}
	if a.view.HasFocus() {
		return line{key("r"), txt(" reply  "), key("e"), txt(" edit  "), key("d"), txt(" delete  "),
			key("a"), txt(" react  "), key("y"), txt(" copy  "), key("o"), txt(" link  "), key("f"), txt(" jump  "), key("?"), txt(" keys")}
	}
	if a.side.HasFocus() {
		return line{key("↑↓"), txt(" move  "), key("Enter"), txt(" open  "), key("←→"), txt(" fold  "), key("type"), txt(" to search  "), key("Esc"), txt(" back")}
	}
	if n := runeLen(text); n > 1800 {
		st := bar.Foreground(th.Warn)
		msg := fmt.Sprintf("%d/%d", n, maxMessageLen)
		if n > maxMessageLen {
			msg += fmt.Sprintf(" · sends as %d messages", len(splitMessage(text, maxMessageLen)))
		}
		return line{{text: msg, style: st}}
	}
	if names := a.st.Typing(a.current); len(names) > 0 && a.current != 0 {
		dots := []string{"   ", ".  ", ".. ", "..."}[a.spin%4]
		return line{{text: typingText(names) + dots, style: bar.Foreground(th.Subtle).Italic(true)}}
	}
	if a.view.scroll > 0 {
		if n := a.view.newBelow(); n > 0 {
			return line{{text: fmt.Sprintf("↓ %d below", n), style: bar.Foreground(th.Mention).Bold(true)}, txt(" · "), key("Esc"), txt(" jump to present")}
		}
		return line{txt("scrolled back · "), key("Esc"), txt(" jump to present")}
	}
	if a.current == 0 && a.ready {
		return line{key("Ctrl+K"), txt(" jump  "), key("Alt+A"), txt(" next unread  "), key("F1"), txt(" help")}
	}
	return nil
}

func typingText(names []string) string {
	switch len(names) {
	case 1:
		return names[0] + " is typing"
	case 2:
		return names[0] + " and " + names[1] + " are typing"
	case 3:
		return names[0] + ", " + names[1] + " and " + names[2] + " are typing"
	}
	return "several people are typing"
}

// ---- Prompt ------------------------------------------------------------------

// promptView is the label left of the composer. It always says what Enter
// will do: send to a channel, run a command, reply, edit or react.
type promptView struct {
	*tview.Box
	a    *App
	text line
}

func newPromptView(a *App) *promptView { return &promptView{Box: tview.NewBox(), a: a} }

func (p *promptView) compute() line {
	a, th := p.a, p.a.th
	sp := func(t string, st tcell.Style) span { return span{text: t, style: st} }
	text := a.input.GetText()
	switch {
	case a.mode == modeEdit:
		return line{sp(" ✎ edit ", th.fg(th.Warn).Bold(true)), sp("❯ ", th.fg(th.Warn))}
	case a.mode == modeSearch:
		return line{sp(" find ", th.fg(th.Link).Bold(true)), sp("❯ ", th.fg(th.Link))}
	case a.mode == modeReact:
		return line{sp(" ☺ react ", th.accent().Bold(true)), sp("❯ ", th.accent())}
	case a.mode == modeReply && a.target != nil:
		au := a.st.Author(a.target.GuildID, a.target.Author)
		c := th.nickColor(uint64(a.target.Author.ID), au.Color)
		return line{sp(" ↪ ", th.muted()), sp(truncate(au.Name, 16), th.fg(c).Bold(true)), sp(" ❯ ", th.accent())}
	case strings.HasPrefix(text, "/") && !strings.HasPrefix(text, "//"):
		return line{sp(" cmd ", th.fg(th.Warn).Bold(true)), sp("❯ ", th.fg(th.Warn))}
	case a.current == 0:
		return line{sp(" ❯ ", th.muted())}
	case !a.st.CanSend(a.current):
		return line{sp(" ⊘ ", th.muted())}
	}
	return line{sp(" ❯ ", th.accent().Bold(true))}
}

// fit resizes the prompt column and the composer height to their content.
func (p *promptView) fit() {
	a := p.a
	p.text = p.compute()
	a.compRow.ResizeItem(p, lineWidth(p.text), 0)
	_, _, w, _ := a.input.GetInnerRect()
	w = max(w, 10)
	rows := 0
	for _, l := range strings.Split(a.input.GetText(), "\n") {
		rows += max(1, (textWidth(l)+w)/w)
	}
	rows = min(rows, 8)
	a.root.ResizeItem(a.compRow, rows, 0)
}

func (p *promptView) Draw(scr tcell.Screen) {
	x, y, w, _ := p.GetRect()
	drawLine(scr, x, y, w, p.text, true, nil)
}
