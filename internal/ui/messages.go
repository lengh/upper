package ui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/lengh/upper/internal/discord"
	"github.com/rivo/tview"
)

func regionID(m *discord.Message) string {
	if m.Pending {
		return "p" + m.NonceValue().String()
	}
	return m.ID.String()
}

// renderMessages redraws the open channel. Messages from the same author in
// quick succession are grouped under one name, like the official client.
func (a *App) renderMessages() {
	if a.current == 0 {
		return
	}
	msgs, more := a.st.Messages(a.current)
	th := a.th
	me := a.st.Me().ID

	var b strings.Builder
	b.Grow(len(msgs) * 96)
	a.order = a.order[:0]
	clear(a.byRegion)

	if more {
		b.WriteString("[" + th.muted + "]↑ older messages: scroll up or press g[-]\n")
	} else {
		b.WriteString("[" + th.muted + "]— beginning of " + tview.Escape(a.st.ChannelTitle(a.current)) + " —[-]\n")
	}

	var prev *discord.Message
	var prevDay string
	newShown := false
	for i := range msgs {
		m := &msgs[i]
		ts := m.Timestamp
		if m.Pending || ts.IsZero() {
			ts = m.ID.Time()
			if m.Pending {
				ts = time.Now()
			}
		}
		ts = ts.Local()

		day := ts.Format("Monday, 2 January 2006")
		if day != prevDay {
			b.WriteString("\n[" + th.muted + "]──── " + day + " ────[-]\n")
			prevDay = day
			prev = nil
		}
		if !newShown && a.newSince != 0 && !m.Pending && m.ID > a.newSince && m.Author.ID != me && i > 0 {
			b.WriteString("[" + th.errc + "]──── new ────[-]\n")
			newShown = true
			prev = nil
		}

		grouped := prev != nil && prev.Author.ID == m.Author.ID && m.ReferencedMessage == nil &&
			m.Type == prev.Type && ts.Sub(prev.Timestamp.Local()) < 5*time.Minute
		id := regionID(m)
		a.order = append(a.order, id)
		a.byRegion[id] = *m

		b.WriteString(`["` + id + `"]`)
		if m.ReferencedMessage != nil {
			r := m.ReferencedMessage
			name := a.st.Author(m.GuildID, r.Author).Name
			snippet := strings.Join(strings.Fields(plainContent(r.Content, m.GuildID, a.st)), " ")
			if len([]rune(snippet)) > 60 {
				snippet = string([]rune(snippet)[:60]) + "…"
			}
			if snippet == "" {
				snippet = "(no text)"
			}
			b.WriteString("[" + th.muted + "]  ╭─ " + tview.Escape(name) + ": " + tview.Escape(snippet) + "[-]\n")
		} else if m.Type == discord.MessageReply && m.MessageReference != nil {
			b.WriteString("[" + th.muted + "]  ╭─ (original message deleted)[-]\n")
		}

		stamp := ""
		if a.cfg.TimeFormat != "" {
			stamp = "[" + th.timestamp + "]" + ts.Format(a.cfg.TimeFormat) + "[-] "
		}
		if grouped {
			b.WriteString(strings.Repeat(" ", len(ts.Format(a.cfg.TimeFormat))+1))
		} else {
			b.WriteString(stamp)
			au := a.st.Author(m.GuildID, m.Author)
			nameColor := th.unread
			if au.Color != 0 {
				nameColor = fmt.Sprintf("#%06x", au.Color)
			}
			b.WriteString("[" + nameColor + "::b]" + tview.Escape(au.Name) + "[-::B]")
			if m.Author.Bot {
				b.WriteString(" [" + th.accent + "]BOT[-]")
			}
			b.WriteString(" ")
		}

		body := a.messageBody(m)
		switch {
		case m.Failed:
			body = "[" + th.errc + "]" + body + " (failed to send: Enter retries, d discards)[-]"
		case m.Pending:
			body = "[" + th.muted + "]" + body + "[-]"
		}
		if a.mentionsMe(m, me) {
			body = "[" + th.mention + "]▌[-]" + body
		}
		b.WriteString(body)
		if m.EditedTimestamp != nil {
			b.WriteString(" [" + th.muted + "](edited)[-]")
		}
		b.WriteString(`[""]` + "\n")
		prev = m
	}

	a.msgs.SetText(strings.TrimSuffix(b.String(), "\n"))
	if a.selected != "" {
		if _, ok := a.byRegion[a.selected]; ok {
			a.msgs.Highlight(a.selected)
		} else {
			a.selected = ""
			a.msgs.Highlight()
		}
	}
}

func (a *App) mentionsMe(m *discord.Message, me Snowflake) bool {
	if m.Author.ID == me {
		return false
	}
	if m.MentionEveryone {
		return true
	}
	for _, u := range m.Mentions {
		if u.ID == me {
			return true
		}
	}
	return false
}

