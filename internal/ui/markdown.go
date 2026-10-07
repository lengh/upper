package ui

import (
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/gdamore/tcell/v2"
	"github.com/lengh/upper/internal/discord"
)

// resolver looks up names for mention tokens.
type resolver interface {
	UserName(guildID, id discord.Snowflake) (string, bool)
	RoleName(guildID, id discord.Snowflake) (string, bool)
	ChannelNameByID(id discord.Snowflake) (string, bool)
}

// md renders Discord-flavoured markdown into styled spans.
type md struct {
	th    *Theme
	r     resolver
	guild discord.Snowflake
	me    discord.Snowflake
	nick  func(user discord.Snowflake) tcell.Color // colour for a user mention
	role  func(role discord.Snowflake) tcell.Color // colour for a role mention
	base  tcell.Style
	plain bool // previews: no block formatting
}

var (
	reCodeBlock = regexp.MustCompile("(?s)```([a-zA-Z0-9_+.#-]*\n)?(.*?)```")
	reToken     = regexp.MustCompile(`<(@!?|@&|#|a?:[A-Za-z0-9_~]+:|t:)(\d+)(?::([tTdDfFR]))?>`)
	reURL       = regexp.MustCompile(`https?://[^\s<>()\[\]]+[^\s<>()\[\].,:;!?'"]`)
	reMasked    = regexp.MustCompile(`\[([^\]\n]+)\]\((https?://[^)\s]+)\)`)
)

// render converts a whole message body.
func (m *md) render(content string) []span {
	content = sanitize(content)
	var out []span
	last := 0
	for _, loc := range reCodeBlock.FindAllStringSubmatchIndex(content, -1) {
		out = append(out, m.blocks(strings.TrimSuffix(content[last:loc[0]], "\n"))...)
		code := strings.Trim(content[loc[4]:loc[5]], "\n")
		if len(out) > 0 {
			out = append(out, span{text: "\n", style: m.base})
		}
		st := m.th.fg(m.th.Code).Background(m.th.CodeBg)
		if m.th.mono {
			st = m.th.base().Dim(true)
		}
		for i, l := range strings.Split(code, "\n") {
			if i > 0 {
				out = append(out, span{text: "\n", style: st, fill: true})
			}
			out = append(out, span{text: " " + l, style: st, fill: true})
		}
		out = append(out, span{text: "\n", style: st, fill: true})
		last = loc[1]
		if last < len(content) && content[last] == '\n' {
			last++
		}
	}
	out = append(out, m.blocks(content[last:])...)
	// Drop a trailing newline left by a closing code block.
	if n := len(out); n > 0 && out[n-1].text == "\n" {
		out = out[:n-1]
	}
	return out
}

// blocks handles line-level syntax: quotes, headers, subtext and lists.
func (m *md) blocks(s string) []span {
	if s == "" {
		return nil
	}
	var out []span
	quoteRest := false
	for i, l := range strings.Split(s, "\n") {
		if i > 0 {
			out = append(out, span{text: "\n", style: m.base})
		}
		st := m.base
		if !m.plain {
			switch {
			case quoteRest || strings.HasPrefix(l, ">>> "):
				if !quoteRest {
					l, quoteRest = l[4:], true
				}
				out = append(out, span{text: "▎ ", style: m.th.faint()})
				st = st.Italic(true)
			case strings.HasPrefix(l, "> "):
				out = append(out, span{text: "▎ ", style: m.th.faint()})
				l, st = l[2:], st.Italic(true)
			case strings.HasPrefix(l, "-# "):
				l, st = l[3:], m.th.muted()
			case strings.HasPrefix(l, "### "):
				l, st = l[4:], st.Bold(true)
			case strings.HasPrefix(l, "## "):
				l, st = l[3:], st.Bold(true).Underline(true)
			case strings.HasPrefix(l, "# "):
				l, st = l[2:], m.th.accent().Bold(true).Underline(true)
			case strings.HasPrefix(l, "- ") || strings.HasPrefix(l, "* "):
				out = append(out, span{text: "• ", style: m.th.muted()})
				l = l[2:]
			case strings.HasPrefix(l, "  - ") || strings.HasPrefix(l, "  * "):
				out = append(out, span{text: "  ◦ ", style: m.th.muted()})
				l = l[4:]
			}
		}
		out = append(out, m.inline(l, st)...)
	}
	return out
}

