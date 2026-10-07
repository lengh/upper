package ui

import (
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/lengh/upper/internal/discord"
	"github.com/rivo/tview"
)

// resolver looks up names for mention tokens.
type resolver interface {
	UserName(guildID, id discord.Snowflake) (string, bool)
	RoleName(guildID, id discord.Snowflake) (string, bool)
	ChannelNameByID(id discord.Snowflake) (string, bool)
}

var (
	reCodeBlock = regexp.MustCompile("(?s)```(?:[a-zA-Z0-9_+-]*\n)?(.*?)```")
	reInline    = regexp.MustCompile("`([^`\n]+)`")
	reToken     = regexp.MustCompile(`<(@!?|@&|#|a?:[A-Za-z0-9_~]+:|t:)(\d+)(?::([tTdDfFR]))?>`)
	reBold      = regexp.MustCompile(`\*\*([^*\n]+?)\*\*`)
	reUnder     = regexp.MustCompile(`__([^_\n]+?)__`)
	reItalic    = regexp.MustCompile(`(^|[^\w*])[*_]([^*_\n]+?)[*_]($|[^\w*])`)
	reStrike    = regexp.MustCompile(`~~([^~\n]+?)~~`)
	reSpoiler   = regexp.MustCompile(`\|\|([^|\n]+?)\|\|`)
)

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

// renderContent converts Discord markdown to tview-tagged text.
func renderContent(content string, guildID discord.Snowflake, r resolver, th theme) string {
	var b strings.Builder
	last := 0
	for _, m := range reCodeBlock.FindAllStringSubmatchIndex(content, -1) {
		b.WriteString(renderInline(content[last:m[0]], guildID, r, th))
		code := strings.TrimRight(content[m[2]:m[3]], "\n")
		b.WriteString("\n[" + th.code + "]")
		for i, line := range strings.Split(code, "\n") {
			if i > 0 {
				b.WriteByte('\n')
			}
			b.WriteString("  │ " + tview.Escape(line))
		}
		b.WriteString("[-]\n")
		last = m[1]
	}
	b.WriteString(renderInline(content[last:], guildID, r, th))
	return strings.Trim(b.String(), "\n")
}

func renderInline(s string, guildID discord.Snowflake, r resolver, th theme) string {
	var b strings.Builder
	last := 0
	for _, m := range reInline.FindAllStringSubmatchIndex(s, -1) {
		b.WriteString(renderText(s[last:m[0]], guildID, r, th))
		b.WriteString("[" + th.code + "]" + tview.Escape(s[m[2]:m[3]]) + "[-]")
		last = m[1]
	}
	b.WriteString(renderText(s[last:], guildID, r, th))
	return b.String()
}

func renderText(s string, guildID discord.Snowflake, r resolver, th theme) string {
	var b strings.Builder
	last := 0
	for _, m := range reToken.FindAllStringSubmatchIndex(s, -1) {
		b.WriteString(styleText(tview.Escape(s[last:m[0]])))
		kind := s[m[2]:m[3]]
		id, _ := discord.ParseSnowflake(s[m[4]:m[5]])
		var style string
		if m[6] >= 0 {
			style = s[m[6]:m[7]]
		}
		b.WriteString(renderToken(kind, id, style, guildID, r, th))
		last = m[1]
	}
	b.WriteString(styleText(tview.Escape(s[last:])))
	return b.String()
}

func renderToken(kind string, id discord.Snowflake, style string, guildID discord.Snowflake, r resolver, th theme) string {
	hl := func(s string) string { return "[" + th.mention + "]" + tview.Escape(s) + "[-]" }
	switch {
	case kind == "@" || kind == "@!":
		if n, ok := r.UserName(guildID, id); ok {
			return hl("@" + n)
		}
		return hl("@unknown-user")
	case kind == "@&":
		if n, ok := r.RoleName(guildID, id); ok {
			return hl("@" + n)
		}
		return hl("@unknown-role")
	case kind == "#":
		if n, ok := r.ChannelNameByID(id); ok {
			return hl("#" + n)
		}
		return hl("#unknown")
	case kind == "t:":
		return hl(formatTimestamp(int64(id), style))
	default: // custom emoji ":name:" or "a:name:"
		return tview.Escape(":" + strings.Trim(strings.TrimPrefix(kind, "a"), ":") + ":")
	}
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

// styleText applies inline emphasis to already-escaped text.
func styleText(s string) string {
	if !strings.ContainsAny(s, "*_~|") {
		return s
	}
	s = reBold.ReplaceAllString(s, "[::b]$1[::B]")
	s = reUnder.ReplaceAllString(s, "[::u]$1[::U]")
	s = reItalic.ReplaceAllString(s, "$1[::i]$2[::I]$3")
	s = reStrike.ReplaceAllString(s, "[::s]$1[::S]")
	s = reSpoiler.ReplaceAllString(s, "[::r]$1[::R]")
	return s
}
