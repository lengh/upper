package ui

import (
	"fmt"
	"strings"

	"github.com/gdamore/tcell/v2"
	"github.com/lengh/upper/internal/discord"
	"github.com/lengh/upper/internal/state"
	"github.com/rivo/tview"
)

// maxSidebarDMs bounds the unread DM list; the rest stay reachable via Ctrl+K.
const maxSidebarDMs = 15

type sideKind int

const (
	sideHeader sideKind = iota
	sideDM
	sideGuild
	sideCategory
	sideChannel
	sideMore
	sideSpacer
)

type sideRow struct {
	kind  sideKind
	id    Snowflake // channel or guild
	text  string
	ch    state.ChannelInfo
	guild state.GuildInfo
}

func (r sideRow) selectable() bool {
	return r.kind == sideDM || r.kind == sideGuild || r.kind == sideChannel || r.kind == sideMore
}

// sidebar lists DMs and servers like WeeChat's buflist: one row per
// buffer, bold when unread, a pill when you're mentioned, a bar marking
// where you are. Servers fold so a hundred of them stay scannable.
type sidebar struct {
	*tview.Box
	a         *App
	rows      []sideRow
	cursor    int
	offset    int
	collapsed map[Snowflake]bool // servers are expanded unless folded
}

func newSidebar(a *App) *sidebar {
	return &sidebar{Box: tview.NewBox(), a: a, collapsed: map[Snowflake]bool{}}
}

func (s *sidebar) expanded(guild Snowflake) bool { return !s.collapsed[guild] }

func (s *sidebar) refresh() {
	a := s.a
	var keep Snowflake
	if s.cursor < len(s.rows) {
		keep = s.rows[s.cursor].id
	}
	var rows []sideRow

	// Direct messages list only conversations with something new, plus the
	// one that's open: an inbox, not an archive. Every other DM is a
	// Ctrl+K away.
	rows = append(rows, sideRow{kind: sideHeader, text: "DIRECT MESSAGES"})
	shown := 0
	for _, c := range a.st.PrivateChannels() {
		if !c.Unread && c.Mentions == 0 && c.ID != a.current {
			continue
		}
		if shown == maxSidebarDMs && c.ID != a.current {
			continue
		}
		rows = append(rows, sideRow{kind: sideDM, id: c.ID, ch: c})
		shown++
	}
	if shown == 0 {
		rows = append(rows, sideRow{kind: sideMore, text: "no new messages"})
	}

	guilds := a.st.Guilds()
	if len(guilds) > 0 {
		rows = append(rows, sideRow{kind: sideSpacer}, sideRow{kind: sideHeader, text: "SERVERS"})
	}
	for _, g := range guilds {
		rows = append(rows, sideRow{kind: sideGuild, id: g.ID, guild: g})
		if !s.expanded(g.ID) {
			continue
		}
		cat := "\x00"
		for _, c := range a.st.GuildChannels(g.ID) {
			if c.Depth == 0 && c.Category != cat {
				cat = c.Category
				if cat != "" {
					rows = append(rows, sideRow{kind: sideCategory, text: strings.ToUpper(cat)})
				}
			}
			rows = append(rows, sideRow{kind: sideChannel, id: c.ID, ch: c})
		}
	}
	s.rows = rows

	s.cursor = -1
	for i, r := range rows {
		if r.id == keep && r.selectable() && keep != 0 {
			s.cursor = i
			break
		}
	}
	if s.cursor < 0 {
		s.cursor = s.firstSelectable(0, 1)
	}
}

func (s *sidebar) firstSelectable(from, dir int) int {
	for i := from; i >= 0 && i < len(s.rows); i += dir {
		if s.rows[i].selectable() {
			return i
		}
	}
	return max(0, min(from, len(s.rows)-1))
}

// reveal expands the server of a channel and moves the cursor onto it.
func (s *sidebar) reveal(id Snowflake) {
	if ch, ok := s.a.st.Channel(id); ok && ch.GuildID != 0 {
		delete(s.collapsed, ch.GuildID)
	}
	s.refresh()
	for i, r := range s.rows {
		if r.id == id && (r.kind == sideChannel || r.kind == sideDM) {
			s.cursor = i
		}
	}
}

// step opens the previous/next channel in sidebar order.
func (s *sidebar) step(delta int) {
	var ids []Snowflake
	cur := -1
	for _, r := range s.rows {
		if r.kind == sideChannel || r.kind == sideDM {
			if r.id == s.a.current {
				cur = len(ids)
			}
			ids = append(ids, r.id)
		}
	}
	if len(ids) == 0 {
		return
	}
	next := 0
	if cur >= 0 {
		next = (cur + delta + len(ids)) % len(ids)
	}
	s.a.open(ids[next])
}

