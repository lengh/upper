package ui

import (
	"fmt"
	"strings"

	"github.com/lengh/upper/internal/discord"
	"github.com/rivo/tview"
)

// maxSidebarDMs bounds the DM list; older DMs stay reachable via Ctrl+K.
const maxSidebarDMs = 40

type nodeKind int

const (
	kindDMRoot nodeKind = iota
	kindGuild
	kindChannel
	kindLabel
)

type nodeRef struct {
	kind nodeKind
	id   Snowflake
}

// navState is the server/channel sidebar. Nodes are reused across refreshes
// so the cursor and expansion survive updates, and guild channels are only
// built while the guild is expanded.
type navState struct {
	a      *App
	root   *tview.TreeNode
	dmRoot *tview.TreeNode
	guilds map[Snowflake]*tview.TreeNode
	chans  map[Snowflake]*tview.TreeNode
}

func (n *navState) init(a *App) {
	n.a = a
	n.root = tview.NewTreeNode("")
	n.dmRoot = tview.NewTreeNode("").SetReference(nodeRef{kind: kindDMRoot}).SetExpanded(true)
	n.guilds = map[Snowflake]*tview.TreeNode{}
	n.chans = map[Snowflake]*tview.TreeNode{}
	a.tree.SetRoot(n.root).SetCurrentNode(n.dmRoot)
}

func (n *navState) chanNode(id Snowflake) *tview.TreeNode {
	node := n.chans[id]
	if node == nil {
		node = tview.NewTreeNode("").SetReference(nodeRef{kind: kindChannel, id: id})
		n.chans[id] = node
	}
	return node
}

func (n *navState) format(prefix, name string, unread bool, mentions int, open bool) string {
	th := n.a.th
	name = tview.Escape(name)
	var s string
	switch {
	case open:
		s = "[" + th.accent + "::b]" + prefix + name + "[-::B]"
	case unread || mentions > 0:
		s = "[" + th.unread + "::b]" + prefix + name + "[-::B]"
	default:
		s = "[" + th.muted + "]" + prefix + name + "[-]"
	}
	if mentions > 0 {
		s += fmt.Sprintf(" [%s::b](%d)[-::B]", th.mention, mentions)
	}
	return s
}

// refresh syncs the sidebar with the state.
func (n *navState) refresh() {
	a := n.a
	seen := map[Snowflake]bool{}

	// Direct messages.
	dms := a.st.PrivateChannels()
	dmMentions := 0
	dmUnread := false
	for _, c := range dms {
		dmMentions += c.Mentions
		dmUnread = dmUnread || c.Unread
	}
	arrow := "▸ "
	if n.dmRoot.IsExpanded() {
		arrow = "▾ "
	}
	n.dmRoot.SetText(n.format(arrow, "Direct Messages", dmUnread, dmMentions, false))
	if n.dmRoot.IsExpanded() {
		kids := make([]*tview.TreeNode, 0, min(len(dms), maxSidebarDMs))
		for i, c := range dms {
			// Always keep the open DM visible even if it is old.
			if i >= maxSidebarDMs && c.ID != a.current {
				continue
			}
			prefix := "@ "
			if c.Type == discord.ChannelGroupDM {
				prefix = "+ "
			}
			node := n.chanNode(c.ID).SetText(n.format(prefix, c.Name, c.Unread, c.Mentions, c.ID == a.current))
			kids = append(kids, node)
			seen[c.ID] = true
		}
		n.dmRoot.SetChildren(kids)
	} else {
		n.dmRoot.ClearChildren()
	}

	// Guilds.
	top := []*tview.TreeNode{n.dmRoot}
	alive := map[Snowflake]bool{}
	for _, g := range a.st.Guilds() {
		alive[g.ID] = true
		node := n.guilds[g.ID]
		if node == nil {
			node = tview.NewTreeNode("").SetReference(nodeRef{kind: kindGuild, id: g.ID}).SetExpanded(false)
			n.guilds[g.ID] = node
		}
		arrow := "▸ "
		if node.IsExpanded() {
			arrow = "▾ "
		}
		node.SetText(n.format(arrow, g.Name, g.Unread, g.Mentions, false))
		if node.IsExpanded() {
			n.fillGuild(node, g.ID, seen)
		} else {
			node.ClearChildren()
		}
		top = append(top, node)
	}
	for id := range n.guilds {
		if !alive[id] {
			delete(n.guilds, id)
		}
	}
	for id := range n.chans {
		if !seen[id] {
			delete(n.chans, id)
		}
	}
	n.root.SetChildren(top)
	// tview drops the cursor when the tree was drawn empty (before READY).
	if n.a.tree.GetCurrentNode() == nil {
		n.a.tree.SetCurrentNode(n.dmRoot)
	}
}

