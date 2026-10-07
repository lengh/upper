package ui

import (
	"fmt"
	"slices"
	"strings"
	"unicode"

	"github.com/gdamore/tcell/v2"
	"github.com/lengh/upper/internal/state"
	"github.com/rivo/tview"
)

// dimBackground fades everything already drawn so an overlay is the one
// thing in focus.
func dimBackground(scr tcell.Screen, th *Theme) {
	w, h := scr.Size()
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			r, comb, st, _ := scr.GetContent(x, y)
			if th.mono {
				st = st.Dim(true)
			} else {
				st = st.Foreground(th.Faint).Background(th.Bg).Bold(false).Underline(false)
			}
			scr.SetContent(x, y, r, comb, st)
		}
	}
}

// panel draws a floating rounded box and returns its inner rectangle.
func panel(scr tcell.Screen, th *Theme, w, h int, title, footer string) (ix, iy, iw, ih int) {
	sw, sh := scr.Size()
	w, h = min(w, sw-2), min(h, sh-2)
	x, y := (sw-w)/2, max(1, (sh-h)/3)
	bg := th.base()
	if !th.mono {
		bg = tcell.StyleDefault.Background(th.Bar).Foreground(th.Text)
	}
	border := bg.Foreground(th.Accent)
	for r := 0; r < h; r++ {
		fill(scr, x, y+r, w, bg)
		scr.SetContent(x, y+r, '│', nil, border)
		scr.SetContent(x+w-1, y+r, '│', nil, border)
	}
	for c := 1; c < w-1; c++ {
		scr.SetContent(x+c, y, '─', nil, border)
		scr.SetContent(x+c, y+h-1, '─', nil, border)
	}
	scr.SetContent(x, y, '╭', nil, border)
	scr.SetContent(x+w-1, y, '╮', nil, border)
	scr.SetContent(x, y+h-1, '╰', nil, border)
	scr.SetContent(x+w-1, y+h-1, '╯', nil, border)
	if title != "" {
		drawText(scr, x+2, y, w-4, " "+title+" ", bg.Foreground(th.Text).Bold(true))
	}
	if footer != "" {
		f := " " + footer + " "
		drawText(scr, x+w-2-textWidth(f), y+h-1, w-4, f, bg.Foreground(th.Muted))
	}
	return x + 2, y + 1, w - 4, h - 2
}

type match struct {
	c     state.ChannelInfo
	score int
	pos   []int // matched rune positions in the name
}

// switcher is the Ctrl+K palette: type a few letters of any channel, DM or
// server and press Enter. Matched letters are highlighted so you can see
// why something ranked where it did.
type switcher struct {
	*tview.Box
	a       *App
	query   []rune
	all     []state.ChannelInfo
	results []match
	index   int
	offset  int
}

func (a *App) openSwitcher(initial string) {
	s := &switcher{Box: tview.NewBox(), a: a, query: []rune(initial), all: a.st.AllChannels()}
	s.update()
	a.pages.AddPage("switcher", s, true, true)
	a.tv.SetFocus(s)
}

func (s *switcher) close() {
	s.a.pages.RemovePage("switcher")
	s.a.focusComposer()
}

func (s *switcher) update() {
	s.results = rankChannels(s.all, string(s.query), s.a.current)
	s.index, s.offset = 0, 0
}

