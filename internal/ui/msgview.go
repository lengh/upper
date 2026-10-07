package ui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/lengh/upper/internal/discord"
	"github.com/rivo/tview"
)

type rowKind int

const (
	rowMsg     rowKind = iota // a line belonging to a message
	rowDay                    // date separator
	rowNew                    // unread marker
	rowIntro                  // channel introduction at the very top
	rowLoading                // older history placeholder
	rowBlank
)

// mrow is one screen row of the conversation. Only rows in the viewport are
// drawn, so channels with long histories cost the same as short ones.
type mrow struct {
	kind     rowKind
	msg      int  // index into msgView.msgs, -1 for decorations
	head     bool // first row of a message: carries time and name
	group    bool // head of an author group (full-colour name)
	showTime bool
	system   bool
	text     line
}

type cached struct {
	sig   string
	width int
	rows  []mrow
}

// msgView renders a channel as a WeeChat-style timeline:
//
//	13:28     Alice │ hey, long messages wrap with a hanging
//	                │ indent so the text column stays clean
//	          Alice │ follow-ups from the same person dim the name
//	13:30        Me │ hi!
//
// Names are right-aligned against the rule; time shows when it changes.
type msgView struct {
	*tview.Box
	a *App

	ch       Snowflake
	msgs     []discord.Message
	more     bool
	rows     []mrow
	width    int
	dirty    bool
	scroll   int // rows from the bottom; 0 follows new messages
	selected string
	newSince Snowflake
	loadErr  string
	nickW    int
	timeW    int
	textX    int // offset of the text column inside the view
	textW    int
	cache    map[string]cached

	anchor      string // message key kept in place across reloads
	anchorDelta int

	hint       *hintState // jump labels on screen
	query      string     // active search, lower-case
	matches    []int
	match      int
	positioned bool // opened at the first unread yet
}

func newMsgView(a *App) *msgView {
	return &msgView{Box: tview.NewBox(), a: a, cache: map[string]cached{}}
}

func msgKey(m *discord.Message) string {
	if m.Pending {
		return "p" + m.NonceValue().String()
	}
	return m.ID.String()
}

func (v *msgView) setChannel(id Snowflake) {
	v.ch = id
	v.scroll = 0
	v.selected = ""
	v.loadErr = ""
	v.anchor = ""
	v.newSince = v.a.st.LastRead(id)
	v.hint, v.query, v.matches, v.positioned = nil, "", nil, false
	clear(v.cache)
	v.reload()
}

// reload re-reads the channel's messages, keeping whatever is on screen in
// the same place when scrolled back.
func (v *msgView) reload() {
	if v.scroll > 0 && len(v.rows) > 0 {
		b := len(v.rows) - 1 - v.scroll
		for i := b; i >= 0; i-- {
			if r := v.rows[i]; r.kind == rowMsg && r.msg >= 0 && r.msg < len(v.msgs) {
				v.anchor = msgKey(&v.msgs[r.msg])
				first := i
				for first > 0 && v.rows[first-1].msg == r.msg && v.rows[first-1].kind == rowMsg {
					first--
				}
				v.anchorDelta = b - first
				break
			}
		}
	}
	v.msgs, v.more = v.a.st.Messages(v.ch)
	v.dirty = true
}

// markAway moves the unread marker to the newest message, so whatever
// arrives while the terminal is in the background gets marked.
func (v *msgView) markAway() {
	for i := len(v.msgs) - 1; i >= 0; i-- {
		if !v.msgs[i].Pending {
			v.newSince = v.msgs[i].ID
			v.dirty = true
			return
		}
	}
}

// ---- Layout ------------------------------------------------------------------

func (v *msgView) columns(w int) (timeW, nickW, textX int) {
	a := v.a
	if a.cfg.TimeFormat != "" && w >= 60 {
		timeW = textWidth(time.Now().Format(a.cfg.TimeFormat))
	}
	nickW = 6
	for i := range v.msgs {
		nickW = max(nickW, textWidth(a.st.Author(v.msgs[i].GuildID, v.msgs[i].Author).Name))
	}
	limit := a.cfg.NickWidth
	if w < 60 {
		limit = min(limit, 10)
	}
	nickW = min(nickW, limit)
	textX = 1 + nickW + 3 // pad, name, " │ "
	if timeW > 0 {
		textX += timeW + 1
	}
	return
}