type inlineRule struct {
	open, close string
	apply       func(tcell.Style) tcell.Style
	spoiler     bool
	word        bool // delimiter must sit at word boundaries (_italic_)
}

var inlineRules = []inlineRule{
	{open: "**", close: "**", apply: func(s tcell.Style) tcell.Style { return s.Bold(true) }},
	{open: "__", close: "__", apply: func(s tcell.Style) tcell.Style { return s.Underline(true) }},
	{open: "~~", close: "~~", apply: func(s tcell.Style) tcell.Style { return s.StrikeThrough(true) }},
	{open: "||", close: "||", spoiler: true, apply: func(s tcell.Style) tcell.Style { return s }},
	{open: "*", close: "*", apply: func(s tcell.Style) tcell.Style { return s.Italic(true) }},
	{open: "_", close: "_", word: true, apply: func(s tcell.Style) tcell.Style { return s.Italic(true) }},
}

func isWordRune(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }

// inline renders inline markup by repeatedly taking the earliest construct,
// recursing into emphasis so nesting like **bold ||spoiler||** works.
func (m *md) inline(s string, st tcell.Style) []span {
	return m.inlineSp(s, st, false)
}

func (m *md) inlineSp(s string, st tcell.Style, spoiler bool) []span {
	var out []span
	emit := func(text string, style tcell.Style) {
		if text != "" {
			out = append(out, span{text: text, style: style, spoiler: spoiler})
		}
	}
	for len(s) > 0 {
		best, bestEnd := -1, 0
		var produce func()

		consider := func(start, end int, f func()) {
			if start >= 0 && (best < 0 || start < best) {
				best, bestEnd, produce = start, end, f
			}
		}

		// Backslash escapes: \* shows a literal asterisk.
		for i := 0; i+1 < len(s); i++ {
			if s[i] == '\\' && strings.IndexByte("\\*_~|`>#-[]()<:", s[i+1]) >= 0 {
				ch := s[i+1 : i+2]
				consider(i, i+2, func() { emit(ch, st) })
				break
			}
		}
		// Inline code: highest priority, no nested formatting.
		if i := strings.IndexByte(s, '`'); i >= 0 {
			if j := strings.IndexByte(s[i+1:], '`'); j > 0 {
				code := s[i+1 : i+1+j]
				consider(i, i+2+j, func() {
					cs := m.th.fg(m.th.Code).Background(m.th.CodeBg)
					if m.th.mono {
						cs = st.Reverse(true)
					}
					emit(code, cs)
				})
			}
		}
		if loc := reToken.FindStringSubmatchIndex(s); loc != nil {
			kind, idS := s[loc[2]:loc[3]], s[loc[4]:loc[5]]
			var style string
			if loc[6] >= 0 {
				style = s[loc[6]:loc[7]]
			}
			consider(loc[0], loc[1], func() {
				id, _ := discord.ParseSnowflake(idS)
				text, tst := m.token(kind, id, style, st)
				emit(text, tst)
			})
		}
		if loc := reMasked.FindStringSubmatchIndex(s); loc != nil {
			label := s[loc[2]:loc[3]]
			consider(loc[0], loc[1], func() {
				out = append(out, m.inlineSp(label, m.link(st), spoiler)...)
			})
		}
		if loc := reURL.FindStringIndex(s); loc != nil {
			consider(loc[0], loc[1], func() { emit(s[loc[0]:loc[1]], m.link(st)) })
		}
		for _, rule := range inlineRules {
			start, end, inner := findDelimited(s, rule)
			if start < 0 {
				continue
			}
			rule := rule
			consider(start, end, func() {
				out = append(out, m.inlineSp(inner, rule.apply(st), spoiler || rule.spoiler)...)
			})
		}

		if best < 0 {
			emit(s, st)
			break
		}
		emit(s[:best], st)
		produce()
		s = s[bestEnd:]
	}
	return out
}

