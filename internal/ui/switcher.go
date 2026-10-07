package ui

import (
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/gdamore/tcell/v2"
	"github.com/lengh/upper/internal/discord"
	"github.com/lengh/upper/internal/state"
	"github.com/rivo/tview"
)

const switcherLimit = 60

// openSwitcher shows a fuzzy finder over every readable channel and DM.
func (a *App) openSwitcher() {
	all := a.st.AllChannels()
	field := tview.NewInputField().SetLabel("› ").SetFieldBackgroundColor(tcell.ColorDefault)
	list := tview.NewList().ShowSecondaryText(false).SetHighlightFullLine(true)
	list.SetSelectedBackgroundColor(color(a.th.selected))

	var results []state.ChannelInfo
	update := func(q string) {
		results = rankChannels(all, q, a.current)
		list.Clear()
		for _, c := range results {
			list.AddItem(a.switcherLabel(c), "", 0, nil)
		}
	}
	update("")

	close := func() {
		a.pages.RemovePage("switcher")
		a.focus(a.input)
	}
	choose := func() {
		i := list.GetCurrentItem()
		if i < 0 || i >= len(results) {
			return
		}
		id := results[i].ID
		a.pages.RemovePage("switcher")
		a.open(id)
	}

	field.SetChangedFunc(update)
	field.SetInputCapture(func(ev *tcell.EventKey) *tcell.EventKey {
		switch ev.Key() {
		case tcell.KeyEscape, tcell.KeyCtrlK:
			close()
			return nil
		case tcell.KeyEnter:
			choose()
			return nil
		case tcell.KeyUp, tcell.KeyCtrlP, tcell.KeyBacktab:
			if n := list.GetItemCount(); n > 0 {
				list.SetCurrentItem((list.GetCurrentItem() + n - 1) % n)
			}
			return nil
		case tcell.KeyDown, tcell.KeyCtrlN, tcell.KeyTab:
			if n := list.GetItemCount(); n > 0 {
				list.SetCurrentItem((list.GetCurrentItem() + 1) % n)
			}
			return nil
		}
		return ev
	})
	list.SetSelectedFunc(func(int, string, string, rune) { choose() })

	box := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(field, 1, 0, true).
		AddItem(list, 0, 1, false)
	box.SetBorder(true).SetTitle(" jump to channel or DM ").SetBorderColor(color(a.th.border))
	box.SetBackgroundColor(tcell.ColorDefault)

	a.pages.AddPage("switcher", centered(box, 70, 20), true, true)
	a.tv.SetFocus(field)
}

func (a *App) switcherLabel(c state.ChannelInfo) string {
	th := a.th
	var s string
	switch c.Type {
	case discord.ChannelDM:
		s = "@" + tview.Escape(c.Name)
	case discord.ChannelGroupDM:
		s = "+" + tview.Escape(c.Name)
	default:
		s = "#" + tview.Escape(c.Name) + "  [" + th.muted + "]" + tview.Escape(c.Category) + "[-]"
	}
	if c.Unread || c.Mentions > 0 {
		s = "[::b]" + s + "[::B]"
	}
	if c.Mentions > 0 {
		s += " [" + th.mention + "](" + strconv.Itoa(c.Mentions) + ")[-]"
	}
	return s
}

// rankChannels scores channels against a fuzzy query. With an empty query it
// lists unread channels first, then recent activity.
func rankChannels(all []state.ChannelInfo, q string, current Snowflake) []state.ChannelInfo {
	q = strings.ToLower(strings.TrimLeft(q, "#@+ "))
	type scored struct {
		c state.ChannelInfo
		s int
	}
	var res []scored
	for _, c := range all {
		if c.ID == current && q == "" {
			continue
		}
		s := 0
		if q != "" {
			var ok bool
			s, ok = fuzzyScore(strings.ToLower(c.Name), q)
			if !ok {
				gs, gok := fuzzyScore(strings.ToLower(c.Category+" "+c.Name), q)
				if !gok {
					continue
				}
				s = gs - 20
			}
		}
		if c.Mentions > 0 {
			s += 40
		} else if c.Unread {
			s += 15
		}
		res = append(res, scored{c, s})
	}
	slices.SortStableFunc(res, func(x, y scored) int {
		if x.s != y.s {
			return y.s - x.s
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
	out := make([]state.ChannelInfo, len(res))
	for i, r := range res {
		out[i] = r.c
	}
	return out
}

// fuzzyScore matches q as a subsequence of s. Contiguous runs, word starts
// and prefix matches score higher.
func fuzzyScore(s, q string) (int, bool) {
	if q == "" {
		return 0, true
	}
	if i := strings.Index(s, q); i >= 0 {
		score := 100 - min(i, 50)
		if i == 0 {
			score += 50
		}
		if len(s) == len(q) {
			score += 50
		}
		return score, true
	}
	sr, qr := []rune(s), []rune(q)
	score, qi, run := 0, 0, 0
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
		qi++
	}
	if qi < len(qr) {
		return 0, false
	}
	return score - len(sr)/8, true
}
