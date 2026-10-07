package ui

import (
	"strings"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/uniseg"
)

// span is a run of text in one style. Rich text throughout the UI is a slice
// of spans; drawing and wrapping work on grapheme clusters so emoji, CJK and
// combining marks take the cells the terminal will give them.
type span struct {
	text  string
	style tcell.Style
	// spoiler text is hidden until its message is selected.
	spoiler bool
	// fill extends the span's background to the end of the line (code
	// blocks).
	fill bool
}

type line []span

func textWidth(s string) int { return uniseg.StringWidth(s) }

func lineWidth(l line) int {
	w := 0
	for _, s := range l {
		w += textWidth(s.text)
	}
	return w
}

// sanitize removes control characters that would corrupt the terminal and
// expands tabs.
func sanitize(s string) string {
	if !strings.ContainsFunc(s, func(r rune) bool { return r < 0x20 && r != '\n' || r == 0x7f }) {
		return s
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '\t':
			b.WriteString("    ")
		case r == '\n':
			b.WriteRune(r)
		case r < 0x20 || r == 0x7f:
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// truncate shortens s to at most w cells, ending in "…" when cut.
func truncate(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if textWidth(s) <= w {
		return s
	}
	var b strings.Builder
	used, state := 0, -1
	rest := s
	for len(rest) > 0 {
		var c string
		var cw int
		c, rest, cw, state = uniseg.FirstGraphemeClusterInString(rest, state)
		if used+cw > w-1 {
			break
		}
		b.WriteString(c)
		used += cw
	}
	return b.String() + "…"
}

// piece kinds used by wrap.
const (
	pWord = iota
	pSpace
	pBreak
)

type piece struct {
	text string
	sp   span // template (style, flags)
	kind int
}

// wrap breaks rich text into lines of at most width cells, breaking at
// spaces where possible and inside words only when a word is wider than the
// line. Explicit newlines are kept.
func wrap(spans []span, width int) []line {
	width = max(width, 1)
	var pieces []piece
	for _, sp := range spans {
		t := sp.text
		for len(t) > 0 {
			i := strings.IndexAny(t, " \n")
			switch {
			case i < 0:
				pieces = append(pieces, piece{t, sp, pWord})
				t = ""
			case i > 0:
				pieces = append(pieces, piece{t[:i], sp, pWord})
				t = t[i:]
			case t[0] == '\n':
				pieces = append(pieces, piece{"\n", sp, pBreak})
				t = t[1:]
			default:
				j := 0
				for j < len(t) && t[j] == ' ' {
					j++
				}
				pieces = append(pieces, piece{t[:j], sp, pSpace})
				t = t[j:]
			}
		}
	}

	var out []line
	var cur line
	curW := 0
	var pending []piece // spaces waiting to see whether the next word fits
	pendingW := 0

	push := func(text string, sp span) {
		if text == "" {
			return
		}
		if n := len(cur); n > 0 && cur[n-1].style == sp.style && cur[n-1].spoiler == sp.spoiler && cur[n-1].fill == sp.fill {
			cur[n-1].text += text
		} else {
			s := sp
			s.text = text
			cur = append(cur, s)
		}
	}
	flush := func() {
		out = append(out, cur)
		cur, curW = nil, 0
		pending, pendingW = nil, 0
	}

	for i := 0; i < len(pieces); {
		p := pieces[i]
		switch p.kind {
		case pBreak:
			// A trailing fill span (code background) must survive an
			// empty line, so keep a zero-width marker.
			if len(cur) == 0 && p.sp.fill {
				cur = line{span{style: p.sp.style, fill: true}}
			}
			flush()
			i++
			continue
		case pSpace:
			pending = append(pending, p)
			pendingW += textWidth(p.text)
			i++
			continue
		}
		// Gather the whole word, which may span several styles.
		j := i
		ww := 0
		for j < len(pieces) && pieces[j].kind == pWord {
			ww += textWidth(pieces[j].text)
			j++
		}
		switch {
		case curW+pendingW+ww <= width:
			for _, sp := range pending {
				push(sp.text, sp.sp)
			}
			curW += pendingW
			pending, pendingW = nil, 0
		case ww <= width:
			flush()
		default:
			// Too long for any line: fill what's left, then hard-break.
			if curW+pendingW < width {
				for _, sp := range pending {
					push(sp.text, sp.sp)
				}
				curW += pendingW
			} else if curW > 0 {
				flush()
			}
			pending, pendingW = nil, 0
		}
		for k := i; k < j; k++ {
			rest, state := pieces[k].text, -1
			for len(rest) > 0 {
				var c string
				var cw int
				c, rest, cw, state = uniseg.FirstGraphemeClusterInString(rest, state)
				if curW+cw > width && curW > 0 {
					flush()
				}
				push(c, pieces[k].sp)
				curW += cw
			}
		}
		i = j
	}
	if len(cur) > 0 || len(out) == 0 {
		out = append(out, cur)
	}
	return out
}

// drawText draws s at (x, y), clipped to maxW cells, and returns the width
// used.
func drawText(scr tcell.Screen, x, y, maxW int, s string, st tcell.Style) int {
	used, state := 0, -1
	for len(s) > 0 && used < maxW {
		var c string
		var w int
		c, s, w, state = uniseg.FirstGraphemeClusterInString(s, state)
		if w == 0 {
			continue
		}
		if used+w > maxW {
			break
		}
		r := []rune(c)
		scr.SetContent(x+used, y, r[0], r[1:], st)
		for k := 1; k < w; k++ {
			scr.SetContent(x+used+k, y, ' ', nil, st)
		}
		used += w
	}
	return used
}

// drawLine draws rich text; spoilers are masked unless reveal is set.
func drawLine(scr tcell.Screen, x, y, maxW int, l line, reveal bool, bg *tcell.Color) int {
	used := 0
	for _, sp := range l {
		st := sp.style
		if bg != nil {
			st = st.Background(*bg)
		}
		text := sp.text
		if sp.spoiler && !reveal {
			text = strings.Repeat("░", textWidth(text))
		}
		used += drawText(scr, x+used, y, maxW-used, text, st)
		if sp.fill && used < maxW {
			fill(scr, x+used, y, maxW-used, st)
			used = maxW
		}
	}
	return used
}

func fill(scr tcell.Screen, x, y, w int, st tcell.Style) {
	for i := 0; i < w; i++ {
		scr.SetContent(x+i, y, ' ', nil, st)
	}
}

// drawRight draws s right-aligned so it ends at x+w.
func drawRight(scr tcell.Screen, x, y, w int, s string, st tcell.Style) {
	s = truncate(s, w)
	drawText(scr, x+w-textWidth(s), y, w, s, st)
}