// findDelimited finds the first well-formed open…close pair.
func findDelimited(s string, r inlineRule) (start, end int, inner string) {
	from := 0
	for {
		i := strings.Index(s[from:], r.open)
		if i < 0 {
			return -1, 0, ""
		}
		i += from
		body := i + len(r.open)
		j := strings.Index(s[body:], r.close)
		if j <= 0 {
			return -1, 0, ""
		}
		j += body
		inner := s[body:j]
		ok := !strings.HasPrefix(inner, " ") && !strings.HasSuffix(inner, " ")
		if r.open == "*" && (strings.HasPrefix(s[i:], "**") || strings.HasPrefix(s[j:], "**")) {
			ok = false
		}
		if r.open == "_" && (strings.HasPrefix(s[i:], "__") || strings.HasPrefix(s[j:], "__")) {
			ok = false
		}
		if r.word {
			if prev, _ := utf8.DecodeLastRuneInString(s[:i]); i > 0 && isWordRune(prev) {
				ok = false
			}
			if next, _ := utf8.DecodeRuneInString(s[j+len(r.close):]); j+len(r.close) < len(s) && isWordRune(next) {
				ok = false
			}
		}
		if ok {
			return i, j + len(r.close), inner
		}
		from = i + len(r.open)
	}
}

func (m *md) link(st tcell.Style) tcell.Style {
	if m.th.mono {
		return st.Underline(true)
	}
	return st.Foreground(m.th.Link).Underline(true)
}

func (m *md) token(kind string, id discord.Snowflake, tsStyle string, st tcell.Style) (string, tcell.Style) {
	ment := st.Bold(true)
	switch {
	case kind == "@" || kind == "@!":
		name, ok := m.r.UserName(m.guild, id)
		if !ok {
			name = "unknown-user"
		}
		if id == m.me {
			if m.th.mono {
				return "@" + name, st.Reverse(true).Bold(true)
			}
			return "@" + name, ment.Foreground(m.th.Mention).Background(m.th.MentBg)
		}
		if m.nick != nil && !m.th.mono {
			ment = ment.Foreground(m.nick(id))
		}
		return "@" + name, ment
	case kind == "@&":
		name, ok := m.r.RoleName(m.guild, id)
		if !ok {
			name = "unknown-role"
		}
		if m.role != nil && !m.th.mono {
			ment = ment.Foreground(m.role(id))
		}
		return "@" + name, ment
	case kind == "#":
		name, ok := m.r.ChannelNameByID(id)
		if !ok {
			name = "unknown"
		}
		if !m.th.mono {
			ment = ment.Foreground(m.th.Accent)
		}
		return "#" + name, ment
	case kind == "t:":
		return formatTimestamp(int64(id), tsStyle), st.Underline(true)
	default: // custom emoji
		name := strings.Trim(strings.TrimPrefix(kind, "a"), ":")
		return ":" + name + ":", st.Foreground(m.th.Warn)
	}
}

// plainContent resolves mention tokens to names without styling, for
// one-line previews.
func plainContent(content string, guildID discord.Snowflake, r resolver) string {
	return reToken.ReplaceAllStringFunc(content, func(tok string) string {
		m := reToken.FindStringSubmatch(tok)
		id, _ := discord.ParseSnowflake(m[2])
		switch kind := m[1]; {
		case kind == "@" || kind == "@!":
			if n, ok := r.UserName(guildID, id); ok {
				return "@" + n
			}
		case kind == "@&":
			if n, ok := r.RoleName(guildID, id); ok {
				return "@" + n
			}
		case kind == "#":
			if n, ok := r.ChannelNameByID(id); ok {
				return "#" + n
			}
		case kind == "t:":
			return formatTimestamp(int64(id), m[3])
		default:
			return ":" + strings.Trim(strings.TrimPrefix(kind, "a"), ":") + ":"
		}
		return tok
	})
}

func formatTimestamp(unix int64, style string) string {
	t := time.Unix(unix, 0).Local()
	switch style {
	case "t":
		return t.Format("15:04")
	case "T":
		return t.Format("15:04:05")
	case "d":
		return t.Format("2006-01-02")
	case "D":
		return t.Format("2 January 2006")
	case "F":
		return t.Format("Monday, 2 January 2006 15:04")
	case "R":
		return relative(time.Until(t))
	}
	return t.Format("2 January 2006 15:04")
}

func relative(d time.Duration) string {
	past := d < 0
	if past {
		d = -d
	}
	var s string
	switch {
	case d < time.Minute:
		s = strconv.Itoa(int(d.Seconds())) + " seconds"
	case d < time.Hour:
		s = strconv.Itoa(int(d.Minutes())) + " minutes"
	case d < 48*time.Hour:
		s = strconv.Itoa(int(d.Hours())) + " hours"
	default:
		s = strconv.Itoa(int(d.Hours()/24)) + " days"
	}
	if past {
		return s + " ago"
	}
	return "in " + s
}
