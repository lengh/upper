// Package ui is upper's full-screen terminal interface.
package ui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/lengh/upper/internal/config"
	"github.com/lengh/upper/internal/discord"
	"github.com/lengh/upper/internal/state"
	"github.com/rivo/tview"
)

type Snowflake = discord.Snowflake

type theme struct {
	border, accent, timestamp, muted, mention, unread, selected, errc, code string
}

func newTheme(t config.Theme) theme {
	return theme{
		border: t.Border, accent: t.Accent, timestamp: t.Timestamp, muted: t.Muted,
		mention: t.Mention, unread: t.Unread, selected: t.Selected, errc: t.Error,
		code: t.Muted,
	}
}

// App wires the gateway, REST client and state to the terminal UI.
//
// Threading model: gateway events are applied to the state on a dedicated
// goroutine, which only records what changed. A refresh loop then coalesces
// those changes and redraws at most ~30 times per second, so a burst of
// hundreds of events costs one redraw, not hundreds. All widget access
// happens on tview's event goroutine.
type App struct {
	cfg  config.Config
	th   theme
	st   *state.State
	rest *discord.REST
	gw   *discord.Gateway
	ctx  context.Context
	quit context.CancelFunc

	tv       *tview.Application
	pages    *tview.Pages
	body     *tview.Flex
	right    *tview.Flex
	tree     *tview.TreeView
	header   *tview.TextView
	msgs     *tview.TextView
	typingTV *tview.TextView
	input    *tview.TextArea
	status   *tview.TextView
	screen   tcell.Screen

	// UI-goroutine state.
	current      Snowflake
	newSince     Snowflake // last read message when the channel was opened
	replyTo      *discord.Message
	editing      *discord.Message
	selected     string // region ID of the selected message
	order        []string
	byRegion     map[string]discord.Message
	loading      map[Snowflake]bool
	subscribed   []Snowflake // large guilds we subscribed to, most recent last
	sidebar      bool
	lastTyping   time.Time
	ackTimer     *time.Timer
	statusMsg    string
	statusUntil  time.Time
	connected    string
	nav          navState
	ready        bool
	gatewayError error

	dirty dirtySet
	sendQ chan sendJob
}

type dirtySet struct {
	mu       sync.Mutex
	tree     bool
	ready    bool
	channels map[Snowflake]bool
	typing   map[Snowflake]bool
	bell     bool
	status   *discord.Status
}

func New(cfg config.Config, st *state.State, rest *discord.REST, gw *discord.Gateway) *App {
	state.MaxMessages = cfg.MaxMessages
	state.MaxChannels = cfg.MaxChannels
	a := &App{
		cfg:      cfg,
		th:       newTheme(cfg.Theme),
		st:       st,
		rest:     rest,
		gw:       gw,
		tv:       tview.NewApplication(),
		byRegion: map[string]discord.Message{},
		loading:  map[Snowflake]bool{},
		sidebar:  true,
		sendQ:    make(chan sendJob, 64),
	}
	a.dirty.channels = map[Snowflake]bool{}
	a.dirty.typing = map[Snowflake]bool{}
	a.build()
	return a
}

func color(s string) tcell.Color { return tcell.GetColor(s) }

