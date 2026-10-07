package ui

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gdamore/tcell/v2"
	"github.com/lengh/upper/internal/config"
	"github.com/lengh/upper/internal/discord"
	"github.com/lengh/upper/internal/state"
)

// maxMessageLen is Discord's limit for accounts without Nitro. Longer input
// is split on line or word boundaries into several messages.
const maxMessageLen = 2000

type composeMode int

const (
	modeNormal composeMode = iota
	modeReply
	modeEdit
	modeReact
)

type sendJob struct {
	channel Snowflake
	content string
	nonce   Snowflake
	replyTo Snowflake
}

func runeLen(s string) int { return utf8.RuneCountInString(s) }

// ---- Keys --------------------------------------------------------------------

func (a *App) onComposerKey(ev *tcell.EventKey) *tcell.EventKey {
	text := a.input.GetText()
	switch ev.Key() {
	case tcell.KeyCtrlJ:
		// Ctrl+J is a newline everywhere; Alt+Enter is taken by Windows
		// Terminal's fullscreen toggle.
		return tcell.NewEventKey(tcell.KeyEnter, '\n', tcell.ModNone)
	case tcell.KeyEnter:
		if ev.Modifiers()&(tcell.ModAlt|tcell.ModShift) != 0 {
			return tcell.NewEventKey(tcell.KeyEnter, '\n', tcell.ModNone)
		}
		a.submit()
		return nil
	case tcell.KeyTab:
		if !a.complete(1) && text == "" {
			a.cycleFocus(false)
		}
		return nil
	case tcell.KeyBacktab:
		if a.comp != nil {
			a.complete(-1)
		} else {
			a.cycleFocus(true)
		}
		return nil
	case tcell.KeyEscape:
		switch {
		case a.comp != nil:
			a.comp = nil
		case a.mode != modeNormal:
			a.resetCompose()
		case a.view.scroll > 0:
			a.view.scrollBottom()
		}
		return nil
	case tcell.KeyUp:
		if ev.Modifiers()&tcell.ModCtrl != 0 {
			a.enterSelection()
			return nil
		}
		if text == "" {
			if m, ok := a.lastOwnMessage(); ok {
				a.startEdit(m)
			} else {
				a.enterSelection()
			}
			return nil
		}
	case tcell.KeyPgUp:
		a.view.scrollBy(a.view.height() - 2)
		return nil
	case tcell.KeyPgDn:
		a.view.scrollBy(-(a.view.height() - 2))
		return nil
	}
	return ev
}

func (a *App) enterSelection() {
	if len(a.view.msgs) == 0 {
		return
	}
	a.focus(a.view)
	a.view.selectLast()
}