func (v *msgView) layout(w int) {
	a, th := v.a, v.a.th
	timeW, nickW, textX := v.columns(w)
	v.timeW, v.nickW, v.textX = timeW, nickW, textX
	// Cap the measure on very wide terminals: long lines are hard to read.
	v.textW = max(10, min(w-textX-1, 110))
	v.width = w
	v.dirty = false

	var rows []mrow
	switch {
	case a.loading[v.ch] && len(v.msgs) > 0:
		rows = append(rows, mrow{kind: rowLoading, msg: -1})
	case v.more:
		rows = append(rows, mrow{kind: rowLoading, msg: -1})
	default:
		rows = append(rows, v.intro()...)
	}

	me := a.st.Me().ID
	var prev *discord.Message
	prevDay := ""
	newShown := false
	for i := range v.msgs {
		m := &v.msgs[i]
		ts := msgTime(m)
		day := ts.Format("2006-01-02")
		if day != prevDay {
			if prevDay != "" || !v.more {
				rows = append(rows, mrow{kind: rowDay, msg: -1, text: line{{text: dayLabel(ts)}}})
			}
			prevDay, prev = day, nil
		}
		if !newShown && v.newSince != 0 && !m.Pending && m.ID > v.newSince && m.Author.ID != me {
			rows = append(rows, mrow{kind: rowNew, msg: -1})
			newShown, prev = true, nil
		}

		group := prev == nil || prev.Author.ID != m.Author.ID || m.ReferencedMessage != nil ||
			isSystem(m) || isSystem(prev) || ts.Sub(msgTime(prev)) > 7*time.Minute
		showTime := group || ts.Format("15:04") != msgTime(prev).Format("15:04")

		mr := v.messageRows(m)
		// A reply's preview row would read as part of the message above
		// it; a blank row makes the association unambiguous.
		if m.ReferencedMessage != nil && prev != nil {
			rows = append(rows, mrow{kind: rowBlank, msg: -1})
		}
		for k := range mr {
			mr[k].msg = i
			if mr[k].head {
				mr[k].group, mr[k].showTime = group, showTime
			}
		}
		rows = append(rows, mr...)
		prev = m
	}
	_ = th

	// Keep the anchored message where it was on screen.
	if v.anchor != "" && v.scroll > 0 {
		for i, r := range rows {
			if r.kind == rowMsg && r.msg >= 0 && msgKey(&v.msgs[r.msg]) == v.anchor {
				v.scroll = max(0, len(rows)-1-(i+v.anchorDelta))
				break
			}
		}
	}
	v.anchor = ""
	v.rows = rows
}

func msgTime(m *discord.Message) time.Time {
	switch {
	case m.Pending:
		return time.Now()
	case m.Timestamp.IsZero():
		return m.ID.Time().Local()
	}
	return m.Timestamp.Local()
}

func dayLabel(t time.Time) string {
	now := time.Now()
	y, m, d := now.Date()
	today := time.Date(y, m, d, 0, 0, 0, 0, now.Location())
	switch {
	case !t.Before(today):
		return "Today"
	case !t.Before(today.AddDate(0, 0, -1)):
		return "Yesterday"
	case t.Year() == now.Year():
		return t.Format("Monday, 2 January")
	}
	return t.Format("Monday, 2 January 2006")
}

func isSystem(m *discord.Message) bool {
	switch m.Type {
	case discord.MessageDefault, discord.MessageReply, 20, 23:
		return false
	}
	return true
}

func (v *msgView) intro() []mrow {
	a, th := v.a, v.a.th
	ch, _ := a.st.Channel(v.ch)
	title := a.st.ChannelTitle(v.ch)
	var desc string
	switch ch.Type {
	case discord.ChannelDM:
		desc = "This is the beginning of your direct messages with " + strings.TrimPrefix(title, "@") + "."
		title = strings.TrimPrefix(title, "@")
	case discord.ChannelGroupDM:
		desc = "This is the beginning of this group."
	default:
		title = ch.Name
		desc = "This is the start of #" + ch.Name + "."
	}
	rows := []mrow{
		{kind: rowBlank, msg: -1},
		{kind: rowIntro, msg: -1, text: line{
			{text: channelGlyph(ch.Type) + " ", style: th.accent().Bold(true)},
			{text: title, style: th.fg(th.Text).Bold(true)}}},
	}
	for _, l := range wrap([]span{{text: desc, style: th.fg(th.Subtle)}}, v.textW) {
		rows = append(rows, mrow{kind: rowIntro, msg: -1, text: l})
	}
	if ch.Topic != "" {
		md := v.renderer(ch.GuildID)
		md.base = th.muted()
		for _, l := range wrap(md.render(ch.Topic), v.textW) {
			rows = append(rows, mrow{kind: rowIntro, msg: -1, text: l})
		}
	}
	return append(rows, mrow{kind: rowBlank, msg: -1})
}

