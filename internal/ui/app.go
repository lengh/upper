// Package ui is upper's full-screen terminal interface.
//
// The design borrows from two decades of IRC clients (WeeChat, irssi,
// catgirl, senpai): a quiet frame of one title bar and one status bar, a
// message column with right-aligned names and hanging indents, an activity
// "hotlist" so you can see what's waiting without looking at the sidebar,
// and a keyboard-first flow where the composer is home and every jump
// (Ctrl+K, Alt+A, Alt+/, Alt+1…9) brings you back there.
package ui

import (
	"context"
	"errors"
	"fmt"
	"slices"
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

// flash levels for status-bar messages.
const (
	levelInfo = iota
	levelOK
	levelWarn
	levelError
)

// App wires the gateway, REST client and state to the terminal UI.
//
// Threading model: gateway events are applied to the state on a dedicated
// goroutine, which only records what changed. A refresh loop coalesces
// those changes and redraws at most ~30 times per second, so a burst of
// hundreds of events costs one redraw. All widget state is owned by tview's
// event goroutine.
type App struct {
	cfg  config.Config
	th   *Theme
	st   *state.State
	rest *discord.REST
	gw   *discord.Gateway
	ctx  context.Context
	quit context.CancelFunc

	tv      *tview.Application
	pages   *tview.Pages
	root    *tview.Flex
	body    *tview.Flex
	compRow *tview.Flex
	title   *titleBar
	side    *sidebar
	view    *msgView
	status  *statusBar
	prompt  *promptView
	input   *tview.TextArea
	screen  tcell.Screen

	// UI-goroutine state.
	current    Snowflake
	previous   Snowflake
	drafts     map[Snowflake]string
	mode       composeMode
	target     *discord.Message // message being replied to, edited or reacted to
	comp       *completion
	completing bool // a completion is editing the composer
	loading    map[Snowflake]bool
	subscribed []Snowflake
	sidebarOn  bool
	narrowPin  bool // sidebar forced visible on a narrow terminal
	screenW    int
	lastTyping time.Time
	ackTimer   *time.Timer
	flashMsg   string
	flashLevel int
	flashUntil time.Time
	confirm    *confirmation
	conn       connState
	ready      bool
	focused    bool // terminal window has focus (assumed until told otherwise)
	lastCtrlC  time.Time
	hotlist    []state.ChannelInfo
	spin       int
	saved      uiState

	dirty dirtySet
	sendQ chan sendJob
	err   error // fatal gateway error, reported after the UI exits
}

type connState struct {
	ok      bool
	message string
}

// confirmation is an inline yes/no question in the status bar, used instead
// of a modal dialog so the conversation stays visible.
type confirmation struct {
	question string
	yes      func()
}

type dirtySet struct {
	mu       sync.Mutex
	tree     bool
	ready    bool
	channels map[Snowflake]bool
	typing   map[Snowflake]bool
	bell     bool
	status   *discord.Status
	current  Snowflake // mirror of App.current for the event goroutine
}

func New(cfg config.Config, st *state.State, rest *discord.REST, gw *discord.Gateway) *App {
	state.MaxMessages = cfg.MaxMessages
	state.MaxChannels = cfg.MaxChannels
	a := &App{
		cfg:       cfg,
		th:        loadTheme(cfg.Theme),
		st:        st,
		rest:      rest,
		gw:        gw,
		tv:        tview.NewApplication(),
		drafts:    map[Snowflake]string{},
		loading:   map[Snowflake]bool{},
		sidebarOn: true,
		focused:   true,
		sendQ:     make(chan sendJob, 64),
		conn:      connState{message: "connecting"},
	}
	a.dirty.channels = map[Snowflake]bool{}
	a.dirty.typing = map[Snowflake]bool{}
	a.saved = loadUIState()
	if a.saved.Sidebar != nil {
		a.sidebarOn = *a.saved.Sidebar
	}
	for id, d := range a.saved.Drafts {
		a.drafts[id] = d
	}
	a.build()
	return a
}

func (a *App) build() {
	// Rounded corners for the few framed overlays.
	tview.Borders.TopLeft, tview.Borders.TopRight = '╭', '╮'
	tview.Borders.BottomLeft, tview.Borders.BottomRight = '╰', '╯'
	tview.Borders.TopLeftFocus, tview.Borders.TopRightFocus = '╭', '╮'
	tview.Borders.BottomLeftFocus, tview.Borders.BottomRightFocus = '╰', '╯'
	tview.Borders.HorizontalFocus, tview.Borders.VerticalFocus = '─', '│'
	a.title = newTitleBar(a)
	a.side = newSidebar(a)
	a.view = newMsgView(a)
	a.status = newStatusBar(a)
	a.prompt = newPromptView(a)
	a.input = tview.NewTextArea().SetWordWrap(true)
	a.retheme()
	a.input.SetInputCapture(a.onComposerKey)
	a.input.SetChangedFunc(a.onComposerChanged)
	a.input.SetBorderPadding(0, 0, 0, 1)

	a.compRow = tview.NewFlex().
		AddItem(a.prompt, 3, 0, false).
		AddItem(a.input, 0, 1, true)

	a.body = tview.NewFlex().
		AddItem(a.side, a.sidebarWidth(80), 0, false).
		AddItem(a.view, 0, 1, false)

	a.root = tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(a.title, 1, 0, false).
		AddItem(a.body, 0, 1, false).
		AddItem(a.status, 1, 0, false).
		AddItem(a.compRow, 1, 0, true)

	a.pages = tview.NewPages().AddPage("main", a.root, true, true)
	a.tv.SetRoot(a.pages, true).SetFocus(a.input)
	a.tv.SetInputCapture(a.onGlobalKey)
	a.tv.SetBeforeDrawFunc(func(s tcell.Screen) bool {
		a.screen = s
		w, _ := s.Size()
		a.screenW = w
		a.body.ResizeItem(a.side, a.sidebarWidth(w), 0)
		a.prompt.fit()
		return false
	})
	a.updatePlaceholder()
}

// retheme applies the current theme to the stock widgets; the custom ones
// read it on every draw.
func (a *App) retheme() {
	th := a.th
	tview.Styles.PrimitiveBackgroundColor = th.Bg
	tview.Styles.ContrastBackgroundColor = th.Surface
	tview.Styles.BorderColor = th.Accent
	tview.Styles.TitleColor = th.Text
	tview.Styles.PrimaryTextColor = th.Text
	a.input.SetBackgroundColor(th.Bg)
	a.input.SetTextStyle(th.base())
	a.input.SetPlaceholderStyle(th.muted().Italic(true))
	a.input.SetSelectedStyle(th.surface(th.base()))
	if a.view != nil {
		clear(a.view.cache)
		a.view.dirty = true
	}
}

// sidebarWidth gives the sidebar its configured width, but never more than
// a third of a narrow terminal, and nothing when hidden.
func (a *App) sidebarWidth(screenW int) int {
	if !a.sidebarVisible(screenW) {
		return 0
	}
	return max(16, min(a.cfg.SidebarWidth, screenW/3))
}

// narrowWidth is the breakpoint below which the conversation gets the whole
// screen; the sidebar comes back with Ctrl+B or when the window widens.
const narrowWidth = 64

func (a *App) sidebarVisible(screenW int) bool {
	if screenW > 0 && screenW < narrowWidth {
		return a.narrowPin
	}
	return a.sidebarOn
}

// Run starts the gateway and blocks until the user quits.
func (a *App) Run(ctx context.Context) error {
	a.ctx, a.quit = context.WithCancel(ctx)
	defer a.quit()

	scr, err := tcell.NewScreen()
	if err != nil {
		return err
	}
	fs := &focusScreen{Screen: scr, onFocus: func(f bool) {
		a.tv.QueueUpdateDraw(func() { a.onTerminalFocus(f) })
	}}
	a.tv.SetScreen(fs)
	scr.EnableMouse(tcell.MouseButtonEvents)
	scr.EnablePaste()
	scr.EnableFocus()
	a.tv.EnableMouse(true).EnablePaste(true)
	a.tv.SetTitle("upper")

	go func() {
		err := a.gw.Run(a.ctx)
		if err != nil && !errors.Is(err, context.Canceled) {
			a.err = err
			a.tv.QueueUpdate(a.tv.Stop)
		}
	}()
	go a.consume()
	go a.refreshLoop()
	go a.sender()

	err = a.tv.Run()
	a.quit()
	a.persist()
	if a.err != nil {
		return a.err
	}
	return err
}

// focusScreen intercepts terminal focus reports, which tview ignores.
type focusScreen struct {
	tcell.Screen
	onFocus func(bool)
}

func (f *focusScreen) PollEvent() tcell.Event {
	for {
		ev := f.Screen.PollEvent()
		if fe, ok := ev.(*tcell.EventFocus); ok {
			f.onFocus(fe.Focused)
			continue
		}
		return ev
	}
}

// onTerminalFocus tracks whether the user is looking. Leaving moves the
// "new messages" marker to now, so on return everything that arrived while
// away is clearly marked, like WeeChat's read marker.
func (a *App) onTerminalFocus(focused bool) {
	a.focused = focused
	if !focused {
		a.view.markAway()
		return
	}
	a.scheduleAck()
	a.refreshTitle()
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
			a.dirty.tree = a.dirty.tree || c.Tree
			a.dirty.ready = a.dirty.ready || c.Ready
			if c.Channel != 0 {
				a.dirty.channels[c.Channel] = true
			}
			if c.Typing != 0 {
				a.dirty.typing[c.Typing] = true
			}
			if c.Mention && c.NewMsg != nil {
				a.dirty.bell = a.dirty.bell || !a.focused || c.NewMsg.ChannelID != a.currentID()
			}
			a.dirty.mu.Unlock()
		}
	}
}