// messageBody renders content plus text placeholders for non-text parts.
func (a *App) messageBody(m *discord.Message) string {
	th := a.th
	var parts []string
	switch m.Type {
	case 0, discord.MessageReply:
	case 7:
		return "[" + th.muted + "]joined the server[-]"
	case 6:
		return "[" + th.muted + "]pinned a message[-]"
	case 1:
		return "[" + th.muted + "]added someone to the group[-]"
	case 2:
		return "[" + th.muted + "]left or was removed from the group[-]"
	case 3:
		return "[" + th.muted + "]started a call[-]"
	case 4:
		return "[" + th.muted + "]changed the channel name: " + tview.Escape(m.Content) + "[-]"
	case 8, 9, 10, 11:
		return "[" + th.muted + "]boosted the server[-]"
	}
	if m.Content != "" {
		content := renderContent(m.Content, m.GuildID, a.st, th)
		// Indent continuation lines under the message text.
		parts = append(parts, strings.ReplaceAll(content, "\n", "\n      "))
	}
	for _, att := range m.Attachments {
		parts = append(parts, "["+th.muted+"][attachment: "+tview.Escape(att.Filename)+"][-]")
	}
	for _, s := range m.Stickers {
		parts = append(parts, "["+th.muted+"][sticker: "+tview.Escape(s.Name)+"][-]")
	}
	if m.Content == "" {
		for _, e := range m.Embeds {
			t := e.Title
			if t == "" {
				t = e.Description
			}
			if t == "" {
				t = e.URL
			}
			if t != "" {
				t = strings.Join(strings.Fields(t), " ")
				if len([]rune(t)) > 120 {
					t = string([]rune(t)[:120]) + "…"
				}
				parts = append(parts, "["+th.muted+"][embed: "+tview.Escape(t)+"][-]")
			}
		}
	}
	if len(parts) == 0 {
		return "[" + th.muted + "](no text content)[-]"
	}
	return strings.Join(parts, " ")
}

// ---- Selection ---------------------------------------------------------------

func (a *App) selectLast() {
	if len(a.order) == 0 {
		return
	}
	a.selected = a.order[len(a.order)-1]
	a.msgs.Highlight(a.selected)
	a.msgs.ScrollToHighlight()
}

func (a *App) moveSelection(delta int) {
	if len(a.order) == 0 {
		return
	}
	idx := len(a.order)
	for i, id := range a.order {
		if id == a.selected {
			idx = i
		}
	}
	idx += delta
	if idx < 0 {
		idx = 0
		a.loadOlder()
	}
	if idx >= len(a.order) {
		// Moving past the newest message returns to the composer.
		a.focus(a.input)
		a.msgs.ScrollToEnd()
		return
	}
	a.selected = a.order[idx]
	a.msgs.Highlight(a.selected)
	a.msgs.ScrollToHighlight()
}

func (a *App) selectedMessage() (discord.Message, bool) {
	m, ok := a.byRegion[a.selected]
	return m, ok
}

func (a *App) onMessagesKey(ev *tcell.EventKey) *tcell.EventKey {
	switch ev.Key() {
	case tcell.KeyUp:
		a.moveSelection(-1)
		return nil
	case tcell.KeyDown:
		a.moveSelection(1)
		return nil
	case tcell.KeyPgUp:
		if row, _ := a.msgs.GetScrollOffset(); row == 0 {
			a.loadOlder()
		}
		return ev
	case tcell.KeyHome:
		a.loadOlder()
		return ev
	case tcell.KeyEscape:
		a.focus(a.input)
		return nil
	case tcell.KeyEnter:
		if m, ok := a.selectedMessage(); ok && m.Failed {
			a.retry(m)
		}
		return nil
	case tcell.KeyRune:
	default:
		return ev
	}
	m, ok := a.selectedMessage()
	me := a.st.Me().ID
	switch ev.Rune() {
	case 'k':
		a.moveSelection(-1)
	case 'j':
		a.moveSelection(1)
	case 'g':
		if len(a.order) > 0 {
			a.selected = a.order[0]
			a.msgs.Highlight(a.selected)
			a.msgs.ScrollToBeginning()
		}
		a.loadOlder()
	case 'G':
		a.selectLast()
	case 'r':
		if ok && !m.Pending {
			a.startReply(m)
		}
	case 'e':
		if ok && !m.Pending && m.Author.ID == me {
			a.startEdit(m)
		}
	case 'd':
		switch {
		case ok && m.Failed:
			a.st.RemoveFailed(m.ChannelID, m.NonceValue())
			a.renderMessages()
		case ok && !m.Pending && (m.Author.ID == me || a.st.CanManage(m.ChannelID)):
			a.confirmDelete(m)
		}
	case 'y':
		if ok {
			if err := copyToClipboard(m.Content); err != nil {
				a.flashErr(err)
			} else {
				a.flash("copied message text")
			}
		}
	case 'i':
		a.focus(a.input)
	default:
		return ev
	}
	return nil
}

func (a *App) startReply(m discord.Message) {
	a.editing = nil
	a.replyTo = &m
	a.renderTyping()
	a.focus(a.input)
}

func (a *App) startEdit(m discord.Message) {
	a.replyTo = nil
	a.editing = &m
	a.input.SetText(m.Content, true)
	a.renderTyping()
	a.focus(a.input)
}

func (a *App) cancelCompose() {
	if a.editing != nil {
		a.input.SetText("", false)
	}
	a.editing, a.replyTo = nil, nil
	a.renderTyping()
}

func (a *App) confirmDelete(m discord.Message) {
	text := strings.Join(strings.Fields(m.Content), " ")
	if len([]rune(text)) > 80 {
		text = string([]rune(text)[:80]) + "…"
	}
	modal := tview.NewModal().
		SetText("Delete this message?\n\n" + text).
		AddButtons([]string{"Delete", "Cancel"}).
		SetDoneFunc(func(_ int, label string) {
			a.pages.RemovePage("confirm")
			a.focus(a.msgs)
			if label != "Delete" {
				return
			}
			go func() {
				ctx, cancel := context.WithTimeout(a.ctx, 15*time.Second)
				defer cancel()
				if err := a.rest.Delete(ctx, m.ChannelID, m.ID); err != nil {
					a.tv.QueueUpdateDraw(func() { a.flashErr(err) })
				}
			}()
		})
	a.pages.AddPage("confirm", modal, false, true)
	a.tv.SetFocus(modal)
}