func (v *msgView) renderer(guild Snowflake) *md {
	a, th := v.a, v.a.th
	return &md{
		th: th, r: a.st, guild: guild, me: a.st.Me().ID, base: th.base(),
		nick: func(u Snowflake) tcell.Color { return th.nickColor(uint64(u), a.st.MemberColor(guild, u)) },
		role: func(r Snowflake) tcell.Color {
			if c := a.st.RoleColor(guild, r); c != 0 {
				return th.legible(c)
			}
			return th.Accent
		},
	}
}

func signature(m *discord.Message) string {
	var b strings.Builder
	b.WriteString(m.Content)
	if m.EditedTimestamp != nil {
		b.WriteString(m.EditedTimestamp.String())
	}
	fmt.Fprintf(&b, "|%v%v|%d|%d|", m.Pending, m.Failed, len(m.Embeds), len(m.Attachments))
	for _, r := range m.Reactions {
		fmt.Fprintf(&b, "%s%d%v,", r.Emoji.Key(), r.Count, r.Me)
	}
	return b.String()
}

// messageRows lays out one message: optional reply preview, the wrapped
// body, then attachments, embeds and reactions as quieter rows.
func (v *msgView) messageRows(m *discord.Message) []mrow {
	key, sig := msgKey(m), signature(m)
	if c, ok := v.cache[key]; ok && c.sig == sig && c.width == v.textW {
		return append([]mrow(nil), c.rows...)
	}
	a, th := v.a, v.a.th
	var rows []mrow
	add := func(l line, head bool) { rows = append(rows, mrow{kind: rowMsg, head: head, text: l}) }
	sub := func(spans []span) {
		for _, l := range wrap(spans, v.textW) {
			add(l, false)
		}
	}

	// Reply preview above the message, like Discord.
	if r := m.ReferencedMessage; r != nil {
		au := a.st.Author(m.GuildID, r.Author)
		snippet := strings.Join(strings.Fields(stripMarkup(plainContent(r.Content, m.GuildID, a.st))), " ")
		if snippet == "" {
			snippet = "(attachment)"
		}
		name := truncate(au.Name, 20)
		l := line{
			{text: "╭─ ", style: th.faint()},
			{text: name, style: th.fg(th.nickColor(uint64(r.Author.ID), au.Color))},
			{text: " ", style: th.base()},
		}
		l = append(l, span{text: truncate(snippet, v.textW-textWidth(name)-4), style: th.muted()})
		add(l, false)
	} else if m.Type == discord.MessageReply && m.MessageReference != nil {
		add(line{{text: "╭─ ", style: th.faint()}, {text: "original message was deleted", style: th.muted().Italic(true)}}, false)
	}

	if isSystem(m) {
		rows = append(rows, mrow{kind: rowMsg, head: true, system: true, text: v.systemLine(m)})
	} else {
		md := v.renderer(m.GuildID)
		body := md.render(m.Content)
		if m.EditedTimestamp != nil {
			body = append(body, span{text: " (edited)", style: th.faint()})
		}
		if m.Content == "" && len(m.Attachments)+len(m.Stickers)+len(m.Embeds) == 0 {
			body = []span{{text: "(empty message)", style: th.muted().Italic(true)}}
		}
		if len(body) > 0 {
			for i, l := range wrap(body, v.textW) {
				add(l, i == 0)
			}
		}
	}
	for _, at := range m.Attachments {
		l := line{{text: "📎 ", style: th.muted()}, {text: at.Filename, style: th.fg(th.Subtle)},
			{text: " · " + humanSize(at.Size), style: th.muted()}}
		add(l, len(rows) == 0 || !hasHead(rows))
	}
	for _, s := range m.Stickers {
		add(line{{text: "▣ sticker · ", style: th.muted()}, {text: s.Name, style: th.fg(th.Subtle)}}, !hasHead(rows))
	}
	for _, e := range m.Embeds {
		for _, l := range v.embed(m, e) {
			add(l, !hasHead(rows))
		}
	}
	if len(m.Reactions) > 0 {
		var spans []span
		for i, r := range m.Reactions {
			name := r.Emoji.Name
			if r.Emoji.ID != 0 {
				name = ":" + name + ":"
			}
			st := th.surface(th.fg(th.Subtle))
			if r.Me {
				st = th.surface(th.accent()).Bold(true)
			}
			if i > 0 {
				spans = append(spans, span{text: " ", style: th.base()})
			}
			spans = append(spans, span{text: " " + name + " " + strconv.Itoa(r.Count) + " ", style: st})
		}
		sub(spans)
	}
	if m.Failed {
		add(line{{text: "✗ not sent · ", style: th.errorStyle().Bold(true)},
			{text: "select it and press Enter to retry or d to discard", style: th.errorStyle()}}, false)
	}
	if !hasHead(rows) && len(rows) > 0 {
		rows[0].head = true
	}

	v.cache[key] = cached{sig: sig, width: v.textW, rows: rows}
	return append([]mrow(nil), rows...)
}

