// Package discord implements the small subset of the Discord user API that
// upper needs: a resumable, compressed Gateway connection, a rate-limit aware
// REST client and the data types for guilds, channels, users and messages.
//
// Structs deliberately declare only the fields upper uses. encoding/json skips
// everything else without allocating, which keeps the (potentially multi-MB)
// READY payload cheap to decode.
package discord

import (
	"encoding/json"
	"strconv"
	"time"
)

// Snowflake is a Discord ID. Discord sends them as JSON strings.
type Snowflake uint64

// discordEpoch is the first second of 2015 in Unix milliseconds.
const discordEpoch = 1420070400000

func (s Snowflake) String() string { return strconv.FormatUint(uint64(s), 10) }

// Time returns the creation time encoded in the snowflake.
func (s Snowflake) Time() time.Time {
	return time.UnixMilli(int64(s>>22) + discordEpoch)
}

func (s Snowflake) MarshalJSON() ([]byte, error) {
	if s == 0 {
		return []byte("null"), nil
	}
	return []byte(`"` + s.String() + `"`), nil
}

func (s *Snowflake) UnmarshalJSON(b []byte) error {
	if len(b) == 0 || string(b) == "null" {
		*s = 0
		return nil
	}
	if b[0] == '"' {
		b = b[1 : len(b)-1]
	}
	v, err := strconv.ParseUint(string(b), 10, 64)
	*s = Snowflake(v)
	return err
}

// ParseSnowflake parses a decimal ID.
func ParseSnowflake(s string) (Snowflake, error) {
	v, err := strconv.ParseUint(s, 10, 64)
	return Snowflake(v), err
}

// NewNonce returns a snowflake-shaped nonce for the current time, which is what
// the official client sends. It lets us match our own echoed MESSAGE_CREATE.
func NewNonce() Snowflake {
	return Snowflake(uint64(time.Now().UnixMilli()-discordEpoch) << 22)
}

// Permissions is a permission bit set. Discord sends it as a string.
type Permissions uint64

func (p *Permissions) UnmarshalJSON(b []byte) error {
	if len(b) > 0 && b[0] == '"' {
		b = b[1 : len(b)-1]
	}
	if len(b) == 0 || string(b) == "null" {
		*p = 0
		return nil
	}
	v, err := strconv.ParseUint(string(b), 10, 64)
	*p = Permissions(v)
	return err
}

const (
	PermAdministrator      Permissions = 1 << 3
	PermViewChannel        Permissions = 1 << 10
	PermSendMessages       Permissions = 1 << 11
	PermManageMessages     Permissions = 1 << 13
	PermReadMessageHistory Permissions = 1 << 16
	PermSendInThreads      Permissions = 1 << 38
	PermAll                Permissions = ^Permissions(0)
)

type ChannelType int

const (
	ChannelText          ChannelType = 0
	ChannelDM            ChannelType = 1
	ChannelVoice         ChannelType = 2
	ChannelGroupDM       ChannelType = 3
	ChannelCategory      ChannelType = 4
	ChannelAnnouncement  ChannelType = 5
	ChannelAnnounceThrd  ChannelType = 10
	ChannelPublicThread  ChannelType = 11
	ChannelPrivateThread ChannelType = 12
	ChannelStage         ChannelType = 13
	ChannelForum         ChannelType = 15
	ChannelMedia         ChannelType = 16
)

// IsText reports whether the channel holds a plain text message stream that
// upper can show. Voice, stage, forum and media channels are excluded.
func (t ChannelType) IsText() bool {
	switch t {
	case ChannelText, ChannelDM, ChannelGroupDM, ChannelAnnouncement,
		ChannelAnnounceThrd, ChannelPublicThread, ChannelPrivateThread:
		return true
	}
	return false
}

func (t ChannelType) IsPrivate() bool { return t == ChannelDM || t == ChannelGroupDM }

type User struct {
	ID            Snowflake `json:"id"`
	Username      string    `json:"username"`
	GlobalName    string    `json:"global_name"`
	Discriminator string    `json:"discriminator"`
	Bot           bool      `json:"bot"`
}

// DisplayName is the name the official client shows.
func (u *User) DisplayName() string {
	if u.GlobalName != "" {
		return u.GlobalName
	}
	return u.Username
}