func (a *App) onComposerChanged() {
	if !a.completing {
		a.comp = nil
	}
	a.updatePlaceholder()
	if !a.cfg.SendTyping || a.current == 0 || a.mode == modeEdit || a.mode == modeReact {
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

func (a *App) updatePlaceholder() {
	ph := ""
	switch {
	case a.mode == modeReact:
		ph = "an emoji like :thumbsup: or 👍 · Tab completes · Esc cancels"
	case a.mode == modeEdit:
		ph = "empty the text and press Enter to keep the original"
	case a.current == 0:
		ph = "press Ctrl+K to open a conversation"
	case !a.st.CanSend(a.current):
		ph = "you can't send messages in this channel"
	default:
		ph = "Message " + a.st.ChannelTitle(a.current)
		if ch, ok := a.st.Channel(a.current); ok && ch.GuildID != 0 {
			ph = "Message #" + ch.Name
		}
	}
	a.input.SetPlaceholder(ph)
}

// ---- Compose modes -----------------------------------------------------------

func (a *App) lastOwnMessage() (discord.Message, bool) {
	me := a.st.Me().ID
	for i := len(a.view.msgs) - 1; i >= 0; i-- {
		m := a.view.msgs[i]
		if m.Author.ID == me && !m.Pending && !isSystem(&m) {
			return m, true
		}
	}
	return discord.Message{}, false
}

func (a *App) startReply(m discord.Message) {
	a.mode, a.target = modeReply, &m
	a.updatePlaceholder()
	a.focusComposer()
}

func (a *App) startEdit(m discord.Message) {
	a.mode, a.target = modeEdit, &m
	a.input.SetText(m.Content, true)
	a.updatePlaceholder()
	a.focusComposer()
}

func (a *App) startReact(m discord.Message) {
	if a.mode == modeNormal {
		if t := a.input.GetText(); t != "" {
			a.drafts[a.current] = t
		}
	}
	a.mode, a.target = modeReact, &m
	a.input.SetText("", false)
	a.updatePlaceholder()
	a.focusComposer()
}

// resetCompose leaves reply/edit/react mode. Edits and reactions discard
// their text; a reply keeps what you typed as a normal message draft.
func (a *App) resetCompose() {
	switch a.mode {
	case modeEdit, modeReact:
		a.input.SetText(a.drafts[a.current], true)
		delete(a.drafts, a.current)
	}
	a.mode, a.target, a.comp = modeNormal, nil, nil
	a.updatePlaceholder()
}

// ---- Sending -----------------------------------------------------------------

var (
	reSed   = regexp.MustCompile(`^s/((?:[^/\\]|\\.)+)/((?:[^/\\]|\\.)*)/?$`)
	reQuick = regexp.MustCompile(`^\+(:[a-z0-9_+-]+:|\S{1,16})$`)
)

func (a *App) submit() {
	raw := a.input.GetText()
	text := strings.TrimSpace(raw)

	switch a.mode {
	case modeEdit:
		a.submitEdit(text)
		return
	case modeReact:
		target := *a.target
		a.resetCompose()
		a.react(target, text)
		return
	}

	if strings.HasPrefix(text, "/") && !strings.HasPrefix(text, "//") {
		a.input.SetText("", false)
		a.command(text)
		return
	}
	text = strings.TrimPrefix(text, "/") // "//foo" sends "/foo"
	if text == "" {
		return
	}
	if a.current == 0 {
		a.flash("open a conversation first · Ctrl+K")
		return
	}

	// Discord's quick edits and reactions: s/old/new and +:emoji:.
	if m := reSed.FindStringSubmatch(text); m != nil && a.mode == modeNormal {
		if own, ok := a.lastOwnMessage(); ok {
			old, repl := unescapeSlash(m[1]), unescapeSlash(m[2])
			if !strings.Contains(own.Content, old) {
				a.flashAt(levelWarn, fmt.Sprintf("“%s” isn't in your last message", old))
				return
			}
			a.input.SetText("", false)
			a.edit(own, strings.Replace(own.Content, old, repl, 1))
			return
		}
	}
	if m := reQuick.FindStringSubmatch(text); m != nil && a.mode == modeNormal {
		if last := len(a.view.msgs) - 1; last >= 0 {
			a.input.SetText("", false)
			a.react(a.view.msgs[last], m[1])
			return
		}
	}

	if !a.st.CanSend(a.current) {
		a.flashAt(levelWarn, "you can't send messages in this channel")
		return
	}
	var reply Snowflake
	if a.mode == modeReply && a.target != nil {
		reply = a.target.ID
	}
	a.input.SetText("", false)
	a.resetCompose()
	a.lastTyping = time.Time{}

	text = a.prepareOutgoing(text)
	me := a.st.Me()
	ch, _ := a.st.Channel(a.current)
	for i, chunk := range splitMessage(text, maxMessageLen) {
		nonce := discord.NewNonce() + Snowflake(i)
		pending := discord.Message{
			ChannelID: a.current, GuildID: ch.GuildID, Author: me, Content: chunk,
			Nonce: []byte(`"` + nonce.String() + `"`),
		}
		job := sendJob{channel: a.current, content: chunk, nonce: nonce}
		if i == 0 {
			job.replyTo = reply
			if a.target != nil {
				pending.ReferencedMessage = a.target
			}
		}
		a.st.AddPending(pending)
		select {
		case a.sendQ <- job:
		default:
			a.st.ResolvePending(a.current, nonce, nil)
			a.flashAt(levelError, "too many messages queued; wait a moment")
		}
	}
	a.view.scrollBottom()
	a.view.reload()
}

func unescapeSlash(s string) string { return strings.ReplaceAll(s, `\/`, "/") }

func (a *App) submitEdit(text string) {
	m := *a.target
	a.mode, a.target = modeNormal, nil
	a.input.SetText(a.drafts[a.current], true)
	delete(a.drafts, a.current)
	a.updatePlaceholder()
	if text == "" || text == m.Content {
		a.flash("edit cancelled")
		return
	}
	a.edit(m, a.prepareOutgoing(text))
}

func (a *App) edit(m discord.Message, content string) {
	if runeLen(content) > maxMessageLen {
		a.flashAt(levelWarn, "an edit can't be longer than 2000 characters")
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(a.ctx, 15*time.Second)
		defer cancel()
		err := a.rest.Edit(ctx, m.ChannelID, m.ID, content)
		a.tv.QueueUpdateDraw(func() {
			if err != nil {
				a.flashErr(err)
			}
		})
	}()
}

// react toggles a reaction given as a shortcode (:fire:) or the emoji itself.
func (a *App) react(m discord.Message, input string) {
	input = strings.TrimSpace(input)
	if input == "" {
		return
	}
	emoji := input
	if strings.HasPrefix(input, ":") && strings.HasSuffix(input, ":") && len(input) > 2 {
		e, ok := emojiByName[strings.Trim(input, ":")]
		if !ok {
			a.flashAt(levelWarn, "unknown emoji "+input+" · try Tab to complete")
			return
		}
		emoji = e
	}
	remove := false
	for _, r := range m.Reactions {
		if r.Me && r.Emoji.ID == 0 && r.Emoji.Name == emoji {
			remove = true
		}
	}
	go func() {
		ctx, cancel := context.WithTimeout(a.ctx, 15*time.Second)
		defer cancel()
		var err error
		if remove {
			err = a.rest.Unreact(ctx, m.ChannelID, m.ID, emoji)
		} else {
			err = a.rest.React(ctx, m.ChannelID, m.ID, emoji)
		}
		a.tv.QueueUpdateDraw(func() {
			if err != nil {
				a.flashErr(err)
			}
		})
	}()
}

// prepareOutgoing turns what you typed into what Discord expects: @names
// and #channels become real mentions and :shortcodes: become emoji. Code
// spans are left exactly as typed.
func (a *App) prepareOutgoing(text string) string {
	people := a.st.MentionCandidates(a.current)
	chans := a.st.ChannelCandidates(a.current)
	return mapOutsideCode(text, func(s string) string {
		s = replaceRefs(s, '@', people, "<@", ">")
		s = replaceRefs(s, '#', chans, "<#", ">")
		return replaceShortcodes(s)
	})
}

var reCode = regexp.MustCompile("(?s)```.*?```|`[^`\n]+`")

func mapOutsideCode(s string, f func(string) string) string {
	var b strings.Builder
	last := 0
	for _, loc := range reCode.FindAllStringIndex(s, -1) {
		b.WriteString(f(s[last:loc[0]]))
		b.WriteString(s[loc[0]:loc[1]])
		last = loc[1]
	}
	b.WriteString(f(s[last:]))
	return b.String()
}

// replaceRefs converts sigil+name into a mention token. Names may contain
// spaces, so the longest matching name wins.
func replaceRefs(s string, sigil byte, cands []state.Candidate, open, close string) string {
	if len(cands) == 0 || strings.IndexByte(s, sigil) < 0 {
		return s
	}
	sorted := slices.Clone(cands)
	slices.SortFunc(sorted, func(x, y state.Candidate) int { return len(y.Name) - len(x.Name) })
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] != sigil || i > 0 && isWordByte(s[i-1]) {
			b.WriteByte(s[i])
			i++
			continue
		}
		rest := s[i+1:]
		matched := false
		for _, c := range sorted {
			for _, name := range []string{c.Name, c.Alt} {
				if name == "" || len(rest) < len(name) || !strings.EqualFold(rest[:len(name)], name) {
					continue
				}
				if len(rest) > len(name) && isWordByte(rest[len(name)]) {
					continue
				}
				b.WriteString(open + c.ID.String() + close)
				i += 1 + len(name)
				matched = true
				break
			}
			if matched {
				break
			}
		}
		if !matched {
			b.WriteByte(s[i])
			i++
		}
	}
	return b.String()
}

