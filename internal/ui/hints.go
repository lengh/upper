package ui

import (
	"fmt"
	"strings"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

// Jump labels, after flash.nvim and Vimium: every message on screen gets a
// one-letter label, typed to act on it. Replying to the fourth message up
// costs Alt+R and one letter instead of stepping through messages, about
// two keystrokes whatever the distance (by the Keystroke-Level Model:
// ~0.4s of keying plus one glance, against ~0.2s and a glance per step).
//
// Labels come from the home row outwards and the nearest message to the
// composer gets the easiest key, so the common case is "a" or "s".
const hintKeys = "asdfghjklqwertyuiopzxcvbnm"

type hintAction int

const (
	hintSelect hintAction = iota
	hintReply
	hintReact
	hintOpen
	hintCopy
)

var hintVerbs = map[hintAction]string{
	hintSelect: "select", hintReply: "reply to", hintReact: "react to",
	hintOpen: "open the link in", hintCopy: "copy",
}

type hintState struct {
	action hintAction
	labels map[rune]int // label → message index
	byMsg  map[int]rune
	back   tview.Primitive
}

// startHints labels the messages visible on screen that the action applies
// to. With exactly one candidate it acts at once.
func (a *App) startHints(action hintAction) {
	v := a.view
	if v.ch == 0 || len(v.rows) == 0 {
		return
	}
	h := v.height()
	first := max(0, len(v.rows)-h-v.scroll)
	last := min(len(v.rows), first+h) - 1

	hs := &hintState{action: action, labels: map[rune]int{}, byMsg: map[int]rune{}, back: a.tv.GetFocus()}
	keys := []rune(hintKeys)
	for k := last; k >= first && len(hs.labels) < len(keys); k-- {
		r := v.rows[k]
		if r.kind != rowMsg || r.msg < 0 {
			continue
		}
		if _, done := hs.byMsg[r.msg]; done {
			continue
		}
		m := &v.msgs[r.msg]
		ok := !m.Pending
		switch action {
		case hintReply, hintReact:
			ok = ok && !isSystem(m)
		case hintOpen:
			ok = ok && len(links(m)) > 0
		case hintCopy:
			ok = ok && m.Content != ""
		}
		if !ok {
			continue
		}
		key := keys[len(hs.labels)]
		hs.labels[key] = r.msg
		hs.byMsg[r.msg] = key
	}
	switch len(hs.labels) {
	case 0:
		a.flash("nothing on screen to " + strings.TrimSuffix(hintVerbs[action], " to"))
		return
	case 1:
		for _, i := range hs.labels {
			a.runHint(action, i)
		}
		return
	}
	v.hint = hs
	a.tv.SetFocus(v)
}

func (a *App) runHint(action hintAction, i int) {
	v := a.view
	if i < 0 || i >= len(v.msgs) {
		return
	}
	m := v.msgs[i]
	switch action {
	case hintSelect:
		v.selected = msgKey(&m)
		a.tv.SetFocus(v)
	case hintReply:
		a.startReply(m)
	case hintReact:
		a.startReact(m)
	case hintOpen:
		a.openLink(m)
		a.focusComposer()
	case hintCopy:
		if err := copyToClipboard(m.Content); err != nil {
			a.flashErr(err)
		} else {
			a.flashAt(levelOK, "copied to clipboard")
		}
		a.focusComposer()
	}
}

// hintKey handles a key while labels are shown. Any key that isn't a label
// cancels, so a mistaken Alt+R never traps you.
func (v *msgView) hintKey(ev *tcell.EventKey) {
	hs := v.hint
	v.hint = nil
	if ev.Key() == tcell.KeyRune {
		if i, ok := hs.labels[ev.Rune()]; ok {
			v.a.runHint(hs.action, i)
			return
		}
	}
	if hs.back != nil {
		v.a.tv.SetFocus(hs.back)
	} else {
		v.a.focusComposer()
	}
}

// drawHintBadges puts each label in the time column of its message's name
// row (or its first visible row if the name is scrolled off).
func (v *msgView) drawHintBadges(scr tcell.Screen, x, top, first, h int) {
	th := v.a.th
	st := tcell.StyleDefault.Background(th.Accent).Foreground(th.BadgeFg).Bold(true)
	if th.mono {
		st = tcell.StyleDefault.Reverse(true).Bold(true)
	}
	at := map[int]int{} // message → screen row
	for r := 0; r < h && first+r < len(v.rows); r++ {
		row := v.rows[first+r]
		if _, ok := v.hint.byMsg[row.msg]; !ok || row.kind != rowMsg {
			continue
		}
		if _, seen := at[row.msg]; !seen || row.head {
			at[row.msg] = r
		}
	}
	w := max(3, v.timeW)
	for msg, r := range at {
		fill(scr, x+1, top+r, w, th.base())
		drawText(scr, x+1, top+r, 3, " "+string(v.hint.byMsg[msg])+" ", st)
	}
}

// labelOf is nil-safe so drawing code can ask unconditionally.
func (hs *hintState) labelOf(msg int) (rune, bool) {
	if hs == nil {
		return 0, false
	}
	r, ok := hs.byMsg[msg]
	return r, ok
}

func (v *msgView) hintStatus() line {
	th := v.a.th
	bar := th.bar()
	return line{
		{text: fmt.Sprintf("%s which message? ", hintVerbs[v.hint.action]), style: bar.Foreground(th.Accent).Bold(true)},
		{text: "type its letter · any other key cancels", style: bar.Foreground(th.Muted)},
	}
}

// ---- Search --------------------------------------------------------------

// setQuery highlights a search term in the loaded messages and selects the
// newest match.
func (v *msgView) setQuery(q string) {
	v.query = strings.ToLower(strings.TrimSpace(q))
	v.matches = v.matches[:0]
	if v.query == "" {
		v.selected = ""
		return
	}
	for i := range v.msgs {
		m := &v.msgs[i]
		au := v.a.st.Author(m.GuildID, m.Author).Name
		if strings.Contains(strings.ToLower(m.Content), v.query) || strings.Contains(strings.ToLower(au), v.query) {
			v.matches = append(v.matches, i)
		}
	}
	v.match = len(v.matches) - 1
	v.showMatch()
}

func (v *msgView) showMatch() {
	if v.match < 0 || v.match >= len(v.matches) {
		v.selected = ""
		return
	}
	v.selectIndex(v.matches[v.match])
}

// stepMatch moves to an older (-1) or newer (+1) match, wrapping around.
func (v *msgView) stepMatch(d int) {
	if n := len(v.matches); n > 0 {
		v.match = (v.match + d + n) % n
		v.showMatch()
	}
}

func (v *msgView) searchStatus() line {
	th := v.a.th
	bar := th.bar()
	key := func(k string) span { return span{text: k, style: bar.Foreground(th.Text).Bold(true)} }
	txt := func(t string) span { return span{text: t, style: bar.Foreground(th.Muted)} }
	switch {
	case v.query == "":
		return line{txt("search the loaded messages by text or name · "), key("Esc"), txt(" close")}
	case len(v.matches) == 0:
		return line{{text: "no matches", style: bar.Foreground(th.Warn)}, txt(" · older messages load as you scroll up")}
	}
	return line{{text: fmt.Sprintf("%d of %d", v.match+1, len(v.matches)), style: bar.Foreground(th.Text).Bold(true)},
		txt("  "), key("↑↓"), txt(" step  "), key("Enter"), txt(" act on it  "), key("Esc"), txt(" close")}
}

// highlightLine marks occurrences of q (lower-case) within a line.
func highlightLine(l line, q string, th *Theme) line {
	if q == "" {
		return l
	}
	var out line
	for _, sp := range l {
		lower := strings.ToLower(sp.text)
		if len(lower) != len(sp.text) || !strings.Contains(lower, q) {
			out = append(out, sp)
			continue
		}
		rest, low := sp.text, lower
		for {
			i := strings.Index(low, q)
			if i < 0 {
				break
			}
			if i > 0 {
				out = append(out, span{text: rest[:i], style: sp.style, spoiler: sp.spoiler})
			}
			hl := sp.style.Background(th.Warn).Foreground(th.BadgeFg)
			if th.mono {
				hl = sp.style.Reverse(true)
			}
			out = append(out, span{text: rest[i : i+len(q)], style: hl})
			rest, low = rest[i+len(q):], low[i+len(q):]
		}
		if rest != "" {
			out = append(out, span{text: rest, style: sp.style, spoiler: sp.spoiler, fill: sp.fill})
		}
	}
	return out
}

func (a *App) startSearch() {
	if a.current == 0 {
		return
	}
	if a.mode == modeNormal {
		if t := a.input.GetText(); t != "" {
			a.drafts[a.current] = t
		}
	}
	a.mode, a.target = modeSearch, nil
	a.input.SetText("", false)
	a.updatePlaceholder()
	a.focusComposer()
}

// endSearch leaves search; with keep, the current match stays selected
// and focus moves to it so it can be acted on.
func (a *App) endSearch(keep bool) {
	v := a.view
	sel := v.selected
	v.query, v.matches = "", nil
	a.mode = modeNormal
	a.input.SetText(a.drafts[a.current], true)
	delete(a.drafts, a.current)
	a.updatePlaceholder()
	if keep && sel != "" {
		v.selected = sel
		a.tv.SetFocus(v)
		return
	}
	v.scrollBottom()
}