func (s *sidebar) activate(i int) {
	if i < 0 || i >= len(s.rows) {
		return
	}
	r := s.rows[i]
	switch r.kind {
	case sideGuild:
		s.collapsed[r.id] = !s.collapsed[r.id]
		s.refresh()
		s.cursor = i
	case sideChannel, sideDM:
		s.a.open(r.id)
	case sideMore:
		s.a.openSwitcher("")
	}
}

func (s *sidebar) move(delta int) {
	i := s.cursor
	for {
		i += delta
		if i < 0 || i >= len(s.rows) {
			return
		}
		if s.rows[i].selectable() {
			s.cursor = i
			return
		}
	}
}

func (s *sidebar) InputHandler() func(*tcell.EventKey, func(tview.Primitive)) {
	return s.WrapInputHandler(func(ev *tcell.EventKey, _ func(tview.Primitive)) {
		_, _, _, h := s.GetInnerRect()
		switch ev.Key() {
		case tcell.KeyUp:
			s.move(-1)
		case tcell.KeyDown:
			s.move(1)
		case tcell.KeyPgUp:
			s.move(-h / 2)
			s.cursor = s.firstSelectable(max(0, s.cursor), 1)
		case tcell.KeyPgDn:
			s.move(h / 2)
		case tcell.KeyHome:
			s.cursor = s.firstSelectable(0, 1)
		case tcell.KeyEnd:
			s.cursor = s.firstSelectable(len(s.rows)-1, -1)
		case tcell.KeyEnter, tcell.KeyRight:
			if r := s.rows[s.cursor]; r.kind == sideGuild && s.expanded(r.id) && ev.Key() == tcell.KeyRight {
				s.move(1)
				return
			}
			s.activate(s.cursor)
		case tcell.KeyLeft:
			s.foldUp()
		case tcell.KeyEscape:
			s.a.focusComposer()
		case tcell.KeyRune:
			switch r := ev.Rune(); r {
			case 'j':
				s.move(1)
			case 'k':
				s.move(-1)
			case 'l', ' ':
				s.activate(s.cursor)
			case 'h':
				s.foldUp()
			case 'g':
				s.cursor = s.firstSelectable(0, 1)
			case 'G':
				s.cursor = s.firstSelectable(len(s.rows)-1, -1)
			case '?':
				s.a.showHelp()
			default:
				// Type to search: any other letter opens the switcher.
				s.a.openSwitcher(string(r))
			}
		}
	})
}

// foldUp collapses the server under the cursor, or moves to its header.
func (s *sidebar) foldUp() {
	if s.cursor >= len(s.rows) {
		return
	}
	r := s.rows[s.cursor]
	if r.kind == sideGuild {
		if s.expanded(r.id) {
			s.collapsed[r.id] = true
			s.refresh()
			s.cursor = slicesIndex(s.rows, r.id, sideGuild)
		}
		return
	}
	if r.kind == sideChannel {
		s.cursor = slicesIndex(s.rows, r.ch.GuildID, sideGuild)
	}
}

func slicesIndex(rows []sideRow, id Snowflake, kind sideKind) int {
	for i, r := range rows {
		if r.id == id && r.kind == kind {
			return i
		}
	}
	return 0
}

func (s *sidebar) MouseHandler() func(tview.MouseAction, *tcell.EventMouse, func(tview.Primitive)) (bool, tview.Primitive) {
	return s.WrapMouseHandler(func(action tview.MouseAction, ev *tcell.EventMouse, setFocus func(tview.Primitive)) (bool, tview.Primitive) {
		x, y := ev.Position()
		if !s.InRect(x, y) {
			return false, nil
		}
		_, ry, _, h := s.GetInnerRect()
		switch action {
		case tview.MouseScrollUp:
			s.offset = max(0, s.offset-3)
			return true, nil
		case tview.MouseScrollDown:
			s.offset = max(0, min(len(s.rows)-h, s.offset+3))
			return true, nil
		case tview.MouseLeftClick:
			i := s.offset + y - ry
			if i >= 0 && i < len(s.rows) && s.rows[i].selectable() {
				s.cursor = i
				s.activate(i)
			}
			return true, nil
		}
		return false, nil
	})
}