func isWordByte(c byte) bool {
	return c == '_' || c == '-' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= 0x80
}

var reShortcode = regexp.MustCompile(`:([a-z0-9_+-]{2,32}):`)

func replaceShortcodes(s string) string {
	if !strings.Contains(s, ":") {
		return s
	}
	return reShortcode.ReplaceAllStringFunc(s, func(m string) string {
		if e, ok := emojiByName[m[1:len(m)-1]]; ok {
			return e
		}
		return m
	})
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
					a.flashErr(err)
				} else {
					a.st.ResolvePending(job.channel, job.nonce, sent)
				}
				if a.current == job.channel {
					a.view.reload()
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
	a.view.reload()
}

func (a *App) confirmDelete(m discord.Message) {
	a.ask("Delete this message?", func() {
		go func() {
			ctx, cancel := context.WithTimeout(a.ctx, 15*time.Second)
			defer cancel()
			err := a.rest.Delete(ctx, m.ChannelID, m.ID)
			a.tv.QueueUpdateDraw(func() {
				if err != nil {
					a.flashErr(err)
				} else {
					a.flashAt(levelOK, "message deleted")
				}
			})
		}()
	})
}

// splitMessage splits s into chunks of at most limit runes, preferring line
// breaks, then spaces.
func splitMessage(s string, limit int) []string {
	var out []string
	for runeLen(s) > limit {
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

// ---- Completion --------------------------------------------------------------

type compItem struct {
	label  string // shown in the status bar
	insert string // replaces the word being completed
}

// completion cycles through candidates for the word before the cursor, as
// in IRC clients: Tab for the next, Shift+Tab for the previous. Recent
// speakers come first.
type completion struct {
	start, end int
	items      []compItem
	index      int
}

// complete returns false when there was nothing to complete.
func (a *App) complete(dir int) bool {
	if c := a.comp; c != nil && len(c.items) > 0 {
		c.index = (c.index + dir + len(c.items)) % len(c.items)
		a.applyCompletion()
		return true
	}
	text := a.input.GetText()
	_, cursor, _ := a.input.GetSelection()
	start := strings.LastIndexAny(text[:cursor], " \n") + 1
	word := text[start:cursor]
	if word == "" {
		return false
	}
	items := a.candidates(word, start == 0)
	if len(items) == 0 {
		a.flash("no completions for " + word)
		return true
	}
	a.comp = &completion{start: start, end: cursor, items: items}
	a.applyCompletion()
	return true
}

func (a *App) applyCompletion() {
	c := a.comp
	it := c.items[c.index]
	a.completing = true
	a.input.Replace(c.start, c.end, it.insert)
	c.end = c.start + len(it.insert)
	a.input.Select(c.end, c.end)
	a.completing = false
}

func (a *App) candidates(word string, lineStart bool) []compItem {
	lw := strings.ToLower(word)
	var out []compItem
	switch {
	case lineStart && strings.HasPrefix(word, "/"):
		for _, c := range commands {
			if strings.HasPrefix(c.name, lw) {
				out = append(out, compItem{label: c.name, insert: c.name + " "})
			}
		}
	case strings.HasPrefix(word, ":") && len(word) >= 2:
		q := strings.Trim(lw, ":")
		for _, e := range emojiList {
			if strings.HasPrefix(e.name, q) {
				out = append(out, compItem{label: e.emoji + " :" + e.name + ":", insert: e.emoji})
				if len(out) == 40 {
					break
				}
			}
		}
		if a.mode != modeReact {
			for i := range out {
				out[i].insert += " "
			}
		}
	case strings.HasPrefix(word, "#"):
		q := strings.ToLower(word[1:])
		for _, c := range a.st.ChannelCandidates(a.current) {
			if strings.HasPrefix(strings.ToLower(c.Name), q) {
				out = append(out, compItem{label: "#" + c.Name, insert: "#" + c.Name + " "})
			}
		}
	default:
		// "@al" or IRC-style bare "al" complete people.
		q := strings.TrimPrefix(lw, "@")
		var prefix, contains []compItem
		for _, c := range a.st.MentionCandidates(a.current) {
			item := compItem{label: "@" + c.Name, insert: "@" + c.Name + " "}
			name, alt := strings.ToLower(c.Name), strings.ToLower(c.Alt)
			switch {
			case strings.HasPrefix(name, q) || strings.HasPrefix(alt, q):
				prefix = append(prefix, item)
			case strings.HasPrefix(word, "@") && (strings.Contains(name, q) || strings.Contains(alt, q)):
				contains = append(contains, item)
			}
		}
		out = append(prefix, contains...)
	}
	return out
}

// ---- Commands ----------------------------------------------------------------

type command struct {
	name, args, desc string
	run              func(a *App, args string)
}

var commands []command

func init() {
	commands = []command{
		{"/dm", "<user> [message]", "open a direct message with a friend", func(a *App, args string) {
			name, msg, _ := strings.Cut(args, " ")
			if name == "" {
				a.flash("usage: /dm <user> [message]")
				return
			}
			a.openDM(name, msg)
		}},
		{"/me", "<action>", "send an action, shown in italics", func(a *App, args string) { a.sendText("_" + args + "_") }},
		{"/shrug", "[message]", `append ¯\_(ツ)_/¯`, func(a *App, args string) { a.sendText(strings.TrimSpace(args + ` ¯\\\_(ツ)\_/¯`)) }},
		{"/tableflip", "[message]", "append (╯°□°)╯︵ ┻━┻", func(a *App, args string) { a.sendText(strings.TrimSpace(args + " (╯°□°)╯︵ ┻━┻")) }},
		{"/unflip", "[message]", "append ┬─┬ノ( º _ ºノ)", func(a *App, args string) { a.sendText(strings.TrimSpace(args + " ┬─┬ノ( º _ ºノ)")) }},
		{"/react", "<emoji>", "react to the last message", func(a *App, args string) {
			if n := len(a.view.msgs); n > 0 && args != "" {
				a.react(a.view.msgs[n-1], args)
			}
		}},
		{"/edit", "", "edit your last message", func(a *App, _ string) {
			if m, ok := a.lastOwnMessage(); ok {
				a.startEdit(m)
			} else {
				a.flash("you haven't sent anything here yet")
			}
		}},
		{"/open", "", "open the last link in your browser", func(a *App, _ string) {
			for i := len(a.view.msgs) - 1; i >= 0; i-- {
				if links(&a.view.msgs[i]) != nil {
					a.openLink(a.view.msgs[i])
					return
				}
			}
			a.flash("no links in view")
		}},
		{"/read", "[all]", "mark this channel (or everything) read", func(a *App, args string) {
			ids := []Snowflake{a.current}
			if args == "all" {
				ids = nil
				for _, c := range a.st.AllChannels() {
					if c.Unread || c.Mentions > 0 {
						ids = append(ids, c.ID)
					}
				}
			}
			for _, id := range ids {
				if msg := a.st.MarkRead(id); msg != 0 {
					go func() {
						ctx, cancel := context.WithTimeout(a.ctx, 15*time.Second)
						defer cancel()
						_ = a.rest.Ack(ctx, id, msg)
					}()
				}
			}
			a.refreshTree()
			a.flashAt(levelOK, "marked read")
		}},
		{"/topic", "", "show the full channel topic", func(a *App, _ string) {
			if ch, ok := a.st.Channel(a.current); ok && ch.Topic != "" {
				a.flash(strings.Join(strings.Fields(ch.Topic), " "))
			} else {
				a.flash("this channel has no topic")
			}
		}},
		{"/theme", "dark|light|mono", "switch colours", func(a *App, args string) {
			a.th = loadTheme(args)
			a.retheme()
			a.flash("theme: " + a.th.Name)
		}},
		{"/time", "", "show or hide timestamps", func(a *App, _ string) {
			if a.cfg.TimeFormat == "" {
				a.cfg.TimeFormat = "15:04"
			} else {
				a.cfg.TimeFormat = ""
			}
			a.view.dirty = true
		}},
		{"/sidebar", "", "show or hide the sidebar (Ctrl+B)", func(a *App, _ string) { a.toggleSidebar() }},
		{"/help", "", "every key and command (F1)", func(a *App, _ string) { a.showHelp() }},
		{"/logout", "", "forget your token and quit", func(a *App, _ string) {
			a.ask("Log out and forget the stored token?", func() {
				if err := config.DeleteToken(); err != nil {
					a.flashErr(err)
					return
				}
				a.tv.Stop()
			})
		}},
		{"/quit", "", "leave upper (Ctrl+Q)", func(a *App, _ string) { a.tv.Stop() }},
	}
}

func findCommand(name string) *command {
	for i := range commands {
		if commands[i].name == name {
			return &commands[i]
		}
	}
	return nil
}

// commandUsage explains the command being typed, as you type it.
func commandUsage(text string) string {
	if !strings.HasPrefix(text, "/") || strings.HasPrefix(text, "//") {
		return ""
	}
	name, _, _ := strings.Cut(text, " ")
	if c := findCommand(strings.ToLower(name)); c != nil {
		return strings.TrimSpace(c.name+" "+c.args) + " — " + c.desc
	}
	var names []string
	for _, c := range commands {
		if strings.HasPrefix(c.name, strings.ToLower(name)) {
			names = append(names, c.name)
		}
	}
	if len(names) == 0 {
		return "unknown command · start with // to send a slash"
	}
	return strings.Join(names, "  ")
}

func (a *App) command(line string) {
	name, args, _ := strings.Cut(line, " ")
	c := findCommand(strings.ToLower(name))
	if c == nil {
		a.flashAt(levelWarn, "unknown command "+name+" · F1 lists them, // sends a literal slash")
		return
	}
	c.run(a, strings.TrimSpace(args))
}

// sendText sends text to the current channel as if typed.
func (a *App) sendText(text string) {
	if strings.TrimSpace(text) == "" || text == "__" {
		return
	}
	a.input.SetText(text, true)
	a.submit()
}

func (a *App) openDM(name, message string) {
	u, ok := a.st.FindUser(name)
	if !ok {
		a.flashAt(levelWarn, "no friend or known user named "+name)
		return
	}
	send := func() {
		if message != "" {
			a.sendText(message)
		}
	}
	if id, ok := a.st.DMWith(u.ID); ok {
		a.open(id)
		send()
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
			a.refreshTree()
			a.open(ch.ID)
			send()
		})
	}()
}

// links lists the URLs in a message, in order.
func links(m *discord.Message) []string {
	out := reURL.FindAllString(m.Content, -1)
	for _, l := range reMasked.FindAllStringSubmatch(m.Content, -1) {
		out = append(out, l[2])
	}
	for _, e := range m.Embeds {
		if e.URL != "" && !slices.Contains(out, e.URL) {
			out = append(out, e.URL)
		}
	}
	return out
}

func (a *App) openLink(m discord.Message) {
	ls := links(&m)
	if len(ls) == 0 {
		a.flash("no link in this message")
		return
	}
	if err := openURL(ls[0]); err != nil {
		a.flashErr(err)
		return
	}
	msg := "opened " + truncate(ls[0], 60)
	if len(ls) > 1 {
		msg += fmt.Sprintf(" (first of %d links)", len(ls))
	}
	a.flashAt(levelOK, msg)
}