var markupStripper = strings.NewReplacer("**", "", "__", "", "~~", "", "||", "", "`", "", "*", "")

// stripMarkup removes emphasis markers for one-line previews.
func stripMarkup(s string) string { return markupStripper.Replace(s) }

func hasHead(rows []mrow) bool {
	for _, r := range rows {
		if r.head {
			return true
		}
	}
	return false
}

func humanSize(n int) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%d KB", n>>10)
	}
	return fmt.Sprintf("%d B", n)
}

func (v *msgView) systemLine(m *discord.Message) line {
	a, th := v.a, v.a.th
	au := a.st.Author(m.GuildID, m.Author)
	name := span{text: au.Name, style: th.fg(th.nickColor(uint64(m.Author.ID), au.Color))}
	act := func(s string) line {
		return line{name, {text: " " + s, style: th.muted().Italic(true)}}
	}
	target := ""
	if len(m.Mentions) > 0 {
		target = m.Mentions[0].DisplayName()
	}
	switch m.Type {
	case 1:
		return act("added " + target + " to the group")
	case 2:
		if target != "" && target != m.Author.DisplayName() {
			return act("removed " + target + " from the group")
		}
		return act("left the group")
	case 3:
		return act("started a call")
	case 4:
		return act("renamed the channel to " + m.Content)
	case 5:
		return act("changed the channel icon")
	case 6:
		return act("pinned a message")
	case 7:
		return act("joined the server")
	case 8, 9, 10, 11:
		return act("boosted the server")
	case 18:
		return act("started a thread: " + m.Content)
	}
	if m.Content != "" {
		return act(m.Content)
	}
	return act("did something Discord-specific")
}

// embed renders link previews and bot cards as a quiet, colour-barred
// block of text. Images are left out by design.
func (v *msgView) embed(m *discord.Message, e discord.Embed) []line {
	a, th := v.a, v.a.th
	if e.Title == "" && e.Description == "" && e.Author == nil && len(e.Fields) == 0 {
		return nil
	}
	barColor := th.Faint
	if e.Color != 0 && !th.mono {
		barColor = th.legible(e.Color)
	}
	bar := span{text: "▍ ", style: th.fg(barColor)}
	w := max(8, v.textW-2)
	var out []line
	addWrapped := func(spans []span, maxRows int) {
		ls := wrap(spans, w)
		if len(ls) > maxRows {
			ls = ls[:maxRows]
			ls[maxRows-1] = append(ls[maxRows-1], span{text: "…", style: th.muted()})
		}
		for _, l := range ls {
			out = append(out, append(line{bar}, l...))
		}
	}
	if e.Provider != nil && e.Provider.Name != "" {
		addWrapped([]span{{text: e.Provider.Name, style: th.muted()}}, 1)
	}
	if e.Author != nil && e.Author.Name != "" {
		addWrapped([]span{{text: e.Author.Name, style: th.fg(th.Subtle).Bold(true)}}, 1)
	}
	if e.Title != "" {
		st := th.fg(th.Text).Bold(true)
		if e.URL != "" && !th.mono {
			st = th.fg(th.Link).Bold(true)
		}
		addWrapped([]span{{text: e.Title, style: st}}, 2)
	}
	md := v.renderer(m.GuildID)
	md.base = th.fg(th.Subtle)
	if e.Description != "" {
		limit := 6
		if e.Type == "link" || e.Type == "article" || e.Type == "video" {
			limit = 3
		}
		addWrapped(md.render(e.Description), limit)
	}
	for i, f := range e.Fields {
		if i == 6 {
			addWrapped([]span{{text: fmt.Sprintf("+%d more fields", len(e.Fields)-6), style: th.muted()}}, 1)
			break
		}
		addWrapped(append([]span{{text: f.Name + "  ", style: th.fg(th.Text).Bold(true)}}, md.render(f.Value)...), 3)
	}
	if e.Footer != nil && e.Footer.Text != "" {
		addWrapped([]span{{text: e.Footer.Text, style: th.muted()}}, 1)
	}
	_ = a
	return out
}

