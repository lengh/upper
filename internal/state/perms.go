package state

import "github.com/lengh/upper/internal/discord"

// permissions computes the user's effective permissions in a guild channel,
// following Discord's documented algorithm: base role permissions, then the
// channel's @everyone, role and member overwrites. Threads use their parent.
func (s *State) permissions(g *guild, ch *discord.Channel) discord.Permissions {
	if g.OwnerID == s.me.ID {
		return discord.PermAll
	}
	perms := g.roles[g.ID].Permissions // @everyone role shares the guild ID
	for _, id := range g.myRoles {
		perms |= g.roles[id].Permissions
	}
	if perms&discord.PermAdministrator != 0 {
		return discord.PermAll
	}

	if ch.Type >= discord.ChannelAnnounceThrd && ch.Type <= discord.ChannelPrivateThread {
		if parent := s.channels[ch.ParentID]; parent != nil {
			ch = parent
		}
	}

	var allow, deny discord.Permissions
	for _, o := range ch.Overwrites {
		if o.Type == 0 && o.ID == g.ID {
			perms = perms&^o.Deny | o.Allow
		}
	}
	for _, o := range ch.Overwrites {
		if o.Type == 0 && o.ID != g.ID && containsID(g.myRoles, o.ID) {
			allow |= o.Allow
			deny |= o.Deny
		}
	}
	perms = perms&^deny | allow
	for _, o := range ch.Overwrites {
		if o.Type == 1 && o.ID == s.me.ID {
			perms = perms&^o.Deny | o.Allow
		}
	}
	return perms
}

func (s *State) canView(g *guild, ch *discord.Channel) bool {
	return s.permissions(g, ch)&discord.PermViewChannel != 0
}

func containsID(ids []discord.Snowflake, id discord.Snowflake) bool {
	for _, x := range ids {
		if x == id {
			return true
		}
	}
	return false
}