// currentID is safe to call off the UI goroutine (a racy read of a word is
// acceptable for the bell heuristic, but keep it explicit).
func (a *App) currentID() Snowflake {
	a.dirty.mu.Lock()
	defer a.dirty.mu.Unlock()
	return a.dirty.current
}

func (a *App) refreshLoop() {
	fast := time.NewTicker(33 * time.Millisecond)
	slow := time.NewTicker(250 * time.Millisecond)
	defer fast.Stop()
	defer slow.Stop()
	treeDue := time.Time{}
	for {
		select {
		case <-a.ctx.Done():
			return
		case <-slow.C:
			// Spinners, typing expiry and the clock.
			a.tv.QueueUpdateDraw(func() { a.spin++ })
		case now := <-fast.C:
			a.dirty.mu.Lock()
			d := &a.dirty
			// The sidebar is costlier and less urgent than the open
			// channel: at most 4 refreshes per second.
			doTree := d.tree && now.After(treeDue)
			if doTree {
				d.tree = false
				treeDue = now.Add(250 * time.Millisecond)
			}
			if !doTree && !d.ready && len(d.channels) == 0 && len(d.typing) == 0 && !d.bell && d.status == nil {
				a.dirty.mu.Unlock()
				continue
			}
			ready, chans, bell, status := d.ready, d.channels, d.bell, d.status
			d.ready, d.bell, d.status = false, false, nil
			d.channels = map[Snowflake]bool{}
			d.typing = map[Snowflake]bool{}
			a.dirty.mu.Unlock()

			a.tv.QueueUpdateDraw(func() {
				if status != nil {
					a.conn = connState{ok: status.Connected, message: status.Message}
				}
				if ready {
					a.onReady()
				}
				if doTree || ready {
					a.refreshTree()
				}
				if chans[a.current] {
					a.view.reload()
					a.scheduleAck()
				}
				if bell && a.cfg.BellOnMention && a.screen != nil {
					_ = a.screen.Beep()
				}
			})
		}
	}
}