func (s *switcher) Draw(scr tcell.Screen) {
	a, th := s.a, s.a.th
	dimBackground(scr, th)
	ph := max(8, min(22, len(s.results)+5))
	x, y, w, h := panel(scr, th, 74, ph, "Jump to", "↑↓ move · Enter open · Esc close")
	bg := tcell.StyleDefault.Background(th.Bar).Foreground(th.Text)
	if th.mono {
		bg = th.base()
	}

	// Query line with a block cursor.
	used := drawText(scr, x, y, w, "❯ ", bg.Foreground(th.Accent).Bold(true))
	used += drawText(scr, x+used, y, w-used, string(s.query), bg.Bold(true))
	scr.SetContent(x+used, y, ' ', nil, bg.Reverse(true))
	if len(s.query) == 0 {
		drawText(scr, x+used+2, y, w-used-2, "channel, DM or server name", bg.Foreground(th.Muted).Italic(true))
	}
	for c := 0; c < w; c++ {
		scr.SetContent(x+c, y+1, '─', nil, bg.Foreground(th.Faint))
	}

	rows := h - 2
	if len(s.results) == 0 {
		drawText(scr, x+2, y+3, w, "nothing matches “"+string(s.query)+"”", bg.Foreground(th.Muted))
		return
	}
	if s.index < s.offset {
		s.offset = s.index
	}
	if s.index >= s.offset+rows {
		s.offset = s.index - rows + 1
	}
	for r := 0; r < rows && s.offset+r < len(s.results); r++ {
		i := s.offset + r
		s.drawResult(scr, x, y+2+r, w, s.results[i], i == s.index, bg)
	}
	if len(s.results) > rows {
		drawRight(scr, x, y+1, w, fmt.Sprintf(" %d/%d ", s.index+1, len(s.results)), bg.Foreground(th.Muted))
	}
	_ = a
}

func (s *switcher) drawResult(scr tcell.Screen, x, y, w int, m match, sel bool, bg tcell.Style) {
	th := s.a.th
	if sel {
		bg = th.surface(bg)
		fill(scr, x-1, y, w+2, bg)
		scr.SetContent(x-1, y, '▌', nil, bg.Foreground(th.Accent))
	}
	c := m.c
	right := c.Category
	if c.Type.IsPrivate() {
		right = "direct message"
	}
	var badge line
	if c.Mentions > 0 {
		badge = line{{text: fmt.Sprintf(" %d ", c.Mentions), style: th.badge()}}
	} else if c.Unread {
		badge = line{{text: "•", style: bg.Foreground(th.Text)}}
	}
	bw := lineWidth(badge)
	if bw > 0 {
		drawLine(scr, x+w-bw, y, bw, badge, true, nil)
		bw++
	}
	rw := min(textWidth(right), w/3)
	drawRight(scr, x, y, w-bw, right, bg.Foreground(th.Muted))

	col := x
	col += drawText(scr, col, y, 2, channelGlyph(c.Type)+" ", bg.Foreground(th.Muted))
	nameW := w - bw - rw - 4
	nameSt := bg.Foreground(th.Subtle)
	if c.Unread || c.Mentions > 0 || sel {
		nameSt = bg.Foreground(th.Text).Bold(true)
	}
	hit := bg.Foreground(th.Accent).Bold(true).Underline(th.mono)
	runes := []rune(truncate(c.Name, nameW))
	for i, r := range runes {
		st := nameSt
		if slices.Contains(m.pos, i) {
			st = hit
		}
		col += drawText(scr, col, y, nameW, string(r), st)
	}
}

func (s *switcher) InputHandler() func(*tcell.EventKey, func(tview.Primitive)) {
	return s.WrapInputHandler(func(ev *tcell.EventKey, _ func(tview.Primitive)) {
		n := len(s.results)
		switch ev.Key() {
		case tcell.KeyEscape, tcell.KeyCtrlK:
			s.close()
		case tcell.KeyEnter:
			if s.index < n {
				id := s.results[s.index].c.ID
				s.a.pages.RemovePage("switcher")
				s.a.open(id)
			}
		case tcell.KeyUp, tcell.KeyCtrlP, tcell.KeyBacktab:
			if n > 0 {
				s.index = (s.index - 1 + n) % n
			}
		case tcell.KeyDown, tcell.KeyCtrlN, tcell.KeyTab:
			if n > 0 {
				s.index = (s.index + 1) % n
			}
		case tcell.KeyPgUp:
			s.index = max(0, s.index-10)
		case tcell.KeyPgDn:
			s.index = max(0, min(n-1, s.index+10))
		case tcell.KeyBackspace, tcell.KeyBackspace2:
			if len(s.query) > 0 {
				s.query = s.query[:len(s.query)-1]
				s.update()
			} else {
				s.close()
			}
		case tcell.KeyCtrlU:
			s.query = nil
			s.update()
		case tcell.KeyCtrlW:
			q := strings.TrimRight(string(s.query), " ")
			if i := strings.LastIndex(q, " "); i >= 0 {
				s.query = []rune(q[:i+1])
			} else {
				s.query = nil
			}
			s.update()
		case tcell.KeyRune:
			s.query = append(s.query, ev.Rune())
			s.update()
		}
	})
}

