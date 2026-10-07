package ui

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/gdamore/tcell/v2"
	"github.com/lengh/upper/internal/discord"
	"github.com/lengh/upper/internal/state"
)

func TestSplitMessage(t *testing.T) {
	long := strings.Repeat("word ", 1000) // 5000 chars
	parts := splitMessage(long, 2000)
	if len(parts) != 3 {
		t.Fatalf("got %d parts", len(parts))
	}
	if strings.Join(strings.Fields(strings.Join(parts, " ")), " ") != strings.TrimSpace(long) {
		t.Fatal("content lost while splitting")
	}
	for _, p := range parts {
		if utf8.RuneCountInString(p) > 2000 {
			t.Fatalf("part too long: %d", len(p))
		}
	}
	parts = splitMessage(strings.Repeat("é", 4500), 2000)
	if len(parts) != 3 || utf8.RuneCountInString(parts[0]) != 2000 || !utf8.ValidString(parts[0]) {
		t.Fatalf("rune split wrong: %d parts", len(parts))
	}
}

func TestRankChannels(t *testing.T) {
	all := []state.ChannelInfo{
		{ID: 1, Name: "general", Category: "Server A", Last: 5},
		{ID: 2, Name: "gaming-news", Category: "Server B", Last: 9},
		{ID: 3, Name: "off-topic", Category: "Server A", Last: 1},
		{ID: 4, Name: "Alice", Type: discord.ChannelDM, Last: 3, Mentions: 2},
	}
	r := rankChannels(all, "gen", 0)
	if len(r) == 0 || r[0].c.ID != 1 || len(r[0].pos) != 3 || r[0].pos[0] != 0 {
		t.Fatalf("gen -> %+v", r)
	}
	if r := rankChannels(all, "gn", 0); len(r) != 2 {
		t.Fatalf("gn should fuzzy-match general and gaming-news, got %d", len(r))
	}
	if r := rankChannels(all, "", 0); r[0].c.ID != 4 {
		t.Fatalf("empty query should put mentions first, got %+v", r[0].c)
	}
	if r := rankChannels(all, "srv b gam", 0); len(r) != 0 && r[0].c.ID != 2 {
		t.Fatalf("server-qualified query -> %+v", r)
	}
}

func plain(l []line) []string {
	var out []string
	for _, x := range l {
		out = append(out, spansText(x))
	}
	return out
}

func TestWrapHangingAndHardBreaks(t *testing.T) {
	st := tcell.StyleDefault
	got := plain(wrap([]span{{text: "the quick brown fox jumps", style: st}}, 10))
	want := []string{"the quick", "brown fox", "jumps"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("wrap = %q", got)
	}
	got = plain(wrap([]span{{text: "abcdefghijklmnop", style: st}}, 6))
	if strings.Join(got, "|") != "abcdef|ghijkl|mnop" {
		t.Fatalf("hard break = %q", got)
	}
	// Styles split mid-word don't create break opportunities.
	got = plain(wrap([]span{{text: "aaaa ", style: st}, {text: "bb", style: st.Bold(true)}, {text: "cc", style: st}}, 6))
	if strings.Join(got, "|") != "aaaa|bbcc" {
		t.Fatalf("styled word = %q", got)
	}
	// Double-width characters count as two cells.
	got = plain(wrap([]span{{text: "日本語日本語", style: st}}, 5))
	if strings.Join(got, "|") != "日本|語日|本語" {
		t.Fatalf("wide = %q", got)
	}
	got = plain(wrap([]span{{text: "a\n\nb", style: st}}, 5))
	if strings.Join(got, "|") != "a||b" {
		t.Fatalf("newlines = %q", got)
	}
}

func TestTruncate(t *testing.T) {
	if got := truncate("general-chat", 8); got != "general…" {
		t.Fatalf("truncate = %q", got)
	}
	if got := truncate("ok", 8); got != "ok" {
		t.Fatalf("truncate = %q", got)
	}
}

type fakeResolver struct{}