func (a *App) refreshTree() {
	a.side.refresh()
	a.hotlist = a.computeHotlist()
	a.refreshTitle()
}

// computeHotlist lists channels with activity, most urgent first: mentions
// and DMs, then plain unread, each by recency. Muted channels only appear
// when they mention you.
func (a *App) computeHotlist() []state.ChannelInfo {
	var out []state.ChannelInfo
	for _, c := range a.st.PrivateChannels() {
		if c.ID != a.current && (c.Unread || c.Mentions > 0) {
			out = append(out, c)
		}
	}
	for _, g := range a.st.Guilds() {
		if !g.Unread && g.Mentions == 0 {
			continue
		}
		for _, c := range a.st.GuildChannels(g.ID) {
			if c.ID != a.current && (c.Unread || c.Mentions > 0) {
				c.Category = g.Name
				out = append(out, c)
			}
		}
	}
	slices.SortStableFunc(out, func(x, y state.ChannelInfo) int {
		px, py := hotPriority(x), hotPriority(y)
		if px != py {
			return py - px
		}
		switch {
		case x.Last > y.Last:
			return -1
		case x.Last < y.Last:
			return 1
		}
		return 0
	})
	return out
}

func hotPriority(c state.ChannelInfo) int {
	switch {
	case c.Mentions > 0:
		return 2
	case c.Type.IsPrivate():
		return 1
	}
	return 0
}