// ---- Drawing -----------------------------------------------------------------

func (v *msgView) Draw(scr tcell.Screen) {
	a, th := v.a, v.a.th
	x, y, w, h := v.GetRect()
	base := th.base()
	for r := 0; r < h; r++ {
		fill(scr, x, y+r, w, base)
	}
	if v.ch == 0 {
		v.drawWelcome(scr, x, y, w, h)
		return
	}
	if !a.st.HistoryLoaded(v.ch) {
		msg := spinner[a.spin%len(spinner)] + " loading " + a.st.ChannelTitle(v.ch)
		st := th.muted()
		if v.loadErr != "" {
			msg, st = "✕ "+v.loadErr, th.errorStyle()
		}
		msg = truncate(msg, w-2)
		drawText(scr, x+(w-textWidth(msg))/2, y+h/2, w, msg, st)
		return
	}
	if v.dirty || v.width != w {
		v.layout(w)
	}

	if !v.positioned {
		v.positioned = true
		v.openAtUnread(h)
	}
	maxScroll := max(0, len(v.rows)-h)
	v.scroll = max(0, min(v.scroll, maxScroll))
	first := len(v.rows) - h - v.scroll
	top := y
	if first < 0 {
		top, first = y-first, 0 // bottom-align short channels
	}
	if first == 0 && v.more && !a.loading[v.ch] {
		a.loadOlder()
	}

	me := a.st.Me().ID
	for r := 0; top+r < y+h && first+r < len(v.rows); r++ {
		v.drawRow(scr, x, top+r, w, v.rows[first+r], v.timeW, me)
	}
	if v.hint != nil {
		v.drawHintBadges(scr, x, top, first, y+h-top)
	}

	if v.scroll > 0 {
		label := " ↓ "
		if n := v.newBelow(); n > 0 {
			label = fmt.Sprintf(" ↓ %d below ", n)
		}
		st := th.badge()
		if v.newBelow() == 0 {
			st = th.surface(th.fg(th.Text))
		}
		drawText(scr, x+w-textWidth(label)-1, y+h-1, w, label, st)
	}
}

func (v *msgView) drawRow(scr tcell.Screen, x, y, w int, r mrow, timeW int, me Snowflake) {
	a, th := v.a, v.a.th
	base := th.base()
	switch r.kind {
	case rowBlank:
		return
	case rowLoading:
		msg := "↑ older messages"
		if a.loading[v.ch] {
			msg = spinner[a.spin%len(spinner)] + " loading older messages"
		}
		drawText(scr, x+(w-textWidth(msg))/2, y, w, msg, th.muted())
		return
	case rowIntro:
		drawLine(scr, x+v.textX, y, w-v.textX-1, r.text, true, nil)
		return
	case rowDay:
		label := " " + r.text[0].text + " "
		lw := textWidth(label)
		left := (w - lw) / 2
		for i := 1; i < w-1; i++ {
			scr.SetContent(x+i, y, '─', nil, th.faint())
		}
		drawText(scr, x+left, y, lw, label, th.muted().Bold(true))
		return
	case rowNew:
		st := th.errorStyle()
		for i := 1; i < w-1; i++ {
			scr.SetContent(x+i, y, '─', nil, st)
		}
		drawText(scr, x+w-7, y, 5, " new ", st.Bold(true))
		return
	}

	m := &v.msgs[r.msg]
	selected := v.selected != "" && msgKey(m) == v.selected
	highlight := a.st.MentionsMe(m)

	var bg *tcell.Color
	switch {
	case selected && !th.mono:
		c := th.Surface
		bg = &c
	case highlight && !th.mono:
		c := th.MentBg
		bg = &c
	}
	if bg != nil {
		fill(scr, x, y, w, base.Background(*bg))
	}
	rowBase := base
	if bg != nil {
		rowBase = base.Background(*bg)
	}

	col := x + 1
	if timeW > 0 {
		if r.head && r.showTime {
			st := rowBase.Foreground(th.Muted)
			if !r.group {
				st = rowBase.Foreground(th.Faint)
			}
			drawText(scr, col, y, timeW, msgTime(m).Format(a.cfg.TimeFormat), st)
		}
		col += timeW + 1
	}

	if r.head {
		au := a.st.Author(m.GuildID, m.Author)
		nick := truncate(au.Name, v.nickW)
		st := rowBase.Foreground(th.nickColor(uint64(m.Author.ID), au.Color)).Bold(true)
		switch {
		case r.system:
			nick, st = "→", rowBase.Foreground(th.Muted)
		case m.Author.ID == me && r.group:
			st = rowBase.Foreground(th.Accent).Bold(true)
		case !r.group:
			st = rowBase.Foreground(th.Faint)
		}
		if th.mono && r.group {
			st = rowBase.Bold(true)
		}
		drawRight(scr, col, y, v.nickW, nick, st)
		if m.Author.Bot && r.group && v.nickW > 8 {
			// Tag bots in the gap before the name if it fits.
			nw := textWidth(nick)
			if nw+5 <= v.nickW {
				drawText(scr, col+v.nickW-nw-4, y, 3, "bot", rowBase.Foreground(th.Muted).Italic(true))
			}
		}
	}
	col += v.nickW + 1

	gutter, gst := '│', rowBase.Foreground(th.Faint)
	switch {
	case selected:
		gutter, gst = '▌', rowBase.Foreground(th.Accent)
	case m.Failed:
		gutter, gst = '✗', rowBase.Foreground(th.Error)
	case highlight:
		gutter, gst = '┃', rowBase.Foreground(th.Mention)
	case m.Pending:
		gutter = '┆'
	}
	scr.SetContent(col, y, gutter, nil, gst)
	col += 2

	l := r.text
	_, labelled := v.hint.labelOf(r.msg)
	if m.Pending || m.Failed || v.hint != nil && !labelled {
		// Pending text is quiet; while labels are up, everything that
		// can't be picked steps back.
		l = restyle(l, func(s tcell.Style) tcell.Style { return s.Foreground(th.Faint) })
	}
	if v.query != "" {
		l = highlightLine(l, v.query, th)
	}
	drawLine(scr, col, y, x+w-col-1, l, selected, bg)
}