func (a *App) build() {
	tview.Styles.PrimitiveBackgroundColor = tcell.ColorDefault
	tview.Styles.ContrastBackgroundColor = color(a.th.selected)
	tview.Styles.BorderColor = color(a.th.border)
	tview.Styles.TitleColor = tcell.ColorWhite

	a.tree = tview.NewTreeView()
	a.tree.SetBorder(true).SetTitle(" servers ").SetTitleAlign(tview.AlignLeft)
	a.tree.SetGraphics(false).SetTopLevel(1)
	a.tree.SetSelectedFunc(a.onTreeSelect)
	a.nav.init(a)

	a.header = tview.NewTextView().SetDynamicColors(true).SetWrap(false)
	a.header.SetBorderPadding(0, 0, 1, 1)

	a.msgs = tview.NewTextView().
		SetDynamicColors(true).
		SetRegions(true).
		SetWrap(true).
		SetWordWrap(true)
	a.msgs.SetBorder(true).SetBorderPadding(0, 0, 1, 1)
	a.msgs.SetInputCapture(a.onMessagesKey)
	a.msgs.SetMouseCapture(func(action tview.MouseAction, ev *tcell.EventMouse) (tview.MouseAction, *tcell.EventMouse) {
		if action == tview.MouseScrollUp {
			if row, _ := a.msgs.GetScrollOffset(); row == 0 {
				a.loadOlder()
			}
		}
		return action, ev
	})

	a.typingTV = tview.NewTextView().SetDynamicColors(true).SetWrap(false)
	a.typingTV.SetBorderPadding(0, 0, 1, 1)

	a.input = tview.NewTextArea().SetWordWrap(true)
	a.input.SetBorder(true).SetBorderPadding(0, 0, 1, 1)
	a.input.SetPlaceholder("Pick a channel on the left, or press Ctrl+K to search")
	a.input.SetPlaceholderStyle(tcell.StyleDefault.Foreground(color(a.th.muted)))
	a.input.SetInputCapture(a.onInputKey)
	a.input.SetChangedFunc(a.onInputChanged)

	a.status = tview.NewTextView().SetDynamicColors(true).SetWrap(false)

	a.right = tview.NewFlex().SetDirection(tview.FlexRow)
	right := a.right.
		AddItem(a.header, 1, 0, false).
		AddItem(a.msgs, 0, 1, false).
		AddItem(a.typingTV, 1, 0, false).
		AddItem(a.input, 3, 0, true)

	a.body = tview.NewFlex().
		AddItem(a.tree, a.cfg.SidebarWidth, 0, false).
		AddItem(right, 0, 1, true)

	root := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(a.body, 0, 1, true).
		AddItem(a.status, 1, 0, false)

	a.pages = tview.NewPages().AddPage("main", root, true, true)
	a.tv.SetRoot(a.pages, true).SetFocus(a.tree)
	a.tv.EnableMouse(true)
	// Bracketed paste: multi-line pastes land in the composer instead of
	// sending one message per line.
	a.tv.EnablePaste(true)
	a.tv.SetInputCapture(a.onGlobalKey)
	a.tv.SetBeforeDrawFunc(func(s tcell.Screen) bool {
		a.screen = s
		// Give the sidebar at most a third of narrow terminals.
		if a.sidebar {
			w, _ := s.Size()
			a.body.ResizeItem(a.tree, max(16, min(a.cfg.SidebarWidth, w/3)), 0)
		}
		return false
	})
	a.focusStyle()
	a.renderHeader()
	a.connected = "connecting…"
	a.renderStatus()
}

// Run starts the gateway and blocks until the user quits. It returns the
// gateway error if the session ended because of one (e.g. a revoked token).
func (a *App) Run(ctx context.Context) error {
	a.ctx, a.quit = context.WithCancel(ctx)
	defer a.quit()

	go func() {
		err := a.gw.Run(a.ctx)
		if err != nil && !errors.Is(err, context.Canceled) {
			a.gatewayError = err
			// Queued so it also works if the UI hasn't started yet.
			a.tv.QueueUpdate(a.tv.Stop)
		}
	}()
	go a.consume()
	go a.refreshLoop()
	go a.sender()

	err := a.tv.Run()
	a.quit()
	if a.gatewayError != nil {
		return a.gatewayError
	}
	return err
}

// consume applies gateway events to the state and records what changed.
func (a *App) consume() {
	for {
		select {
		case <-a.ctx.Done():
			return
		case s := <-a.gw.Status:
			a.dirty.mu.Lock()
			a.dirty.status = &s
			a.dirty.mu.Unlock()
		case ev := <-a.gw.Events:
			c, err := a.st.Apply(ev)
			if err != nil {
				continue
			}
			a.dirty.mu.Lock()
			if c.Tree {
				a.dirty.tree = true
			}
			if c.Ready {
				a.dirty.ready = true
			}
			if c.Channel != 0 {
				a.dirty.channels[c.Channel] = true
			}
			if c.Typing != 0 {
				a.dirty.typing[c.Typing] = true
			}
			if c.Mention && c.NewMsg != nil {
				a.dirty.bell = true
			}
			a.dirty.mu.Unlock()
		}
	}
}