// refreshTitle puts the mention count in the terminal tab title, the one
// place that's visible while you're in another window.
func (a *App) refreshTitle() {
	n := 0
	for _, c := range a.hotlist {
		n += c.Mentions
	}
	// ASCII only: some terminals and multiplexers mangle other titles.
	title := "upper"
	if ch, ok := a.st.Channel(a.current); ok {
		name := "#" + ch.Name
		if ch.Type.IsPrivate() {
			name = strings.TrimPrefix(a.st.ChannelTitle(a.current), "@")
		}
		title = name + " - upper"
	}
	if n > 0 {
		title = fmt.Sprintf("(%d) %s", n, title)
	}
	a.tv.SetTitle(title)
}

func (a *App) onReady() {
	first := !a.ready
	a.ready = true
	// A fresh session forgets subscriptions; re-subscribe recent ones.
	a.gw.Subscribe(a.subscribed)
	if first {
		// Every launch starts closed: servers folded, no channel open,
		// just the overview. Where you were is one Alt+/ away.
		for _, g := range a.st.Guilds() {
			a.side.collapsed[g.ID] = true
		}
		if _, ok := a.st.Channel(a.saved.LastChannel); ok {
			a.previous = a.saved.LastChannel
		}
	}
	if a.current != 0 {
		if _, ok := a.st.Channel(a.current); !ok {
			a.current = 0
			a.view.setChannel(0)
		} else {
			a.loadHistory(a.current)
		}
	}
	a.view.reload()
}

// ---- Status messages ---------------------------------------------------------

func (a *App) flash(msg string) { a.flashAt(levelInfo, msg) }

func (a *App) flashAt(level int, msg string) {
	a.flashMsg, a.flashLevel = msg, level
	a.flashUntil = time.Now().Add(4 * time.Second)
	if level == levelError {
		a.flashUntil = time.Now().Add(8 * time.Second)
	}
}

func (a *App) flashErr(err error) { a.flashAt(levelError, humanError(err)) }

// humanError rewrites API errors into something a person can act on.
func humanError(err error) string {
	var he *discord.HTTPError
	if errors.As(err, &he) {
		switch he.Code {
		case 50013:
			return "you don't have permission to do that here"
		case 50001:
			return "you don't have access to that channel"
		case 50007:
			return "that user doesn't accept DMs from you"
		case 20016, 20028:
			return "slow mode is on in this channel; wait a moment before sending"
		case 50035:
			return "Discord rejected the message (it may be too long or empty)"
		case 10008:
			return "that message no longer exists"
		case 40002:
			return "Discord wants you to verify your account in the official app first"
		}
		switch he.Status {
		case 401:
			return "your session expired; restart upper to log in again"
		case 403:
			return "Discord refused that action"
		case 404:
			return "not found; it may have been deleted"
		case 429:
			return "slow down: Discord is rate limiting you"
		}
		if he.Status >= 500 {
			return "Discord is having trouble; try again in a moment"
		}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "request timed out; check your connection"
	}
	return err.Error()
}

// ask shows an inline yes/no question. Any key other than y cancels.
func (a *App) ask(question string, yes func()) {
	a.confirm = &confirmation{question: question, yes: yes}
}

// ---- Focus -------------------------------------------------------------------

func (a *App) focus(p tview.Primitive) {
	if p != tview.Primitive(a.view) {
		a.view.clearSelection()
	}
	a.tv.SetFocus(p)
}

