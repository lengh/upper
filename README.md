# upper

A fast, keyboard-driven Discord client for the terminal, made for WSL.
It does text chat only: servers, channels, threads you've joined and direct
messages. There are no voice calls, images or file uploads.

```
┌ servers ───────────────┐ Test Server › #general  Say hi
│▾ Direct Messages (1)   │┌──────────────────────────────────────────────────┐
│  @ Alice (1)           ││ ──── Wednesday, 7 October 2026 ────              │
│▾ Test Server (2)       ││ 13:28 Alice (mod) ▌live message 1, ping @Me      │
│  TEXT                  ││   ╭─ Alice (mod): live message 1, ping @Me       │
│  # general (2)         ││ 13:28 Me thanks alice                            │
│  # random              ││ 13:29 Alice (mod) welcome to the upper demo!     │
│                        │└──────────────────────────────────────────────────┘
│                        │ Alice (mod) is typing…
│                        │╔══════════════════════════════════════════════════╗
│                        │║ Message #general — Enter send · Ctrl+J newline   ║
└────────────────────────┘╚══════════════════════════════════════════════════╝
 upper connected  Ctrl+K switch · Alt+U next unread · Tab focus · F1 help
```

> [!WARNING]
> upper logs in with your **personal account token**. Discord's Terms of
> Service don't allow third-party clients or automated user accounts, and
> accounts have occasionally been flagged or banned for using them. upper
> keeps the risk low: it identifies itself honestly, sends nothing in the
> background, and stays well inside rate limits. The risk is still not zero,
> so use it at your own discretion.

## Install (WSL / Linux)

You need Go 1.21 or newer, which automatically downloads the exact toolchain
the project pins. On Ubuntu 24.04 and later, `apt install golang-go` is
enough. Older releases ship an older Go, so use `sudo snap install go
--classic` or the tarball from <https://go.dev/dl>.

```sh
sudo apt install golang-go git
git clone https://github.com/lengh/upper
cd upper
go build -o ~/.local/bin/upper ./cmd/upper
```

Make sure `~/.local/bin` is on your `PATH`, then run `upper`.

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

| Key | Action |
| --- | --- |
| `Ctrl+K` | Fuzzy-search every channel and DM, with unread ones first |
| `Alt+U` | Jump to the next unread channel (mentions first) |
| `Alt+↑` / `Alt+↓` | Previous / next channel in the sidebar |
| `Tab` / `Shift+Tab` | Move focus between sidebar → messages → composer |
| `Enter` | Send (in the composer) · open (in the sidebar) |
| `Ctrl+J` or `Alt+Enter` | New line in the composer |
| `↑` (empty composer) | Edit your last message |
| `PgUp` | Browse messages: `↑↓`/`jk` select, `r` reply, `e` edit, `d` delete, `y` copy, `g`/`G` oldest/newest |
| `Esc` | Cancel a reply or edit, or go back |
| `Ctrl+B` | Hide/show the sidebar |
| `F1` | Help |
| `Ctrl+C` / `Ctrl+Q` | Quit |

Scrolling past the top of a channel, with the keyboard or the mouse wheel,
loads older history. Multi-line pastes land in the composer as one message.
Messages longer than 2000 characters are split at line or word boundaries.

**Commands:** `/dm <user> [text]` opens a DM with a friend, `/read` marks
everything read, `/sidebar`, `/logout` and `/quit`. Start a message with
`//` to send a literal `/`.

Attachments, stickers and embeds appear as short text placeholders such as
`[attachment: notes.pdf]`, so you can tell something was shared.

## Configuration

`~/.config/upper/config.json` is optional. Any key you leave out keeps its
default:

```json
{
  "time_format": "15:04",
  "history_page": 50,
  "max_messages": 400,
  "max_channels": 64,
  "sidebar_width": 32,
  "bell_on_mention": true,
  "send_typing": true,
  "mark_read": true,
  "mention_on_reply": true,
  "theme": {
    "border": "#5865f2", "accent": "#5865f2", "timestamp": "#72767d",
    "muted": "#8e9297", "mention": "#faa61a", "unread": "#ffffff",
    "selected": "#404249", "error": "#ed4245"
  }
}
```

- `time_format` is a [Go time layout](https://pkg.go.dev/time#pkg-constants);
  `""` hides timestamps.
- `bell_on_mention` rings the terminal bell on mentions and DMs. In Windows
  Terminal this flashes the taskbar icon.
- `mark_read` syncs read state with your other devices.
- `send_typing` controls whether others see you typing.

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
- **UI** (`internal/ui`): events only mark what changed. A render loop
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