func (fakeResolver) UserName(_, id discord.Snowflake) (string, bool)     { return "alice", id == 1 }
func (fakeResolver) RoleName(_, id discord.Snowflake) (string, bool)     { return "mods", id == 2 }
func (fakeResolver) ChannelNameByID(id discord.Snowflake) (string, bool) { return "general", id == 3 }

func render(s string) []span {
	th := darkTheme()
	m := &md{th: th, r: fakeResolver{}, me: 1, base: th.base()}
	return m.render(s)
}

func TestMarkdown(t *testing.T) {
	cases := map[string]string{
		"hi <@1> and <@&2> in <#3>": "hi @alice and @mods in #general",
		"nice <:pog:123>":           "nice :pog:",
		"**bold** and *it*":         "bold and it",
		"snake_case_name stays":     "snake_case_name stays",
		"use `x*y*z`":               "use x*y*z",
		`\*not italic\*`:            "*not italic*",
		"> quoted":                  "▎ quoted",
		"- item":                    "• item",
		"[label](https://x.y)":      "label",
		"||secret||":                "secret",
	}
	for in, want := range cases {
		if got := spansText(render(in)); got != want {
			t.Errorf("render(%q) = %q, want %q", in, got, want)
		}
	}

	spans := render("**bold ||hidden||**")
	var sawSpoiler, sawBold bool
	for _, s := range spans {
		_, _, attr := s.style.Decompose()
		sawBold = sawBold || attr&tcell.AttrBold != 0
		sawSpoiler = sawSpoiler || s.spoiler && s.text == "hidden"
	}
	if !sawBold || !sawSpoiler {
		t.Fatalf("nested formatting lost: %+v", spans)
	}

	spans = render("x <@1> y")
	for _, s := range spans {
		if s.text == "@alice" {
			if _, bg, _ := s.style.Decompose(); bg != darkTheme().MentBg {
				t.Fatal("self-mention isn't highlighted")
			}
		}
	}

	code := render("```go\nfmt.Println()\n```")
	if len(code) == 0 || !code[0].fill || !strings.Contains(spansText(code), "fmt.Println()") {
		t.Fatalf("code block = %+v", code)
	}
}

func TestPrepareMentions(t *testing.T) {
	people := []state.Candidate{{ID: 10, Name: "Alice Smith", Alt: "alice"}, {ID: 11, Name: "Al", Alt: "al"}}
	chans := []state.Candidate{{ID: 20, Name: "general"}}
	f := func(s string) string {
		return mapOutsideCode(s, func(x string) string {
			x = replaceRefs(x, '@', people, "<@", ">")
			x = replaceRefs(x, '#', chans, "<#", ">")
			return replaceShortcodes(x)
		})
	}
	cases := map[string]string{
		"hi @Alice Smith!":      "hi <@10>!",
		"hi @alice and @al":     "hi <@10> and <@11>",
		"mail me@alice.com":     "mail me@alice.com",
		"see #general :fire:":   "see <#20> 🔥",
		"`@alice :fire:` stays": "`@alice :fire:` stays",
		"@nobody":               "@nobody",
	}
	for in, want := range cases {
		if got := f(in); got != want {
			t.Errorf("prepare(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestEmojiTable(t *testing.T) {
	for _, name := range []string{"thumbsup", "+1", "fire", "heart", "joy", "skull"} {
		if emojiByName[name] == "" {
			t.Errorf("missing :%s:", name)
		}
	}
}

func TestLegibleRoleColors(t *testing.T) {
	th := darkTheme()
	c := th.legible(0x000080) // navy on a dark terminal
	r, g, b := c.RGB()
	if (r+g+b)/3 < 90 {
		t.Fatalf("navy stayed too dark: %d %d %d", r, g, b)
	}
}

func TestSedPattern(t *testing.T) {
	m := reSed.FindStringSubmatch(`s/teh/the/`)
	if m == nil || m[1] != "teh" || m[2] != "the" {
		t.Fatalf("sed = %v", m)
	}
	if reSed.MatchString("see/this") {
		t.Fatal("plain text matched sed")
	}
	if m := reQuick.FindStringSubmatch("+:fire:"); m == nil || m[1] != ":fire:" {
		t.Fatalf("quick react = %v", m)
	}
}