func (a *App) focusComposer() { a.focus(a.input) }

func (a *App) cycleFocus(back bool) {
	order := []tview.Primitive{a.input, a.side, a.view}
	if !a.sidebarVisible(a.screenW) {
		order = []tview.Primitive{a.input, a.view}
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
	if order[i] == tview.Primitive(a.view) {
		a.view.selectLast()
	}
	a.focus(order[i])
}

func (a *App) toggleSidebar() {
	if a.screenW > 0 && a.screenW < narrowWidth {
		a.narrowPin = !a.narrowPin
	} else {
		a.sidebarOn = !a.sidebarOn
		a.saved.Sidebar = &a.sidebarOn
	}
	if !a.sidebarVisible(a.screenW) && a.tv.GetFocus() == tview.Primitive(a.side) {
		a.focusComposer()
	}
}

func (a *App) overlayOpen() bool {
	name, _ := a.pages.GetFrontPage()
	return name != "main"
}

// onGlobalKey handles keys that work everywhere. Conventions follow IRC
// clients where they exist (Alt+A activity, Alt+/ last window, Alt+1…9) and
// Discord where they don't (Ctrl+K).
func (a *App) onGlobalKey(ev *tcell.EventKey) *tcell.EventKey {
	if a.overlayOpen() {
		return ev
	}
	if a.confirm != nil {
		c := a.confirm
		a.confirm = nil
		if ev.Key() == tcell.KeyRune && (ev.Rune() == 'y' || ev.Rune() == 'Y') {
			c.yes()
		} else {
			a.flash("cancelled")
		}
		return nil
	}

	alt := ev.Modifiers()&tcell.ModAlt != 0
	switch ev.Key() {
	case tcell.KeyCtrlC:
		// Windows habits make Ctrl+C a copy reflex: clear the composer
		// first and only quit on a deliberate second press.
		if a.input.GetText() != "" && a.tv.GetFocus() == tview.Primitive(a.input) {
			a.input.SetText("", false)
			a.resetCompose()
			return nil
		}
		if time.Since(a.lastCtrlC) < 1500*time.Millisecond {
			a.tv.Stop()
			return nil
		}
		a.lastCtrlC = time.Now()
		a.flashAt(levelWarn, "press Ctrl+C again to quit")
		return nil
	case tcell.KeyCtrlQ:
		a.tv.Stop()
		return nil
	case tcell.KeyCtrlK:
		a.openSwitcher("")
		return nil
	case tcell.KeyCtrlF:
		if a.mode != modeSearch {
			a.resetCompose()
			a.startSearch()
			return nil
		}
	case tcell.KeyCtrlB, tcell.KeyF2:
		a.toggleSidebar()
		return nil
	case tcell.KeyF1:
		a.showHelp()
		return nil
	case tcell.KeyCtrlL:
		a.tv.Sync()
		return nil
	case tcell.KeyCtrlN:
		a.side.step(1)
		return nil
	case tcell.KeyCtrlP:
		a.side.step(-1)
		return nil
	case tcell.KeyUp, tcell.KeyDown, tcell.KeyLeft, tcell.KeyRight:
		if alt {
			if ev.Key() == tcell.KeyUp || ev.Key() == tcell.KeyLeft {
				a.side.step(-1)
			} else {
				a.side.step(1)
			}
			return nil
		}
	case tcell.KeyHome, tcell.KeyEnd:
		if alt {
			if ev.Key() == tcell.KeyHome {
				a.view.scrollTop()
			} else {
				a.view.scrollBottom()
			}
			return nil
		}
	case tcell.KeyRune:
		if !alt {
			break
		}
		switch r := ev.Rune(); {
		case r == 'a' || r == 'A':
			a.nextActivity()
		case r == 'r' || r == 'R':
			a.startHints(hintReply)
		case r == 'e' || r == 'E':
			a.startHints(hintReact)
		case r == 'o' || r == 'O':
			a.startHints(hintOpen)
		case r == 'y' || r == 'Y':
			a.startHints(hintCopy)
		case r == '/':
			a.togglePrevious()
		case r == 'u' || r == 'U':
			a.view.scrollToUnread()
		case r == 'n' || r == 'N':
			a.view.jumpHighlight(1)
		case r == 'p' || r == 'P':
			a.view.jumpHighlight(-1)
		case r == '<':
			a.view.scrollTop()
		case r == '>':
			a.view.scrollBottom()
		case r >= '1' && r <= '9':
			if i := int(r - '1'); i < len(a.hotlist) {
				a.open(a.hotlist[i].ID)
			}
		default:
			return ev
		}
		return nil
	}
	return ev
}

func (a *App) nextActivity() {
	if len(a.hotlist) > 0 {
		a.open(a.hotlist[0].ID)
		return
	}
	a.flash("nothing unread · you're all caught up")
}

func (a *App) togglePrevious() {
	if a.previous != 0 {
		if _, ok := a.st.Channel(a.previous); ok {
			a.open(a.previous)
			return
		}
	}
	a.flash("no previous channel yet")
}

// ---- Opening channels --------------------------------------------------------

// open switches to a channel. The composer keeps a draft per channel, so
// switching away mid-sentence loses nothing.
func (a *App) open(id Snowflake) {
	ch, ok := a.st.Channel(id)
	if !ok || !ch.Type.IsText() {
		return
	}
	if a.current != id {
		// Leave reply/edit/react first so only real drafts are kept.
		a.resetCompose()
		if a.current != 0 {
			if text := a.input.GetText(); text != "" {
				a.drafts[a.current] = text
			} else {
				delete(a.drafts, a.current)
			}
			a.previous = a.current
		}
		a.input.SetText(a.drafts[id], true)
		delete(a.drafts, id)
	}
	a.current = id
	a.dirty.mu.Lock()
	a.dirty.current = id
	a.dirty.mu.Unlock()
	a.saved.LastChannel, a.saved.PrevChannel = id, a.previous

	if ch.GuildID != 0 && a.st.IsLarge(ch.GuildID) {
		a.subscribe(ch.GuildID)
	}
	a.view.setChannel(id)
	if !a.st.HistoryLoaded(id) {
		a.loadHistory(id)
	}
	a.side.reveal(id)
	a.refreshTree()
	a.updatePlaceholder()
	a.scheduleAck()
	a.focusComposer()
}

// subscribe asks the gateway to stream a large guild, keeping the ten most
// recently opened to bound the event volume.
func (a *App) subscribe(gid Snowflake) {
	if i := slices.Index(a.subscribed, gid); i >= 0 {
		a.subscribed = append(slices.Delete(a.subscribed, i, i+1), gid)
		return
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
				a.flashErr(err)
				if a.current == id {
					a.view.loadErr = humanError(err)
				}
			}
			if a.current == id {
				a.view.reload()
				a.scheduleAck()
			}
		})
	}()
}

