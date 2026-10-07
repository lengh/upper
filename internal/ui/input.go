package ui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gdamore/tcell/v2"
	"github.com/lengh/upper/internal/config"
	"github.com/lengh/upper/internal/discord"
)

// maxMessageLen is Discord's limit for accounts without Nitro. Longer input
// is split on line or word boundaries into several messages.
const maxMessageLen = 2000

type sendJob struct {
	channel Snowflake
	content string
	nonce   Snowflake
	replyTo Snowflake
}

func (a *App) onInputKey(ev *tcell.EventKey) *tcell.EventKey {
	switch ev.Key() {
	case tcell.KeyCtrlJ:
		// Ctrl+J is a newline everywhere; Alt+Enter is taken by Windows
		// Terminal's fullscreen toggle.
		return tcell.NewEventKey(tcell.KeyEnter, '\n', tcell.ModNone)
	case tcell.KeyEnter:
		if ev.Modifiers()&(tcell.ModAlt|tcell.ModShift) != 0 {
			// Newline; tview's TextArea inserts one for a plain Enter.
			return tcell.NewEventKey(tcell.KeyEnter, '\n', tcell.ModNone)
		}
		a.submit()
		return nil
	case tcell.KeyEscape:
		if a.editing != nil || a.replyTo != nil {
			a.cancelCompose()
		} else {
			a.focus(a.tree)
		}
		return nil
	case tcell.KeyUp:
		if a.input.GetText() == "" {
			if m, ok := a.lastOwnMessage(); ok {
				a.startEdit(m)
			} else {
				a.focus(a.msgs)
				a.selectLast()
			}
			return nil
		}
	case tcell.KeyPgUp, tcell.KeyPgDn:
		a.focus(a.msgs)
		a.selectLast()
		return nil
	}
	return ev
}

func (a *App) lastOwnMessage() (discord.Message, bool) {
	me := a.st.Me().ID
	for i := len(a.order) - 1; i >= 0; i-- {
		m := a.byRegion[a.order[i]]
		if m.Author.ID == me && !m.Pending && (m.Type == discord.MessageDefault || m.Type == discord.MessageReply) {
			return m, true
		}
	}
	return discord.Message{}, false
}

// onInputChanged grows the composer with its content and sends a typing
// indicator at most once every 8 seconds.
func (a *App) onInputChanged() {
	a.fitInput()
	if !a.cfg.SendTyping || a.current == 0 || a.editing != nil {
		return
	}
	text := a.input.GetText()
	if text == "" || strings.HasPrefix(text, "/") || time.Since(a.lastTyping) < 8*time.Second {
		return
	}
	a.lastTyping = time.Now()
	id := a.current
	go func() {
		ctx, cancel := context.WithTimeout(a.ctx, 10*time.Second)
		defer cancel()
		_ = a.rest.Typing(ctx, id)
	}()
}

// fitInput sizes the composer to its content, up to 8 lines.
func (a *App) fitInput() {
	_, _, w, _ := a.input.GetInnerRect()
	w = max(w, 10)
	rows := 0
	for _, line := range strings.Split(a.input.GetText(), "\n") {
		rows += max(1, (utf8.RuneCountInString(line)+w-1)/w)
	}
	a.right.ResizeItem(a.input, min(rows, 8)+2, 0)
	if rows <= 8 {
		// Everything fits; undo any scrolling done while the box was smaller.
		a.input.SetOffset(0, 0)
	}
}

func (a *App) submit() {
	text := strings.TrimSpace(a.input.GetText())
	if strings.HasPrefix(text, "/") && !strings.HasPrefix(text, "//") {
		a.input.SetText("", false)
		a.command(text)
		return
	}
	text = strings.TrimPrefix(text, "/") // "//foo" sends "/foo"
	if a.current == 0 {
		a.flash("open a channel first (Ctrl+K)")
		return
	}

	if a.editing != nil {
		m := *a.editing
		a.cancelCompose()
		a.input.SetText("", false)
		if text == "" || text == m.Content {
			return
		}
		if utf8.RuneCountInString(text) > maxMessageLen {
			a.flash("edit too long (2000 characters max)")
			return
		}
		go func() {
			ctx, cancel := context.WithTimeout(a.ctx, 15*time.Second)
			defer cancel()
			if err := a.rest.Edit(ctx, m.ChannelID, m.ID, text); err != nil {
				a.tv.QueueUpdateDraw(func() { a.flashErr(err) })
			}
		}()
		return
	}

	if text == "" {
		return
	}
	if !a.st.CanSend(a.current) {
		a.flash("you can't send messages in this channel")
		return
	}
	var reply Snowflake
	if a.replyTo != nil {
		reply = a.replyTo.ID
	}
	a.input.SetText("", false)
	a.cancelCompose()
	a.lastTyping = time.Time{}

	me := a.st.Me()
	ch, _ := a.st.Channel(a.current)
	for i, chunk := range splitMessage(text, maxMessageLen) {
		nonce := discord.NewNonce() + Snowflake(i)
		pending := discord.Message{
			ChannelID: a.current,
			GuildID:   ch.GuildID,
			Author:    me,
			Content:   chunk,
			Nonce:     []byte(`"` + nonce.String() + `"`),
		}
		a.st.AddPending(pending)
		job := sendJob{channel: a.current, content: chunk, nonce: nonce}
		if i == 0 {
			job.replyTo = reply
		}
		select {
		case a.sendQ <- job:
		default:
			a.st.ResolvePending(a.current, nonce, nil)
			a.flash("too many messages queued")
		}
	}
	a.renderMessages()
	a.msgs.ScrollToEnd()
}

