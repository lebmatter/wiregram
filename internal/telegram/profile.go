package telegram

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/gotd/td/tg"

	"wiregram/internal/backend"
)

// Profile describes a user from the users seen so far, with their bio and
// birthday from users.getFullUser. A user known only from a group (a min
// user) cannot be fetched in full, so the cached details stand alone.
func (tb *TelegramBackend) Profile(ctx context.Context, userID string) (backend.Profile, error) {
	id, err := strconv.ParseInt(userID, 10, 64)
	if err != nil {
		return backend.Profile{}, fmt.Errorf("read user ID %q: %w", userID, err)
	}
	c := tb.C
	c.mu.Lock()
	u := c.users[id]
	c.mu.Unlock()
	if u == nil {
		return backend.Profile{}, fmt.Errorf("user %d is not loaded", id)
	}
	var full *tg.UserFull
	if !u.Min {
		res, err := c.Api.UsersGetFullUser(ctx, u.AsInput())
		if err == nil {
			full = &res.FullUser
			for _, uc := range res.Users {
				if fresh, ok := uc.(*tg.User); ok && fresh.ID == id {
					u = fresh
				}
			}
		}
	}
	p := backend.Profile{Name: strings.TrimSpace(u.FirstName + " " + u.LastName)}
	if p.Name == "" {
		p.Name = UserName(u)
	}
	var handles []string
	if u.Username != "" {
		handles = append(handles, "@"+u.Username)
	}
	for _, un := range u.Usernames {
		if un.Active && un.Username != u.Username {
			handles = append(handles, "@"+un.Username)
		}
	}
	p.Add("Username", strings.Join(handles, " "))
	if u.Phone != "" {
		p.Add("Phone", "+"+u.Phone)
	}
	if full != nil {
		p.Add("Bio", full.About)
		if b, ok := full.GetBirthday(); ok {
			p.Add("Birthday", birthday(b))
		}
		if full.CommonChatsCount > 0 {
			p.Add("Groups in common", strconv.Itoa(full.CommonChatsCount))
		}
	}
	p.Add("Last seen", lastSeen(u.Status))
	var kinds []string
	for _, k := range []struct {
		on   bool
		name string
	}{{u.Bot, "bot"}, {u.Verified, "verified"}, {u.Premium, "premium"},
		{u.Contact, "contact"}, {u.Scam, "scam"}, {u.Fake, "fake"}} {
		if k.on {
			kinds = append(kinds, k.name)
		}
	}
	p.Add("Account", strings.Join(kinds, ", "))
	p.Add("User ID", userID)
	return p, nil
}

func birthday(b tg.Birthday) string {
	t := time.Date(2000, time.Month(b.Month), b.Day, 0, 0, 0, 0, time.Local)
	if y, ok := b.GetYear(); ok {
		return t.AddDate(y-2000, 0, 0).Format("2 January 2006")
	}
	return t.Format("2 January")
}

func lastSeen(s tg.UserStatusClass) string {
	switch s := s.(type) {
	case *tg.UserStatusOnline:
		return "online"
	case *tg.UserStatusOffline:
		return time.Unix(int64(s.WasOnline), 0).Local().Format("2 Jan 2006 15:04")
	case *tg.UserStatusRecently:
		return "recently"
	case *tg.UserStatusLastWeek:
		return "within a week"
	case *tg.UserStatusLastMonth:
		return "within a month"
	}
	return ""
}
