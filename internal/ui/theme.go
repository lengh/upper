package ui

import (
	"hash/fnv"
	"math"
	"os"

	"github.com/gdamore/tcell/v2"
)

// Theme is the full set of colours the interface draws with. Every colour
// has one job, so the screen reads the same way everywhere: Accent marks
// where you are, Mention marks what wants your attention, Muted marks what
// can be ignored.
type Theme struct {
	Name string

	Bg      tcell.Color // main background (ColorDefault = terminal's own)
	Text    tcell.Color // message text
	Subtle  tcell.Color // secondary text: topics, previews
	Muted   tcell.Color // timestamps, separators, hints
	Faint   tcell.Color // rules and quiet glyphs
	Bar     tcell.Color // title and status bar background
	BarText tcell.Color
	Surface tcell.Color // selection and current-channel background
	Cursor  tcell.Color // keyboard cursor background in lists
	Accent  tcell.Color // focus, own name, prompt
	Mention tcell.Color // highlights of you
	MentBg  tcell.Color // background tint of messages that mention you
	Badge   tcell.Color // unread-count pill background
	BadgeFg tcell.Color
	Error   tcell.Color
	Warn    tcell.Color
	OK      tcell.Color
	Link    tcell.Color
	Code    tcell.Color
	CodeBg  tcell.Color
	Nicks   []tcell.Color // palette for user colours

	mono  bool
	light bool
}

func hex(s string) tcell.Color { return tcell.GetColor(s) }

// Mocha and Latte are the Catppuccin palettes: soft, high-legibility colours
// that stay distinguishable on 256-colour terminals too.
func darkTheme() *Theme {
	return &Theme{
		Name: "dark", Bg: tcell.ColorDefault,
		Text: hex("#cdd6f4"), Subtle: hex("#a6adc8"), Muted: hex("#7f849c"), Faint: hex("#45475a"),
		Bar: hex("#181825"), BarText: hex("#bac2de"), Surface: hex("#313244"), Cursor: hex("#45475a"),
		Accent: hex("#cba6f7"), Mention: hex("#fab387"), MentBg: hex("#2a2420"),
		Badge: hex("#f38ba8"), BadgeFg: hex("#11111b"),
		Error: hex("#f38ba8"), Warn: hex("#f9e2af"), OK: hex("#a6e3a1"),
		Link: hex("#74c7ec"), Code: hex("#f5e0dc"), CodeBg: hex("#11111b"),
		Nicks: []tcell.Color{
			hex("#f5e0dc"), hex("#f2cdcd"), hex("#f5c2e7"), hex("#cba6f7"), hex("#eba0ac"),
			hex("#fab387"), hex("#f9e2af"), hex("#a6e3a1"), hex("#94e2d5"), hex("#89dceb"),
			hex("#74c7ec"), hex("#89b4fa"), hex("#b4befe"),
		},
	}
}

func lightTheme() *Theme {
	return &Theme{
		Name: "light", Bg: tcell.ColorDefault, light: true,
		Text: hex("#4c4f69"), Subtle: hex("#5c5f77"), Muted: hex("#8c8fa1"), Faint: hex("#bcc0cc"),
		Bar: hex("#e6e9ef"), BarText: hex("#5c5f77"), Surface: hex("#ccd0da"), Cursor: hex("#bcc0cc"),
		Accent: hex("#8839ef"), Mention: hex("#fe640b"), MentBg: hex("#fbeee4"),
		Badge: hex("#d20f39"), BadgeFg: hex("#eff1f5"),
		Error: hex("#d20f39"), Warn: hex("#df8e1d"), OK: hex("#40a02b"),
		Link: hex("#209fb5"), Code: hex("#dc8a78"), CodeBg: hex("#e6e9ef"),
		Nicks: []tcell.Color{
			hex("#dc8a78"), hex("#dd7878"), hex("#ea76cb"), hex("#8839ef"), hex("#e64553"),
			hex("#fe640b"), hex("#df8e1d"), hex("#40a02b"), hex("#179299"), hex("#04a5e5"),
			hex("#209fb5"), hex("#1e66f5"), hex("#7287fd"),
		},
	}
}

// monoTheme honours NO_COLOR: structure comes from bold, dim, underline and
// reverse video only.
func monoTheme() *Theme {
	d := tcell.ColorDefault
	return &Theme{Name: "mono", mono: true, Bg: d, Text: d, Subtle: d, Muted: d, Faint: d, Bar: d,
		BarText: d, Surface: d, Cursor: d, Accent: d, Mention: d, MentBg: d, Badge: d, BadgeFg: d,
		Error: d, Warn: d, OK: d, Link: d, Code: d, CodeBg: d, Nicks: []tcell.Color{d}}
}