func (a *App) refreshLoop() {
	fast := time.NewTicker(33 * time.Millisecond)
	slow := time.NewTicker(time.Second)
	defer fast.Stop()
	defer slow.Stop()
	treeDue := time.Time{}
	for {
		select {
		case <-a.ctx.Done():
			return
		case <-slow.C:
			// Typing indicators expire and relative times drift.
			a.tv.QueueUpdateDraw(func() {
				a.renderTyping()
				a.renderStatus()
			})
		case now := <-fast.C:
			a.dirty.mu.Lock()
			d := &a.dirty
			// The sidebar is costlier to rebuild and less urgent than the
			// open channel, so it refreshes at most 4 times per second.
			doTree := d.tree && now.After(treeDue)
			if doTree {
				a.dirty.tree = false
				treeDue = now.Add(250 * time.Millisecond)
			}
			hasWork := doTree || d.ready || len(d.channels) > 0 || len(d.typing) > 0 || d.bell || d.status != nil
			if !hasWork {
				a.dirty.mu.Unlock()
				continue
			}
			ready, chans, typing, bell, status := d.ready, d.channels, d.typing, d.bell, d.status
			a.dirty.ready, a.dirty.bell, a.dirty.status = false, false, nil
			a.dirty.channels = map[Snowflake]bool{}
			a.dirty.typing = map[Snowflake]bool{}
			a.dirty.mu.Unlock()

			a.tv.QueueUpdateDraw(func() {
				if status != nil {
					a.connected = status.Message
					a.renderStatus()
				}
				if ready {
					a.onReady()
				}
				if doTree || ready {
					a.nav.refresh()
				}
				if chans[a.current] {
					a.renderMessages()
					a.scheduleAck()
				}
				if typing[a.current] {
					a.renderTyping()
				}
				if bell && a.cfg.BellOnMention && a.screen != nil {
					_ = a.screen.Beep()
				}
			})
		}
	}
}

func (a *App) onReady() {
	a.ready = true
	me := a.st.Me()
	a.input.SetPlaceholder("Pick a channel on the left, or press Ctrl+K to search")
	// A fresh session forgets subscriptions; re-subscribe recent ones.
	a.gw.Subscribe(a.subscribed)
	if a.current != 0 {
		if _, ok := a.st.Channel(a.current); !ok {
			a.current = 0
			a.renderHeader()
			a.msgs.SetText("")
		} else {
			a.loadHistory(a.current)
		}
	}
	a.flash(fmt.Sprintf("logged in as %s", me.Tag()))
}

// ---- Status and header -------------------------------------------------------

func (a *App) flash(msg string) {
	a.statusMsg = msg
	a.statusUntil = time.Now().Add(5 * time.Second)
	a.renderStatus()
}

func (a *App) flashErr(err error) {
	a.flash("[" + a.th.errc + "]" + tview.Escape(err.Error()) + "[-]")
}

func (a *App) renderStatus() {
	var b strings.Builder
	b.WriteString(" [" + a.th.accent + "::b]upper[-::B] ")
	b.WriteString("[" + a.th.muted + "]" + tview.Escape(a.connected) + "[-]")
	if a.statusMsg != "" && time.Now().Before(a.statusUntil) {
		b.WriteString("  " + a.statusMsg)
	} else {
		b.WriteString("  [" + a.th.muted + "]Ctrl+K switch · Alt+U next unread · Tab focus · F1 help · Ctrl+C quit[-]")
	}
	a.status.SetText(b.String())
}

func (a *App) renderHeader() {
	if a.current == 0 {
		a.header.SetText("[" + a.th.muted + "]no channel open[-]")
		return
	}
	ch, _ := a.st.Channel(a.current)
	text := "[::b]" + tview.Escape(a.st.ChannelTitle(a.current)) + "[::B]"
	if ch.Topic != "" {
		topic := strings.ReplaceAll(ch.Topic, "\n", " ")
		text += "  [" + a.th.muted + "]" + tview.Escape(topic) + "[-]"
	}
	a.header.SetText(text)
}