func (s *sidebar) Draw(scr tcell.Screen) {
	a, th := s.a, s.a.th
	x, y, w, h := s.GetRect()
	if w <= 0 {
		return
	}
	base := th.base()
	for row := 0; row < h; row++ {
		fill(scr, x, y+row, w, base)
		scr.SetContent(x+w-1, y+row, '│', nil, th.faint())
	}
	cw := w - 3 // content width, leaving a gap before the rule
	focused := s.HasFocus()

	// Keep the interesting row in view: the cursor while navigating, the
	// open channel otherwise.
	anchor := s.cursor
	if !focused {
		for i, r := range s.rows {
			if r.id == a.current && (r.kind == sideChannel || r.kind == sideDM) {
				anchor = i
			}
		}
	}
	if anchor < s.offset {
		s.offset = anchor
	}
	if anchor >= s.offset+h {
		s.offset = anchor - h + 1
	}
	s.offset = max(0, min(s.offset, len(s.rows)-h))

	for row := 0; row < h; row++ {
		i := s.offset + row
		if i >= len(s.rows) {
			break
		}
		r := s.rows[i]
		ry := y + row
		isCurrent := r.id != 0 && r.id == a.current && (r.kind == sideChannel || r.kind == sideDM)
		bg := base
		switch {
		case focused && i == s.cursor:
			bg = th.cursor(base)
		case isCurrent:
			bg = th.surface(base)
		}
		if bg != base {
			fill(scr, x, ry, cw+1, bg)
		}
		if isCurrent {
			scr.SetContent(x, ry, '▌', nil, bg.Foreground(th.Accent))
		}
		s.drawRow(scr, x, ry, cw, r, bg, isCurrent)
	}

	// Scroll hints when rows are hidden.
	if s.offset > 0 {
		scr.SetContent(x+w-1, y, '▲', nil, th.muted())
	}
	if s.offset+h < len(s.rows) {
		scr.SetContent(x+w-1, y+h-1, '▼', nil, th.muted())
	}
}

func (s *sidebar) drawRow(scr tcell.Screen, x, y, cw int, r sideRow, bg tcell.Style, current bool) {
	a, th := s.a, s.a.th
	fg := func(c tcell.Color) tcell.Style { return bg.Foreground(c) }

	// Right-hand badge: mention pill, unread dot, or draft marker.
	var badge line
	unread, mentions, muted := r.ch.Unread, r.ch.Mentions, r.ch.Muted
	if r.kind == sideGuild {
		unread, mentions, muted = r.guild.Unread && !s.expanded(r.id), r.guild.Mentions, r.guild.Muted
		if s.expanded(r.id) {
			mentions = 0 // shown on the channels themselves
		}
	}
	switch {
	case mentions > 0:
		badge = line{{text: fmt.Sprintf(" %d ", mentions), style: th.badge()}}
	case r.kind == sideGuild && unread:
		badge = line{{text: "•", style: fg(th.Text)}}
	case (r.kind == sideChannel || r.kind == sideDM) && a.drafts[r.id] != "" && !current:
		badge = line{{text: "✎", style: fg(th.Muted)}}
	}
	bw := lineWidth(badge)
	if bw > 0 {
		drawLine(scr, x+1+cw-bw, y, bw, badge, true, nil)
		bw++
	}

	nameStyle := fg(th.Subtle)
	switch {
	case current:
		nameStyle = fg(th.Text).Bold(true)
	case muted && mentions == 0:
		nameStyle = fg(th.Faint)
	case unread || mentions > 0:
		nameStyle = fg(th.Text).Bold(true)
	}
	if th.mono && (unread || mentions > 0) {
		nameStyle = nameStyle.Bold(true)
	}

	// Unread dot in the left margin, Discord-style.
	if !current && (unread || mentions > 0) && r.kind != sideGuild {
		scr.SetContent(x, y, '•', nil, fg(th.Text))
	}

	col := x + 1
	avail := cw - bw
	put := func(t string, st tcell.Style) {
		n := drawText(scr, col, y, avail, t, st)
		col += n
		avail -= n
	}
	switch r.kind {
	case sideHeader:
		put(r.text, fg(th.Muted).Bold(true))
	case sideSpacer:
	case sideMore:
		put("  "+r.text, fg(th.Muted).Italic(true))
		put(" · Ctrl+K", fg(th.Faint))
	case sideDM:
		glyph, gst := "○", fg(th.Faint)
		if r.ch.Type == discord.ChannelGroupDM {
			glyph, gst = "◇", fg(th.Muted)
		} else if u, ok := a.st.DMRecipient(r.id); ok {
			switch a.st.Presence(u.ID) {
			case "online":
				glyph, gst = "●", fg(th.OK)
			case "idle":
				glyph, gst = "◐", fg(th.Warn)
			case "dnd":
				glyph, gst = "⊖", fg(th.Error)
			}
		}
		put(" "+glyph+" ", gst)
		put(truncate(r.ch.Name, avail), nameStyle)
	case sideGuild:
		arrow := "▸ "
		if s.expanded(r.id) {
			arrow = "▾ "
		}
		put(arrow, fg(th.Muted))
		st := fg(th.Text).Bold(unread || mentions > 0)
		if muted && mentions == 0 {
			st = fg(th.Faint)
		} else if !unread && mentions == 0 {
			st = fg(th.BarText)
		}
		put(truncate(r.guild.Name, avail), st)
	case sideCategory:
		put("  "+truncate(r.text, avail-2), fg(th.Faint))
	case sideChannel:
		indent := "  "
		glyph := channelGlyph(r.ch.Type)
		if r.ch.Depth > 0 {
			indent = "    "
		}
		gst := fg(th.Faint)
		if current {
			gst = fg(th.Accent)
		}
		put(indent+glyph+" ", gst)
		put(truncate(r.ch.Name, avail), nameStyle)
	}
}
