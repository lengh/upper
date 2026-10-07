# upper

A fast, keyboard-driven Discord client for the terminal, made for WSL.
It does text chat only: servers, channels, threads you've joined and direct
messages. There are no voice calls, images or file uploads.

```
 # general  Everything Go · be kind · no recruiters                                                Gophers  ●
 DIRECT MESSAGES           │ 11:31  bobby │ anyone else still on 1.22? thinking about bumping the toolchain
• ◐ Bob                 1  │ 11:33  Alice │ we moved to 1.24 last week, iterators alone were worth it
  ● Alice                  │ 12:31      → │ dave joined the server
  ◇ weekend plans          │ 12:32   dave │ hi all 👋 found this place through the meetup
                           │ 12:33  Carol │ welcome dave! grab a sticker from #welcome
 SERVERS                   │ ──────────────────────────────────── Today ─────────────────────────────────────
 ▾ Gophers                 │ 11:31    You │ morning! did anyone figure out the flaky CI job?
   TEXT                    │ 11:32  bobby │ it's the race in the cache warmer. repro:
   » welcome               │ 11:33  bobby │  for i := range workers {
▌  # general               │              │      go warm(cache, i) // shares buf!
     ↳ release-planning    │              │  }
   # help                  │ 11:33  Alice │ oh nice catch. go test -race flags it immediately too
   # off-topic             │              │  🎯 3   👀 1
 ▸ Synthwave Café        • │
                           │              │ ╭─ Alice oh nice catch. go test -race flags it immediately too
                           │ 11:34    You │ I'll send a patch after lunch (edited)
                           │ 12:31  Carol │ related reading for anyone curious:
                           │              │ ▍ go.dev
                           │              │ ▍ Data Race Detector
                           │              │ ▍ Data races are among the most common and hardest to debug types
                           │              │ ▍ of bugs in concurrent systems.
                           │ 12:32   dave │ thanks, reading now
                           │ ────────────────────────────────────────────────────────────────────────── new ─
                           │ 14:06  Alice ┃ @You patch looks good, approved ✅ merging when CI is green
                           │ 14:11  bobby │ release notes are drafted in the thread, please skim them before
                           │              │ friday ░░░░░░░░░░░░░░░
                           │              │  🚀 4
 14:31 │ 1:Bob(1)  2:#lounge
 ❯ Message #general
```

> [!WARNING]
> upper logs in with your **personal account token**. Discord's Terms of
> Service don't allow third-party clients or automated user accounts, and
> accounts have occasionally been flagged or banned for using them. upper
> keeps the risk low: it identifies itself honestly, sends nothing in the
> background, and stays well inside rate limits. The risk is still not zero,
> so use it at your own discretion.

## Install (WSL / Linux)

In your Ubuntu (WSL) terminal:

```sh
curl -fsSL https://lengh.github.io/upper/install.sh | sh
```

The installer picks the right build (x86-64 or ARM64), verifies its SHA-256
checksum, installs it to `~/.local/bin` and adds that to your PATH if needed.
Running it again is safe: it finds your existing install (wherever it is on
your PATH), reports *Up to date* if you already have the newest build, and
otherwise updates that copy in place.