func restyle(l line, f func(tcell.Style) tcell.Style) line {
	out := make(line, len(l))
	for i, s := range l {
		s.style = f(s.style)
		out[i] = s
	}
	return out
}

// drawWelcome is the empty state: a logo while connecting, then a summary
// of what's waiting and the handful of keys worth knowing.
func (v *msgView) drawWelcome(scr tcell.Screen, x, y, w, h int) {
	a, th := v.a, v.a.th
	logo := []string{
		` _  _ _ __ _ __  ___ _ _ `,
		`| || | '_ \ '_ \/ -_) '_|`,
		` \_,_| .__/ .__/\___|_|  `,
		`     |_|  |_|            `,
	}
	var body []line
	if !a.ready {
		body = append(body, line{{text: spinner[a.spin%len(spinner)] + " " + a.conn.message, style: th.muted()}})
	} else {
		me := a.st.Me()
		body = append(body, line{{text: "Welcome back, ", style: th.fg(th.Subtle)}, {text: me.DisplayName(), style: th.accent().Bold(true)}})
		body = append(body, nil)
		if len(a.hotlist) == 0 {
			body = append(body, line{{text: "You're all caught up.", style: th.fg(th.OK)}})
		} else {
			n := len(a.hotlist)
			word := "conversations are"
			if n == 1 {
				word = "conversation is"
			}
			body = append(body, line{{text: fmt.Sprintf("%d %s waiting:", n, word), style: th.fg(th.Subtle)}})
			for i, c := range a.hotlist {
				if i == 5 {
					break
				}
				name := c.Name
				if !c.Type.IsPrivate() {
					name = "#" + name + "  " + c.Category
				}
				l := line{{text: fmt.Sprintf("Alt+%d  ", i+1), style: th.muted()}, {text: truncate(name, 34), style: th.fg(th.Text)}}
				if c.Mentions > 0 {
					l = append(l, span{text: " ", style: th.base()}, span{text: fmt.Sprintf(" %d ", c.Mentions), style: th.badge()})
				}
				body = append(body, l)
			}
		}
		body = append(body, nil)
		for _, kv := range [][2]string{
			{"Ctrl+K", "jump to any channel or DM"},
			{"Alt+A ", "go to the next unread conversation"},
			{"Alt+/ ", "flip back to the previous channel"},
			{"F1    ", "every key and command"},
		} {
			body = append(body, line{{text: kv[0] + "  ", style: th.fg(th.Text).Bold(true)}, {text: kv[1], style: th.muted()}})
		}
	}

	total := len(logo) + 2 + len(body)
	top := y + max(0, (h-total)/2)
	lw := textWidth(logo[0])
	for i, l := range logo {
		lx := x + max(0, (w-lw)/2)
		for j, r := range l {
			st := th.accent().Bold(true)
			if !th.mono {
				st = th.fg(blend(th.Accent, th.Link, float64(j)/float64(lw)))
			}
			if lx+j < x+w {
				scr.SetContent(lx+j, top+i, r, nil, st)
			}
		}
	}
	bw := 0
	for _, l := range body {
		bw = max(bw, lineWidth(l))
	}
	bx := x + max(1, (w-bw)/2)
	for i, l := range body {
		if top+len(logo)+2+i >= y+h {
			break
		}
		drawLine(scr, bx, top+len(logo)+2+i, w-(bx-x), l, true, nil)
	}
}