func (s *switcher) MouseHandler() func(tview.MouseAction, *tcell.EventMouse, func(tview.Primitive)) (bool, tview.Primitive) {
	return s.WrapMouseHandler(func(action tview.MouseAction, ev *tcell.EventMouse, _ func(tview.Primitive)) (bool, tview.Primitive) {
		switch action {
		case tview.MouseScrollUp:
			s.index = max(0, s.index-1)
		case tview.MouseScrollDown:
			s.index = max(0, min(len(s.results)-1, s.index+1))
		case tview.MouseLeftClick:
			// Clicks outside the panel close it.
			s.close()
		}
		return true, nil
	})
}

const switcherLimit = 200

// rankChannels scores channels against a fuzzy query. With an empty query it
// lists mentions, then unread, then everything by recent activity.
func rankChannels(all []state.ChannelInfo, q string, current Snowflake) []match {
	q = strings.ToLower(strings.TrimLeft(q, "#@◇ "))
	var res []match
	for _, c := range all {
		if c.ID == current && q == "" {
			continue
		}
		m := match{c: c}
		if q != "" {
			var ok bool
			m.score, m.pos, ok = fuzzyMatch(strings.ToLower(c.Name), q)
			if !ok {
				// "srv gen" style: match against "server name".
				gs, _, gok := fuzzyMatch(strings.ToLower(c.Category+" "+c.Name), q)
				if !gok {
					continue
				}
				m.score = gs - 25
			}
		}
		switch {
		case c.Mentions > 0:
			m.score += 40
		case c.Unread:
			m.score += 15
		}
		if c.Muted {
			m.score -= 10
		}
		res = append(res, m)
	}
	slices.SortStableFunc(res, func(x, y match) int {
		if x.score != y.score {
			return y.score - x.score
		}
		switch {
		case x.c.Last > y.c.Last:
			return -1
		case x.c.Last < y.c.Last:
			return 1
		}
		return 0
	})
	if len(res) > switcherLimit {
		res = res[:switcherLimit]
	}
	return res
}

// fuzzyMatch matches q as a subsequence of s. Contiguous runs, word starts
// and prefixes score higher. It returns the matched rune positions.
func fuzzyMatch(s, q string) (int, []int, bool) {
	if q == "" {
		return 0, nil, true
	}
	sr, qr := []rune(s), []rune(q)
	if i := strings.Index(s, q); i >= 0 {
		start := len([]rune(s[:i]))
		pos := make([]int, len(qr))
		for k := range qr {
			pos[k] = start + k
		}
		score := 100 - min(start, 50)
		if start == 0 {
			score += 50
		}
		if len(sr) == len(qr) {
			score += 50
		}
		return score, pos, true
	}
	score, qi, run := 0, 0, 0
	var pos []int
	for i := 0; i < len(sr) && qi < len(qr); i++ {
		if sr[i] != qr[qi] {
			run = 0
			continue
		}
		run++
		score += run * 2
		if i == 0 || !unicode.IsLetter(sr[i-1]) && !unicode.IsDigit(sr[i-1]) {
			score += 6
		}
		pos = append(pos, i)
		qi++
	}
	if qi < len(qr) {
		return 0, nil, false
	}
	return score - len(sr)/8, pos, true
}