**Updates are automatic.** Every launch checks for a newer build (quickly:
it gives up after 3 seconds if you're offline). If there is one, upper
downloads it, verifies its checksum, swaps it in atomically and exits with
*Run upper again*. The next launch starts the new version. `upper --update`
updates without launching, `/update` does it from inside the app, and
`upper --no-update` (or `UPPER_NO_UPDATE=1`) skips the check. Builds and the download page live at
<https://lengh.github.io/upper>, and the
[pages workflow](.github/workflows/pages.yml) rebuilds them on every push.

To build from source instead, you need Go 1.21 or newer, which automatically
downloads the exact toolchain the project pins:

```sh
git clone https://github.com/lengh/upper && cd upper
go build -o ~/.local/bin/upper ./cmd/upper
```

**Try it first without an account:** `upper --demo` starts the interface
against a built-in fake server, with no Discord connection at all.

### First login

When upper starts for the first time, it asks for your token and checks it.
It then offers to save the token to `~/.config/upper/token`, readable only by
you (mode 0600). You can also pass it through the `UPPER_TOKEN` environment
variable.

To find your token:

1. Open <https://discord.com/app> in a browser and log in.
2. Press F12, open the **Network** tab and type `api` in the filter box.
3. Click any channel, select a request and copy the value of its
   **Authorization** request header.

Treat the token like a password: it gives full access to your account.
Changing your password resets it. `upper --logout` (or `/logout`) deletes
the stored copy.

## Using it

The sidebar is an inbox. **Direct messages** lists only conversations with
something new, plus the one that's open; every other DM is a `Ctrl+K` away.
**Servers** list their channels in Discord's order. Every launch starts
closed: servers folded and no channel open, just an overview of what's
waiting under a cat grooming its paw. `Alt+/` returns to where you left off,
and `→`/`Enter` unfolds a server. When there's more unread than fits on
screen, a channel opens at the first unread message so you read in order.
Below 64 columns, the conversation gets the whole width (`Ctrl+B` brings the
sidebar back).

The composer is home. Every jump takes you somewhere and puts you straight
back in the composer, so the usual loop is: read, `Alt+A` to the next
conversation, type, `Enter`.

**Getting around**

| Key | Action |
| --- | --- |
| `Ctrl+K` | Fuzzy-search every channel and DM (matched letters are highlighted) |
| `Alt+A` | Next conversation with activity: mentions and DMs first |
| `Alt+1` … `Alt+9` | Open an item from the numbered activity list in the status bar |
| `Alt+/` | Flip back to the previous channel |
| `Alt+↑` / `Alt+↓`, `Ctrl+P` / `Ctrl+N` | Previous / next channel in the sidebar |
| `Tab` (empty composer) | Focus the sidebar, then the messages |
| `Ctrl+B` | Hide or show the sidebar |

**Writing**

| Key | Action |
| --- | --- |
| `Enter` / `Ctrl+J` | Send / new line (`Alt+Enter` and `Shift+Enter` also add a new line) |
| `Tab`, `Shift+Tab` | Complete `@names` (recent speakers first), `#channels`, `:emoji:` and `/commands` |
| `↑` (empty composer) | Edit your last message |
| `s/old/new` | Fix your last message, like the official client |
| `+:emoji:` | React to the last message |
| `Esc` | Cancel a reply, edit or completion; otherwise jump back to the present |

`@Name` and `#channel` turn into real mentions when sent, and `:shortcodes:`
into emoji. Code spans are left untouched. Drafts are kept per channel, even
across restarts. The prompt always shows what `Enter` will do: `❯` send,
`↪ Alice ❯` reply, `✎ edit ❯`, `☺ react ❯` or `cmd ❯`.

**Jump labels.** Any message on screen is two keys away. `Alt+R` labels the
visible messages with home-row letters (the nearest gets `a`), and typing a
letter replies to that message. `Alt+E` reacts, `Alt+O` opens a link, `Alt+Y`
copies, and `f` selects one in message mode. If only one message qualifies,
it acts immediately.

**Reading**

| Key | Action |
| --- | --- |
| `PgUp` / `PgDn`, mouse wheel | Scroll. The view holds still while new messages arrive and counts them |
| `Ctrl+F` | Find in the channel: matches are highlighted, `↑↓` steps, `Enter` acts on one |
| `Alt+U` | Scroll to the first unread message |
| `Alt+N` / `Alt+P` | Next / previous message that mentions you |
| `Alt+<` / `Alt+>` | Oldest / newest |

**On a message** (`Ctrl+↑`, or click it): `↑↓`/`jk` move, `r` or `Enter`
reply, `e` edit, `d` delete (asks first), `a` react, `y` copy, `o` open the
link in your Windows browser, and `Esc` returns to the composer.

**Commands:** `/dm <user> [text]`, `/me`, `/shrug`, `/tableflip`, `/unflip`,
`/react`, `/edit`, `/open`, `/read [all]`, `/topic`, `/theme dark|light|mono`,
`/time`, `/sidebar`, `/logout` and `/quit`. While you type a command, the
status bar shows its usage. Start a message with `//` to send a literal `/`.

`F1` shows every key. `Ctrl+C` clears the composer, and pressing it twice
quits. This avoids accidents, since Windows makes Ctrl+C a copy reflex.

## Design notes

The layout follows two decades of IRC clients that people spend all day in
(WeeChat, irssi, catgirl, senpai), adapted to Discord:

- **Quiet chrome.** One title bar, one status bar, a single rule between
  the sidebar and the messages. There are no boxes around panes: borders
  cost space and attention, and the content is what matters.
- **The timeline.** Names are right-aligned against a vertical rule and long
  messages wrap with a hanging indent, so the text column stays straight and
  scannable. Follow-ups from the same person dim the name instead of hiding
  it, so every message stays attributable. Times appear only when they
  change. Days are separated, and a red `new` rule marks where you stopped
  reading. If you switch to another window, that marker moves, so whatever
  arrived while you were away is easy to find.
- **Colour has a job.** Each person gets a stable colour derived from their
  user ID, so it survives name changes (as in catgirl). Discord role colours
  win, but they're lightened or darkened until they're readable on your
  background. Orange always means *you* (mentions, highlighted messages), the
  accent always means *where you are*, and dim means *safe to ignore*. Muted
  channels fade away.
- **The hotlist.** Like WeeChat's, it lives in the status bar and is
  numbered for `Alt+1…9`, so you can see and reach what's waiting without
  looking at the sidebar. The terminal tab title carries your mention count
  (`(2) #general - upper`), and mentions ring the bell (Windows Terminal
  flashes the taskbar). That only happens when you're not already looking.
- **Respect attention.** Channels are marked read only while the terminal
  window has focus and you're at the bottom. Scrolling back to read history
  doesn't silently mark things read.
- **Nothing is lost.** Unsent drafts and your last channel (`Alt+/`)
  survive restarts (`~/.local/state/upper`). Failed sends stay in place for
  a one-key retry.
- **Graceful everywhere.** It degrades to 80×24 and smaller (timestamps go
  first, then the sidebar narrows), honours `NO_COLOR` with a monochrome
  theme, and has light and dark Catppuccin palettes. Everything works with
  the keyboard alone, and the mouse works too.

## Configuration

`~/.config/upper/config.json` is optional. Any key you leave out keeps its
default:

```json
{
  "time_format": "15:04",
  "history_page": 50,
  "max_messages": 400,
  "max_channels": 64,
  "sidebar_width": 28,
  "bell_on_mention": true,
  "send_typing": true,
  "mark_read": true,
  "mention_on_reply": true,
  "theme": "dark",
  "nick_width": 16
}
```

- `time_format` is a [Go time layout](https://pkg.go.dev/time#pkg-constants);
  `""` hides timestamps.
- `bell_on_mention` rings the terminal bell on mentions and DMs. In Windows
  Terminal this flashes the taskbar icon.
- `mark_read` syncs read state with your other devices.
- `send_typing` controls whether others see you typing.
- `theme` is `dark`, `light` (for light terminal backgrounds) or `mono`.
  Setting `NO_COLOR` forces `mono`.
- `nick_width` caps the name column; longer names are shortened with `…`.

## WSL tips

- Use **Windows Terminal** for true colour, mouse support and bracketed
  paste. The legacy console works but looks worse.
- Windows Terminal reserves `Alt+Enter` for fullscreen, so use `Ctrl+J` for
  new lines (or unbind `Alt+Enter` in its settings).
- `y` copies through `clip.exe`, straight into the Windows clipboard, with
  full Unicode support. Outside WSL it uses `wl-copy`/`xclip`, or the
  OSC 52 escape sequence as a fallback.

## How it works, and why it scales

upper talks to Discord's API directly through its own small client, so it has
very few dependencies and nothing hidden runs in the background.

- **Gateway** (`internal/discord/gateway.go`): one WebSocket with
  `zlib-stream` transport compression. Discord sends one compressed stream
  for the whole connection, so frames are piped through a single inflater
  into a streaming JSON decoder, with no per-frame buffering. Heartbeats use
  jitter and zombie-connection detection. Dropped connections **resume**
  (Discord replays missed events) instead of reloading everything, using
  exponential backoff. Outgoing commands share a budget below Discord's
  120-per-minute limit, and heartbeats are never starved by them.
- **REST** (`internal/discord/rest.go`): rate limits are tracked per route
  from the `X-RateLimit-*` headers, so upper waits *before* it would hit a
  limit instead of collecting 429s. Global limits and `retry_after` are
  honoured.
- **State** (`internal/state`): decoded structs declare only the fields
  upper uses, which keeps a multi-megabyte READY payload cheap. READY is
  decoded outside the lock so the UI never stalls. Channels are indexed per
  server, and visibility follows Discord's permission algorithm (roles,
  @everyone, role and member overwrites), so hidden channels never show up.
  Message history is loaded only for channels you open, capped per channel,
  with the least recently viewed channels evicted. Memory stays flat whether
  you're in 5 servers or 200: refreshing the sidebar for 200 servers ×
  60 channels takes about 0.4 ms.
- **Large servers**: Discord only streams messages from big servers to user
  accounts that subscribe to them. upper subscribes to a server when you open
  one of its channels (keeping the 10 most recent), and uses Discord's
  lightweight unread updates for the rest.
- **UI** (`internal/ui`): the conversation is a custom virtualized view.
  Only the rows on screen are drawn, and wrapped layouts are cached per
  message, so long histories cost nothing extra. Events only mark what changed. A render loop
  coalesces bursts into at most ~30 redraws per second (the sidebar at most
  4 per second), so a busy server never floods the terminal. Sent messages
  appear instantly as pending and are matched to Discord's echo by nonce. A
  message that fails to send stays in place for one-key retry (`Enter`) or
  discard (`d`). Read acknowledgements are debounced into one request.
- **Notification settings**: muted servers, channels and categories, and
  suppressed `@everyone`, are respected for unread markers and the bell.

## Development

```sh
go test -race ./...        # unit tests plus a fake Discord gateway/REST server
go run ./cmd/upper --demo  # run against the fake server
```

`internal/discord/discordtest` is an in-process fake of the Gateway
(zlib-stream compressed, with identify, resume and heartbeats) and of the
REST endpoints upper uses. The tests run against it.