func blend(a, b tcell.Color, t float64) tcell.Color {
	ar, ag, ab := a.RGB()
	br, bg, bb := b.RGB()
	mix := func(p, q int32) int32 { return p + int32(float64(q-p)*t) }
	return tcell.NewRGBColor(mix(ar, br), mix(ag, bg), mix(ab, bb))
}

// ---- Scrolling and selection -------------------------------------------------

func (v *msgView) height() int { _, _, _, h := v.GetInnerRect(); return max(1, h) }

func (v *msgView) scrollBy(n int) {
	v.scroll = max(0, v.scroll+n)
	if v.scroll == 0 {
		v.a.scheduleAck()
	}
}

func (v *msgView) scrollTop() { v.scroll = len(v.rows) }

func (v *msgView) scrollBottom() {
	v.scroll = 0
	v.selected = ""
	v.a.scheduleAck()
}

// newBelow counts messages below the viewport.
func (v *msgView) newBelow() int {
	if v.scroll == 0 {
		return 0
	}
	bottom := len(v.rows) - 1 - v.scroll
	seen := map[int]bool{}
	for i := bottom + 1; i < len(v.rows); i++ {
		if r := v.rows[i]; r.kind == rowMsg && r.head {
			seen[r.msg] = true
		}
	}
	return len(seen)
}

func (v *msgView) clearSelection() { v.selected = "" }

func (v *msgView) msgIndex(key string) int {
	for i := range v.msgs {
		if msgKey(&v.msgs[i]) == key {
			return i
		}
	}
	return -1
}

func (v *msgView) selectLast() {
	if n := len(v.msgs); n > 0 {
		v.selectIndex(n - 1)
	}
}

func (v *msgView) selectIndex(i int) {
	if i < 0 || i >= len(v.msgs) {
		return
	}
	v.selected = msgKey(&v.msgs[i])
	v.ensureVisible(i)
}

// ensureVisible scrolls the minimum needed to show a whole message.
func (v *msgView) ensureVisible(i int) {
	if v.dirty || v.width == 0 {
		_, _, w, _ := v.GetInnerRect()
		v.layout(max(w, 20))
	}
	firstRow, lastRow := -1, -1
	for k, r := range v.rows {
		if r.kind == rowMsg && r.msg == i {
			if firstRow < 0 {
				firstRow = k
			}
			lastRow = k
		}
	}
	if firstRow < 0 {
		return
	}
	h := v.height()
	top := len(v.rows) - h - v.scroll
	bottom := top + h - 1
	switch {
	case lastRow > bottom:
		v.scroll = max(0, len(v.rows)-1-lastRow)
	case firstRow < top:
		v.scroll = max(0, len(v.rows)-h-firstRow)
	}
}

func (v *msgView) moveSelection(d int) {
	i := v.msgIndex(v.selected)
	if i < 0 {
		v.selectLast()
		return
	}
	i += d
	if i < 0 {
		v.a.loadOlder()
		i = 0
	}
	if i >= len(v.msgs) {
		v.scrollBottom()
		v.a.focusComposer()
		return
	}
	v.selectIndex(i)
}

func (v *msgView) jumpHighlight(dir int) {
	start := v.msgIndex(v.selected)
	if start < 0 {
		start = len(v.msgs)
	}
	for i := start + dir; i >= 0 && i < len(v.msgs); i += dir {
		if v.a.st.MentionsMe(&v.msgs[i]) {
			v.selectIndex(i)
			v.a.focus(v)
			return
		}
	}
	v.a.flash("no more highlights in this direction")
}

// openAtUnread starts a channel at its first unread message when there's
// more unread than fits on screen, so you read in order instead of
// landing at the end. Nothing is marked read until you reach the bottom.
func (v *msgView) openAtUnread(h int) {
	for k, r := range v.rows {
		if r.kind == rowNew && len(v.rows)-k > h {
			v.scroll = max(0, len(v.rows)-h-max(0, k-1))
			return
		}
	}
}