func (n *navState) fillGuild(node *tview.TreeNode, gid Snowflake, seen map[Snowflake]bool) {
	a := n.a
	chans := a.st.GuildChannels(gid)
	kids := make([]*tview.TreeNode, 0, len(chans)+8)
	cat := "\x00"
	for _, c := range chans {
		if c.Depth == 0 && c.Category != cat {
			cat = c.Category
			if cat != "" {
				kids = append(kids, tview.NewTreeNode("["+a.th.muted+"::d]"+tview.Escape(strings.ToUpper(cat))+"[-::D]").
					SetReference(nodeRef{kind: kindLabel}).SetSelectable(false))
			}
		}
		prefix := "# "
		if c.Depth > 0 {
			prefix = "  └ "
		} else if c.Type == discord.ChannelAnnouncement {
			prefix = "! "
		}
		kids = append(kids, n.chanNode(c.ID).SetText(n.format(prefix, c.Name, c.Unread, c.Mentions, c.ID == a.current)))
		seen[c.ID] = true
	}
	if len(kids) == 0 {
		kids = append(kids, tview.NewTreeNode("["+a.th.muted+"]no readable text channels[-]").
			SetReference(nodeRef{kind: kindLabel}).SetSelectable(false))
	}
	node.SetChildren(kids)
}

func (a *App) onTreeSelect(node *tview.TreeNode) {
	ref, _ := node.GetReference().(nodeRef)
	switch ref.kind {
	case kindDMRoot, kindGuild:
		node.SetExpanded(!node.IsExpanded())
		a.nav.refresh()
	case kindChannel:
		a.open(ref.id)
	}
}

// markCurrent expands the guild of the open channel and moves the cursor
// onto it, so the sidebar follows Ctrl+K and Alt+U jumps.
func (n *navState) markCurrent(id Snowflake) {
	ch, ok := n.a.st.Channel(id)
	if !ok {
		return
	}
	if ch.GuildID != 0 {
		if g := n.guilds[ch.GuildID]; g != nil {
			g.SetExpanded(true)
		}
	} else {
		n.dmRoot.SetExpanded(true)
	}
	n.refresh()
	if node := n.chans[id]; node != nil {
		n.a.tree.SetCurrentNode(node)
	}
}

// step opens the previous/next channel in sidebar order.
func (n *navState) step(down bool) {
	var list []Snowflake
	var walk func(*tview.TreeNode)
	walk = func(t *tview.TreeNode) {
		for _, c := range t.GetChildren() {
			if ref, ok := c.GetReference().(nodeRef); ok && ref.kind == kindChannel {
				list = append(list, ref.id)
			}
			if c.IsExpanded() {
				walk(c)
			}
		}
	}
	walk(n.root)
	if len(list) == 0 {
		return
	}
	idx := -1
	for i, id := range list {
		if id == n.a.current {
			idx = i
		}
	}
	switch {
	case idx < 0:
		idx = 0
	case down:
		idx = (idx + 1) % len(list)
	default:
		idx = (idx + len(list) - 1) % len(list)
	}
	n.a.open(list[idx])
}