func (a *App) renderTyping() {
	if a.current == 0 {
		a.typingTV.SetText("")
		return
	}
	names := a.st.Typing(a.current)
	var s string
	switch len(names) {
	case 0:
	case 1:
		s = names[0] + " is typing…"
	case 2:
		s = names[0] + " and " + names[1] + " are typing…"
	case 3:
		s = names[0] + ", " + names[1] + " and " + names[2] + " are typing…"
	default:
		s = "several people are typing…"
	}
	switch {
	case a.editing != nil:
		s = "editing message · Esc to cancel   " + s
	case a.replyTo != nil:
		author := a.st.Author(a.replyTo.GuildID, a.replyTo.Author).Name
		s = "replying to " + author + " · Esc to cancel   " + s
	}
	a.typingTV.SetText("[" + a.th.muted + "]" + tview.Escape(s) + "[-]")
}

// ---- Focus -------------------------------------------------------------------

func (a *App) focusStyle() {
	for _, b := range []*tview.Box{a.tree.Box, a.msgs.Box, a.input.Box} {
		b.SetBorderColor(color(a.th.muted))
	}
	switch a.tv.GetFocus() {
	case a.tree:
		a.tree.SetBorderColor(color(a.th.border))
	case a.msgs:
		a.msgs.SetBorderColor(color(a.th.border))
	case a.input:
		a.input.SetBorderColor(color(a.th.border))
	}
}

func (a *App) focus(p tview.Primitive) {
	if p != a.msgs && a.selected != "" {
		a.selected = ""
		a.msgs.Highlight()
	}
	a.tv.SetFocus(p)
	a.focusStyle()
}

func (a *App) cycleFocus(back bool) {
	order := []tview.Primitive{a.tree, a.msgs, a.input}
	if !a.sidebar {
		order = order[1:]
	}
	cur := a.tv.GetFocus()
	i := 0
	for j, p := range order {
		if p == cur {
			i = j
		}
	}
	if back {
		i = (i + len(order) - 1) % len(order)
	} else {
		i = (i + 1) % len(order)
	}
	if order[i] == a.msgs {
		a.selectLast()
	}
	a.focus(order[i])
}

func (a *App) toggleSidebar() {
	a.sidebar = !a.sidebar
	if a.sidebar {
		a.body.ResizeItem(a.tree, a.cfg.SidebarWidth, 0)
	} else {
		a.body.ResizeItem(a.tree, 0, 0)
		if a.tv.GetFocus() == a.tree {
			a.focus(a.input)
		}
	}
}

func (a *App) onGlobalKey(ev *tcell.EventKey) *tcell.EventKey {
	if name, _ := a.pages.GetFrontPage(); name != "main" {
		return ev
	}
	switch {
	case ev.Key() == tcell.KeyCtrlK:
		a.openSwitcher()
		return nil
	case ev.Key() == tcell.KeyCtrlB:
		a.toggleSidebar()
		return nil
	case ev.Key() == tcell.KeyF1:
		a.showHelp()
		return nil
	case ev.Key() == tcell.KeyCtrlQ:
		a.tv.Stop()
		return nil
	case ev.Key() == tcell.KeyTab:
		a.cycleFocus(false)
		return nil
	case ev.Key() == tcell.KeyBacktab:
		a.cycleFocus(true)
		return nil
	case ev.Modifiers()&tcell.ModAlt != 0 && ev.Key() == tcell.KeyRune && (ev.Rune() == 'u' || ev.Rune() == 'U'):
		if id, ok := a.st.NextUnread(a.current); ok {
			a.open(id)
		} else {
			a.flash("no unread channels")
		}
		return nil
	case ev.Modifiers()&tcell.ModAlt != 0 && (ev.Key() == tcell.KeyUp || ev.Key() == tcell.KeyDown):
		a.nav.step(ev.Key() == tcell.KeyDown)
		return nil
	}
	return ev
}

// ---- Opening channels --------------------------------------------------------

