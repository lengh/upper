package ui

import (
	"strings"

	"github.com/gdamore/tcell/v2"
)

// The empty-state cat: grooming its paw, every so often looking up and
// blinking before going back to it. Plain ASCII, so it renders the same in
// any terminal and font.
//
// Letters in the template mark the parts that move or get their own colour:
// E eyes, N nose, T tongue, P toe beans, K blush, W whiskers.
var catTemplate = []string{
	`        /\             /\`,
	`       /  \.---------./  \`,
	`      /                   \`,
	`     |     E         E     |`,
	`   WW|    KK    N    KK    |WW`,
	`   WW|        \_T_/        |WW`,
	`     \        (PPP)        /`,
	`     '-.____  (   )  ____.-'`,
	`        /      ` + "`" + `-'      \`,
	`       /                 \`,
	`      |    |         |    |     (`,
	`      |    |         |    |      )`,
	`       \___|_________|___/______/`,
	`           ` + "`" + `         ` + "`",
}

var catWidth = func() int {
	w := 0
	for _, l := range catTemplate {
		w = max(w, len(l))
	}
	return w
}()

// catCenter is the column of the cat's nose: the drawing is centred on its
// face, not its bounding box, which the tail would pull off-centre.
var catCenter = strings.IndexByte(catTemplate[4], 'N')

// catPose returns the eyes and tongue for an animation tick (250 ms). A
// six-second loop: three seconds of licking, then a pause to look around,
// with one slow blink.
func catPose(tick int) (eye, tongue byte) {
	switch t := tick % 24; {
	case t < 12:
		if t/2%2 == 0 {
			return '^', 'U'
		}
		return '^', 'u'
	case t == 17:
		return '-', 'w'
	default:
		return 'o', 'w'
	}
}

// drawCat draws the cat with its top-left corner at (x, y).
func drawCat(scr tcell.Screen, th *Theme, x, y, tick int, maxW, maxH int) {
	eye, tongue := catPose(tick)
	outline := th.fg(th.Subtle)
	pink := th.fg(th.Nicks[2]) // the theme's pink
	if th.mono {
		outline, pink = th.base(), th.base().Bold(true)
	}
	for row, l := range catTemplate {
		if row >= maxH {
			return
		}
		for col := 0; col < len(l) && col < maxW; col++ {
			ch, st := rune(l[col]), outline
			switch l[col] {
			case 'E':
				ch, st = rune(eye), th.fg(th.Text).Bold(true)
			case 'N':
				ch, st = '.', pink.Bold(true)
			case 'T':
				ch, st = rune(tongue), pink.Bold(true)
			case 'P':
				ch, st = ',', pink
			case 'K':
				ch, st = '~', pink.Dim(true)
				if !th.mono {
					st = th.fg(blend(th.Nicks[2], th.Faint, 0.45))
				}
			case 'W':
				ch, st = '-', th.muted()
			case ' ':
				continue
			}
			scr.SetContent(x+col, y+row, ch, nil, st)
		}
	}
}

// catText renders a pose as plain text (for tests and the README).
func catText(tick int) string {
	eye, tongue := catPose(tick)
	r := strings.NewReplacer("E", string(eye), "N", ".", "T", string(tongue), "P", ",", "K", "~", "W", "-")
	return r.Replace(strings.Join(catTemplate, "\n"))
}