func (a *App) loadOlder() {
	id := a.current
	if id == 0 || a.loading[id] || !a.st.HistoryLoaded(id) {
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
			if a.current == id {
				a.view.reload()
			}
		})
	}()
}

// scheduleAck marks the open channel read once activity settles, but only
// while you can actually see the newest messages: the terminal is focused
// and the view isn't scrolled back. Acks are batched into one request.
func (a *App) scheduleAck() {
	if !a.cfg.MarkRead || a.current == 0 || !a.focused || a.view.scroll > 0 {
		return
	}
	id := a.current
	if a.ackTimer != nil {
		a.ackTimer.Stop()
	}
	a.ackTimer = time.AfterFunc(1200*time.Millisecond, func() {
		a.tv.QueueUpdateDraw(func() {
			if a.current != id || !a.focused || a.view.scroll > 0 {
				return
			}
			msg := a.st.MarkRead(id)
			if msg == 0 {
				return
			}
			a.refreshTree()
			go func() {
				ctx, cancel := context.WithTimeout(a.ctx, 15*time.Second)
				defer cancel()
				_ = a.rest.Ack(ctx, id, msg)
			}()
		})
	})
}

func (a *App) persist() {
	if a.current != 0 {
		if text := a.input.GetText(); text != "" {
			a.drafts[a.current] = text
		}
	}
	a.saved.Drafts = a.drafts
	saveUIState(a.saved)
}
