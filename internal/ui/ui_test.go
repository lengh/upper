package ui

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/lengh/upper/internal/discord"
	"github.com/lengh/upper/internal/state"
)

func TestSplitMessage(t *testing.T) {
	long := strings.Repeat("word ", 1000) // 5000 chars
	parts := splitMessage(long, 2000)
	if len(parts) != 3 {
		t.Fatalf("got %d parts", len(parts))
	}
	joined := strings.Join(parts, " ")
	if strings.Join(strings.Fields(joined), " ") != strings.TrimSpace(long) {
		t.Fatal("content lost while splitting")
	}
	for _, p := range parts {
		if utf8.RuneCountInString(p) > 2000 {
			t.Fatalf("part too long: %d", len(p))
		}
	}
	// No spaces at all still splits, and multibyte runes are kept whole.
	parts = splitMessage(strings.Repeat("é", 4500), 2000)
	if len(parts) != 3 || utf8.RuneCountInString(parts[0]) != 2000 || !utf8.ValidString(parts[0]) {
		t.Fatalf("rune split wrong: %d parts", len(parts))
	}
	if got := splitMessage("hi", 2000); len(got) != 1 || got[0] != "hi" {
		t.Fatalf("short = %v", got)
	}
}

func TestRankChannels(t *testing.T) {
	all := []state.ChannelInfo{
		{ID: 1, Name: "general", Category: "Server A", Last: 5},
		{ID: 2, Name: "gaming-news", Category: "Server B", Last: 9},
		{ID: 3, Name: "off-topic", Category: "Server A", Last: 1},
		{ID: 4, Name: "Alice", Type: discord.ChannelDM, Last: 3, Mentions: 2},
	}
	if r := rankChannels(all, "gen", 0); len(r) == 0 || r[0].ID != 1 {
		t.Fatalf("gen -> %+v", r)
	}
	if r := rankChannels(all, "gn", 0); len(r) != 2 {
		t.Fatalf("gn should fuzzy-match general and gaming-news, got %+v", r)
	}
	if r := rankChannels(all, "", 0); r[0].ID != 4 {
		t.Fatalf("empty query should put mentions first, got %+v", r[0])
	}
	if r := rankChannels(all, "zzz", 0); len(r) != 0 {
		t.Fatalf("zzz -> %+v", r)
	}
}

type fakeResolver struct{}

func (fakeResolver) UserName(_, id discord.Snowflake) (string, bool) {
	return "alice", id == 1
}
func (fakeResolver) RoleName(_, id discord.Snowflake) (string, bool)     { return "mods", id == 2 }
func (fakeResolver) ChannelNameByID(id discord.Snowflake) (string, bool) { return "general", id == 3 }

func TestRenderContent(t *testing.T) {
	th := theme{mention: "M", code: "C"}
	cases := map[string]string{
		"hi <@1> and <@!1>":         "hi [M]@alice[-] and [M]@alice[-]",
		"<@&2> see <#3>":            "[M]@mods[-] see [M]#general[-]",
		"<@9>":                      "[M]@unknown-user[-]",
		"nice <:pog:123>":           "nice :pog:",
		"**bold** and *it*":         "[::b]bold[::B] and [::i]it[::I]",
		"use `x[y]`":                "use [C]x[y[][-]",
		"[red]not a tag":            "[red[]not a tag",
		"snake_case_name stays":     "snake_case_name stays",
		"```go\nfmt.Println()\n```": "[C]  │ fmt.Println()[-]",
	}
	for in, want := range cases {
		if got := renderContent(in, 0, fakeResolver{}, th); got != want {
			t.Errorf("render(%q)\n got  %q\n want %q", in, got, want)
		}
	}
}