func (v *msgView) scrollToUnread() {
	for k, r := range v.rows {
		if r.kind == rowNew {
			v.scroll = max(0, len(v.rows)-v.height()-k)
			return
		}
	}
	v.a.flash("no unread marker in this channel")
}

func (v *msgView) selectedMessage() (discord.Message, bool) {
	if i := v.msgIndex(v.selected); i >= 0 {
		return v.msgs[i], true
	}
	return discord.Message{}, false
}

func (v *msgView) InputHandler() func(*tcell.EventKey, func(tview.Primitive)) {
	return v.WrapInputHandler(func(ev *tcell.EventKey, setFocus func(tview.Primitive)) {
		a := v.a
		if v.hint != nil {
			v.hintKey(ev)
			return
		}
		h := v.height()
		switch ev.Key() {
		case tcell.KeyUp:
			v.moveSelection(-1)
			return
		case tcell.KeyDown:
			v.moveSelection(1)
			return
		case tcell.KeyPgUp:
			v.scrollBy(h - 2)
			return
		case tcell.KeyPgDn:
			v.scrollBy(-(h - 2))
			return
		case tcell.KeyHome:
			v.scrollTop()
			if len(v.msgs) > 0 {
				v.selected = msgKey(&v.msgs[0])
			}
			return
		case tcell.KeyEnd:
			v.selectLast()
			return
		case tcell.KeyEscape:
			v.scrollBottom()
			a.focusComposer()
			return
		case tcell.KeyEnter:
			if m, ok := v.selectedMessage(); ok {
				if m.Failed {
					a.retry(m)
				} else if !m.Pending {
					a.startReply(m)
				}
			}
			return
		case tcell.KeyRune:
		default:
			return
		}
		m, ok := v.selectedMessage()
		me := a.st.Me().ID
		switch ev.Rune() {
		case 'k':
			v.moveSelection(-1)
		case 'j':
			v.moveSelection(1)
		case 'g':
			v.scrollTop()
			if len(v.msgs) > 0 {
				v.selected = msgKey(&v.msgs[0])
			}
		case 'G':
			v.selectLast()
		case 'r':
			if ok && !m.Pending {
				a.startReply(m)
			}
		case 'e':
			switch {
			case !ok || m.Pending:
			case m.Author.ID != me:
				a.flash("you can only edit your own messages")
			default:
				a.startEdit(m)
			}
		case 'a', '+':
			if ok && !m.Pending {
				a.startReact(m)
			}
		case 'd':
			switch {
			case !ok:
			case m.Failed:
				a.st.RemoveFailed(m.ChannelID, m.NonceValue())
				v.reload()
			case m.Pending:
			case m.Author.ID == me || a.st.CanManage(m.ChannelID):
				a.confirmDelete(m)
			default:
				a.flash("you can only delete your own messages here")
			}
		case 'y':
			if ok {
				if err := copyToClipboard(m.Content); err != nil {
					a.flashErr(err)
				} else {
					a.flashAt(levelOK, "copied to clipboard")
				}
			}
		case 'o':
			if ok {
				a.openLink(m)
			}
		case 'f':
			a.startHints(hintSelect)
		case '?':
			a.showHelp()
		case 'i', 'q':
			v.scrollBottom()
			a.focusComposer()
		default:
			// Typing a letter goes straight to the composer.
			a.focusComposer()
			if h := a.input.InputHandler(); h != nil {
				h(ev, setFocus)
			}
		}
	})
}

func (v *msgView) MouseHandler() func(tview.MouseAction, *tcell.EventMouse, func(tview.Primitive)) (bool, tview.Primitive) {
	return v.WrapMouseHandler(func(action tview.MouseAction, ev *tcell.EventMouse, setFocus func(tview.Primitive)) (bool, tview.Primitive) {
		x, y := ev.Position()
		if !v.InRect(x, y) {
			return false, nil
		}
		switch action {
		case tview.MouseScrollUp:
			v.scrollBy(3)
			return true, nil
		case tview.MouseScrollDown:
			v.scrollBy(-3)
			return true, nil
		case tview.MouseLeftClick:
			_, ry, _, h := v.GetRect()
			first := len(v.rows) - h - v.scroll
			top := ry
			if first < 0 {
				top, first = ry-first, 0
			}
			k := first + (y - top)
			if k >= 0 && k < len(v.rows) && v.rows[k].kind == rowMsg {
				v.selected = msgKey(&v.msgs[v.rows[k].msg])
				setFocus(v)
				return true, v
			}
			return true, nil
		}
		return false, nil
	})
}