// Tag is the unique handle (legacy name#1234 for old-style accounts).
func (u *User) Tag() string {
	if u.Discriminator != "" && u.Discriminator != "0" {
		return u.Username + "#" + u.Discriminator
	}
	return u.Username
}

type Overwrite struct {
	ID    Snowflake   `json:"id"`
	Type  int         `json:"type"` // 0 role, 1 member
	Allow Permissions `json:"allow"`
	Deny  Permissions `json:"deny"`
}

type Channel struct {
	ID            Snowflake   `json:"id"`
	Type          ChannelType `json:"type"`
	GuildID       Snowflake   `json:"guild_id"`
	Name          string      `json:"name"`
	Topic         string      `json:"topic"`
	Position      int         `json:"position"`
	ParentID      Snowflake   `json:"parent_id"`
	LastMessageID Snowflake   `json:"last_message_id"`
	Overwrites    []Overwrite `json:"permission_overwrites"`
	Recipients    []User      `json:"recipients"`
	RecipientIDs  []Snowflake `json:"recipient_ids"`
	OwnerID       Snowflake   `json:"owner_id"`
}

type Role struct {
	ID          Snowflake   `json:"id"`
	Name        string      `json:"name"`
	Color       int         `json:"color"`
	Position    int         `json:"position"`
	Permissions Permissions `json:"permissions"`
}

type Member struct {
	User  *User       `json:"user"`
	Nick  string      `json:"nick"`
	Roles []Snowflake `json:"roles"`
	// Present on members received with messages.
	UserID Snowflake `json:"user_id"`
}

type Guild struct {
	ID          Snowflake `json:"id"`
	Name        string    `json:"name"`
	OwnerID     Snowflake `json:"owner_id"`
	Roles       []Role    `json:"roles"`
	Channels    []Channel `json:"channels"`
	Threads     []Channel `json:"threads"`
	Members     []Member  `json:"members"`
	Large       bool      `json:"large"`
	MemberCount int       `json:"member_count"`
	Unavailable bool      `json:"unavailable"`

	// With CLIENT_STATE_V2 the guild fields move under "properties". upper
	// does not request that capability, but accept the shape anyway.
	Properties *struct {
		Name    string    `json:"name"`
		OwnerID Snowflake `json:"owner_id"`
	} `json:"properties"`
}

// Normalize folds the properties sub-object into the main struct.
func (g *Guild) Normalize() {
	if g.Properties != nil {
		if g.Name == "" {
			g.Name = g.Properties.Name
		}
		if g.OwnerID == 0 {
			g.OwnerID = g.Properties.OwnerID
		}
		g.Properties = nil
	}
}

type MessageType int

const (
	MessageDefault MessageType = 0
	MessageReply   MessageType = 19
)

type Attachment struct {
	Filename string `json:"filename"`
	Size     int    `json:"size"`
}

type Embed struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	URL         string `json:"url"`
}

type Sticker struct {
	Name string `json:"name"`
}

type MessageReference struct {
	MessageID Snowflake `json:"message_id,omitempty"`
	ChannelID Snowflake `json:"channel_id,omitempty"`
	GuildID   Snowflake `json:"guild_id,omitempty"`
}

type Message struct {
	ID                Snowflake         `json:"id"`
	ChannelID         Snowflake         `json:"channel_id"`
	GuildID           Snowflake         `json:"guild_id"`
	Type              MessageType       `json:"type"`
	Author            User              `json:"author"`
	Member            *Member           `json:"member"`
	Content           string            `json:"content"`
	Timestamp         time.Time         `json:"timestamp"`
	EditedTimestamp   *time.Time        `json:"edited_timestamp"`
	Mentions          []User            `json:"mentions"`
	MentionRoles      []Snowflake       `json:"mention_roles"`
	MentionEveryone   bool              `json:"mention_everyone"`
	Attachments       []Attachment      `json:"attachments"`
	Embeds            []Embed           `json:"embeds"`
	Stickers          []Sticker         `json:"sticker_items"`
	ReferencedMessage *Message          `json:"referenced_message"`
	MessageReference  *MessageReference `json:"message_reference"`
	Nonce             json.RawMessage   `json:"nonce"`

	// Local-only state for optimistic sends.
	Pending bool `json:"-"`
	Failed  bool `json:"-"`
}