// open switches the view to a channel, fetching history if needed.
func (a *App) open(id Snowflake) {
	ch, ok := a.st.Channel(id)
	if !ok || !ch.Type.IsText() {
		return
	}
	if a.current != id {
		a.cancelCompose()
		a.input.SetText("", false)
	}
	a.current = id
	a.selected = ""
	a.newSince = a.st.LastRead(id)
	if ch.GuildID != 0 && a.st.IsLarge(ch.GuildID) {
		a.subscribe(ch.GuildID)
	}
	a.renderHeader()
	if a.st.CanSend(id) {
		a.input.SetPlaceholder("Message " + a.st.ChannelTitle(id) + " — Enter send · Ctrl+J newline")
	} else {
		a.input.SetPlaceholder("You can't send messages in this channel")
	}
	if !a.st.HistoryLoaded(id) {
		a.msgs.SetText("[" + a.th.muted + "]loading…[-]")
		a.loadHistory(id)
	} else {
		a.renderMessages()
	}
	a.msgs.ScrollToEnd()
	a.renderTyping()
	a.nav.markCurrent(id)
	a.scheduleAck()
	a.focus(a.input)
}

// subscribe asks the gateway to stream a large guild. Only the most recent
// guilds are kept to bound the event volume.
func (a *App) subscribe(gid Snowflake) {
	for i, g := range a.subscribed {
		if g == gid {
			a.subscribed = append(a.subscribed[:i], a.subscribed[i+1:]...)
			a.subscribed = append(a.subscribed, gid)
			return
		}
	}
	a.subscribed = append(a.subscribed, gid)
	if len(a.subscribed) > 10 {
		a.subscribed = a.subscribed[len(a.subscribed)-10:]
	}
	a.gw.Subscribe([]Snowflake{gid})
}

func (a *App) loadHistory(id Snowflake) {
	if a.loading[id] {
		return
	}
	a.loading[id] = true
	limit := a.cfg.HistoryPage
	go func() {
		ctx, cancel := context.WithTimeout(a.ctx, 30*time.Second)
		defer cancel()
		page, err := a.rest.Messages(ctx, id, 0, limit)
		if err == nil {
			a.st.SetHistory(id, page, limit)
		}
		a.tv.QueueUpdateDraw(func() {
			delete(a.loading, id)
			if err != nil {
				a.flashErr(fmt.Errorf("loading messages: %w", err))
				if a.current == id {
					a.msgs.SetText("[" + a.th.errc + "]could not load messages: " + tview.Escape(err.Error()) + "[-]")
				}
				return
			}
			if a.current == id {
				a.renderMessages()
				a.msgs.ScrollToEnd()
			}
		})
	}()
}

func (a *App) loadOlder() {
	id := a.current
	if id == 0 || a.loading[id] {
		return
	}
	if _, more := a.st.Messages(id); !more {
		return
	}
	before := a.st.Oldest(id)
	if before == 0 {
		return
	}
	a.loading[id] = true
	a.flash("loading older messages…")
	limit := a.cfg.HistoryPage
	go func() {
		ctx, cancel := context.WithTimeout(a.ctx, 30*time.Second)
		defer cancel()
		page, err := a.rest.Messages(ctx, id, before, limit)
		if err == nil {
			a.st.PrependHistory(id, page, limit)
		}
		a.tv.QueueUpdateDraw(func() {
			delete(a.loading, id)
			if err != nil {
				a.flashErr(err)
				return
			}
			a.statusMsg = ""
			a.renderStatus()
			if a.current == id {
				anchor := a.selected
				if anchor == "" {
					anchor = before.String()
				}
				a.renderMessages()
				a.msgs.Highlight(anchor)
				a.msgs.ScrollToHighlight()
				if a.tv.GetFocus() == a.msgs {
					a.selected = anchor
				} else {
					a.msgs.Highlight()
				}
			}
		})
	}()
}

// scheduleAck marks the open channel read shortly after activity settles,
// batching acks for busy channels into one request.
func (a *App) scheduleAck() {
	if !a.cfg.MarkRead || a.current == 0 {
		return
	}
	id := a.current
	if a.ackTimer != nil {
		a.ackTimer.Stop()
	}
	a.ackTimer = time.AfterFunc(1500*time.Millisecond, func() {
		a.tv.QueueUpdateDraw(func() {
			if a.current != id {
				return
			}
			msg := a.st.MarkRead(id)
			if msg == 0 {
				return
			}
			a.nav.refresh()
			go func() {
				ctx, cancel := context.WithTimeout(a.ctx, 15*time.Second)
				defer cancel()
				_ = a.rest.Ack(ctx, id, msg)
			}()
		})
	})
}