// loadTheme picks a theme by name, falling back to NO_COLOR handling.
func loadTheme(name string) *Theme {
	if _, ok := os.LookupEnv("NO_COLOR"); ok || name == "mono" {
		return monoTheme()
	}
	if name == "light" {
		return lightTheme()
	}
	return darkTheme()
}

// Styles. In mono mode attributes carry the meaning colours otherwise would.

func (t *Theme) base() tcell.Style            { return tcell.StyleDefault.Background(t.Bg).Foreground(t.Text) }
func (t *Theme) fg(c tcell.Color) tcell.Style { return t.base().Foreground(c) }
func (t *Theme) bar() tcell.Style {
	if t.mono {
		return tcell.StyleDefault.Reverse(true)
	}
	return tcell.StyleDefault.Background(t.Bar).Foreground(t.BarText)
}
func (t *Theme) muted() tcell.Style {
	if t.mono {
		return t.base().Dim(true)
	}
	return t.fg(t.Muted)
}
func (t *Theme) faint() tcell.Style {
	if t.mono {
		return t.base().Dim(true)
	}
	return t.fg(t.Faint)
}
func (t *Theme) accent() tcell.Style {
	if t.mono {
		return t.base().Bold(true)
	}
	return t.fg(t.Accent)
}
func (t *Theme) mention() tcell.Style {
	if t.mono {
		return t.base().Bold(true).Underline(true)
	}
	return t.fg(t.Mention)
}
func (t *Theme) errorStyle() tcell.Style {
	if t.mono {
		return t.base().Bold(true)
	}
	return t.fg(t.Error)
}
func (t *Theme) badge() tcell.Style {
	if t.mono {
		return tcell.StyleDefault.Reverse(true).Bold(true)
	}
	return tcell.StyleDefault.Background(t.Badge).Foreground(t.BadgeFg).Bold(true)
}
func (t *Theme) surface(s tcell.Style) tcell.Style {
	if t.mono {
		return s.Reverse(true)
	}
	return s.Background(t.Surface)
}
func (t *Theme) cursor(s tcell.Style) tcell.Style {
	if t.mono {
		return s.Reverse(true).Bold(true)
	}
	return s.Background(t.Cursor)
}

// nickColor gives every user a stable colour derived from their ID, so it
// survives nickname changes (as catgirl does). Discord role colours win when
// set, adjusted so they stay legible on the theme's background.
func (t *Theme) nickColor(userID uint64, roleColor int) tcell.Color {
	if t.mono {
		return tcell.ColorDefault
	}
	if roleColor != 0 {
		return t.legible(roleColor)
	}
	h := fnv.New32a()
	var b [8]byte
	for i := range b {
		b[i] = byte(userID >> (8 * i))
	}
	_, _ = h.Write(b[:])
	return t.Nicks[h.Sum32()%uint32(len(t.Nicks))]
}

// legible clamps a colour's lightness so dark role colours don't vanish on a
// dark terminal (and light ones on a light terminal).
func (t *Theme) legible(rgb int) tcell.Color {
	r, g, b := float64(rgb>>16&0xff)/255, float64(rgb>>8&0xff)/255, float64(rgb&0xff)/255
	h, s, l := rgbToHSL(r, g, b)
	if t.light {
		l = math.Min(l, 0.42)
	} else {
		l = math.Max(l, 0.62)
	}
	r, g, b = hslToRGB(h, s, l)
	return tcell.NewRGBColor(int32(r*255+0.5), int32(g*255+0.5), int32(b*255+0.5))
}

func rgbToHSL(r, g, b float64) (h, s, l float64) {
	mx, mn := math.Max(r, math.Max(g, b)), math.Min(r, math.Min(g, b))
	l = (mx + mn) / 2
	if mx == mn {
		return 0, 0, l
	}
	d := mx - mn
	if l > 0.5 {
		s = d / (2 - mx - mn)
	} else {
		s = d / (mx + mn)
	}
	switch mx {
	case r:
		h = (g - b) / d
		if g < b {
			h += 6
		}
	case g:
		h = (b-r)/d + 2
	default:
		h = (r-g)/d + 4
	}
	return h / 6, s, l
}

func hslToRGB(h, s, l float64) (r, g, b float64) {
	if s == 0 {
		return l, l, l
	}
	hue := func(p, q, t float64) float64 {
		if t < 0 {
			t++
		}
		if t > 1 {
			t--
		}
		switch {
		case t < 1.0/6:
			return p + (q-p)*6*t
		case t < 0.5:
			return q
		case t < 2.0/3:
			return p + (q-p)*(2.0/3-t)*6
		}
		return p
	}
	q := l * (1 + s)
	if l >= 0.5 {
		q = l + s - l*s
	}
	p := 2*l - q
	return hue(p, q, h+1.0/3), hue(p, q, h), hue(p, q, h-1.0/3)
}