// sender posts queued messages one at a time so they arrive in order.
func (a *App) sender() {
	for {
		select {
		case <-a.ctx.Done():
			return
		case job := <-a.sendQ:
			ctx, cancel := context.WithTimeout(a.ctx, 30*time.Second)
			sent, err := a.rest.Send(ctx, job.channel, job.content, job.nonce, job.replyTo, a.cfg.MentionOnReply)
			cancel()
			a.tv.QueueUpdateDraw(func() {
				if err != nil {
					a.st.ResolvePending(job.channel, job.nonce, nil)
					a.flashErr(fmt.Errorf("send failed: %w", err))
				} else {
					a.st.ResolvePending(job.channel, job.nonce, sent)
				}
				if a.current == job.channel {
					a.renderMessages()
				}
			})
		}
	}
}

func (a *App) retry(m discord.Message) {
	a.st.RemoveFailed(m.ChannelID, m.NonceValue())
	nonce := discord.NewNonce()
	m.Nonce = []byte(`"` + nonce.String() + `"`)
	m.Failed = false
	a.st.AddPending(m)
	select {
	case a.sendQ <- sendJob{channel: m.ChannelID, content: m.Content, nonce: nonce}:
	default:
		a.st.ResolvePending(m.ChannelID, nonce, nil)
	}
	a.renderMessages()
}

// splitMessage splits s into chunks of at most limit runes, preferring line
// breaks, then spaces.
func splitMessage(s string, limit int) []string {
	var out []string
	for utf8.RuneCountInString(s) > limit {
		r := []rune(s)
		cut := string(r[:limit])
		idx := strings.LastIndex(cut, "\n")
		if idx < limit/2 {
			idx = strings.LastIndex(cut, " ")
		}
		if idx < limit/4 {
			idx = len(cut)
		}
		out = append(out, strings.TrimRight(s[:idx], " \n"))
		s = strings.TrimLeft(s[idx:], " \n")
	}
	if s != "" {
		out = append(out, s)
	}
	return out
}

// ---- Commands ----------------------------------------------------------------

func (a *App) command(line string) {
	fields := strings.Fields(line)
	cmd, args := strings.ToLower(fields[0]), fields[1:]
	switch cmd {
	case "/quit", "/q", "/exit":
		a.tv.Stop()
	case "/help", "/?":
		a.showHelp()
	case "/dm", "/msg", "/query":
		if len(args) == 0 {
			a.flash("usage: /dm <username> [message]")
			return
		}
		a.openDM(args[0], strings.Join(args[1:], " "))
	case "/read":
		// Mark every channel read.
		for _, c := range a.st.AllChannels() {
			if c.Unread || c.Mentions > 0 {
				id := c.ID
				if msg := a.st.MarkRead(id); msg != 0 {
					go func() {
						ctx, cancel := context.WithTimeout(a.ctx, 15*time.Second)
						defer cancel()
						_ = a.rest.Ack(ctx, id, msg)
					}()
				}
			}
		}
		a.nav.refresh()
		a.flash("marked everything read")
	case "/logout":
		if err := config.DeleteToken(); err != nil {
			a.flashErr(err)
			return
		}
		a.tv.Stop()
	case "/sidebar":
		a.toggleSidebar()
	default:
		a.flash("unknown command " + cmd + " (try /help; start with // to send a literal slash)")
	}
}

func (a *App) openDM(name, message string) {
	u, ok := a.st.FindUser(name)
	if !ok {
		a.flash("no friend or known user named " + name)
		return
	}
	if id, ok := a.st.DMWith(u.ID); ok {
		a.open(id)
		if message != "" {
			a.input.SetText(message, true)
			a.submit()
		}
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(a.ctx, 15*time.Second)
		defer cancel()
		ch, err := a.rest.OpenDM(ctx, u.ID)
		a.tv.QueueUpdateDraw(func() {
			if err != nil {
				var he *discord.HTTPError
				if errors.As(err, &he) && he.Code == 50007 {
					err = fmt.Errorf("%s doesn't accept DMs from you", u.DisplayName())
				}
				a.flashErr(err)
				return
			}
			a.st.AddChannel(*ch)
			a.nav.refresh()
			a.open(ch.ID)
			if message != "" {
				a.input.SetText(message, true)
				a.submit()
			}
		})
	}()
}