// NonceValue returns the nonce as a snowflake, or 0.
func (m *Message) NonceValue() Snowflake {
	var s Snowflake
	if len(m.Nonce) > 0 {
		_ = s.UnmarshalJSON(m.Nonce)
	}
	return s
}

// MessageDelete is the payload of MESSAGE_DELETE.
type MessageDelete struct {
	ID        Snowflake `json:"id"`
	ChannelID Snowflake `json:"channel_id"`
	GuildID   Snowflake `json:"guild_id"`
}

type MessageDeleteBulk struct {
	IDs       []Snowflake `json:"ids"`
	ChannelID Snowflake   `json:"channel_id"`
}

type TypingStart struct {
	ChannelID Snowflake `json:"channel_id"`
	GuildID   Snowflake `json:"guild_id"`
	UserID    Snowflake `json:"user_id"`
	Member    *Member   `json:"member"`
}

type ReadState struct {
	ID            Snowflake `json:"id"`
	LastMessageID Snowflake `json:"last_message_id"`
	MentionCount  int       `json:"mention_count"`
	ReadStateType int       `json:"read_state_type"`
}

type MessageAck struct {
	ChannelID Snowflake `json:"channel_id"`
	MessageID Snowflake `json:"message_id"`
}

type GuildMemberUpdate struct {
	GuildID Snowflake   `json:"guild_id"`
	User    User        `json:"user"`
	Nick    string      `json:"nick"`
	Roles   []Snowflake `json:"roles"`
}

type GuildRoleEvent struct {
	GuildID Snowflake `json:"guild_id"`
	Role    Role      `json:"role"`
	RoleID  Snowflake `json:"role_id"`
}

type GuildDelete struct {
	ID          Snowflake `json:"id"`
	Unavailable bool      `json:"unavailable"`
}

// Ready is the subset of the READY event upper consumes. upper identifies
// without the DEDUPE_USER_OBJECTS capability so users are inlined.
type Ready struct {
	SessionID        string    `json:"session_id"`
	ResumeGatewayURL string    `json:"resume_gateway_url"`
	User             User      `json:"user"`
	Guilds           []Guild   `json:"guilds"`
	PrivateChannels  []Channel `json:"private_channels"`
	Users            []User    `json:"users"`
	Relationships    []struct {
		Type int  `json:"type"` // 1 friend
		User User `json:"user"`
	} `json:"relationships"`
	// Without VERSIONED_READ_STATES this is a plain array.
	ReadState json.RawMessage `json:"read_state"`
	// Without VERSIONED_USER_GUILD_SETTINGS this is a plain array.
	UserGuildSettings json.RawMessage `json:"user_guild_settings"`
	// Order of guilds in the user's sidebar, if the legacy settings are sent.
	UserSettings *struct {
		GuildPositions []Snowflake `json:"guild_positions"`
		GuildFolders   []struct {
			GuildIDs []Snowflake `json:"guild_ids"`
		} `json:"guild_folders"`
	} `json:"user_settings"`
}

// GuildSettings are the user's notification settings for one guild.
type GuildSettings struct {
	GuildID          Snowflake `json:"guild_id"` // 0 for DMs
	Muted            bool      `json:"muted"`
	SuppressEveryone bool      `json:"suppress_everyone"`
	ChannelOverrides []struct {
		ChannelID Snowflake `json:"channel_id"`
		Muted     bool      `json:"muted"`
	} `json:"channel_overrides"`
}

// GuildSettings decodes user_guild_settings in either shape.
func (r *Ready) GuildSettings() []GuildSettings {
	return decodeVersioned[GuildSettings](r.UserGuildSettings)
}

func decodeVersioned[T any](raw json.RawMessage) []T {
	if len(raw) == 0 {
		return nil
	}
	var plain []T
	if json.Unmarshal(raw, &plain) == nil {
		return plain
	}
	var versioned struct {
		Entries []T `json:"entries"`
	}
	_ = json.Unmarshal(raw, &versioned)
	return versioned.Entries
}

// ReadStates decodes read_state whether it is a plain or versioned array.
func (r *Ready) ReadStates() []ReadState {
	return decodeVersioned[ReadState](r.ReadState)
}
