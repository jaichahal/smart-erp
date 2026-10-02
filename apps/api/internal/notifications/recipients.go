package notifications

// User is a person the identity module can name. Directory is that module's
// read interface; notifications does not import identity.
type User struct {
	ID       string
	Name     string
	Email    string
	Roles    []string
	Disabled bool
}

// DeriveRecipients merges role-derived users with explicit ids, then drops the
// actor and every disabled user (E5, R13.2).
func DeriveRecipients(actorID string, roles, explicit []string, users []User) []User {
	roleSet := map[string]struct{}{}
	for _, r := range roles {
		roleSet[r] = struct{}{}
	}
	explicitSet := map[string]struct{}{}
	for _, id := range explicit {
		if id != "" {
			explicitSet[id] = struct{}{}
		}
	}
	byID := map[string]User{}
	for _, u := range users {
		if u.Disabled || u.ID == actorID || u.ID == "" {
			continue
		}
		if _, want := explicitSet[u.ID]; want {
			byID[u.ID] = u
			continue
		}
		for _, role := range u.Roles {
			if _, ok := roleSet[role]; ok {
				byID[u.ID] = u
				break
			}
		}
	}
	// Explicit ids that were not in the directory still count, except the actor.
	// Disabled state is only known from the directory, so an unknown explicit id
	// is kept: identity owns the disabled flag.
	for id := range explicitSet {
		if id == actorID {
			continue
		}
		if _, ok := byID[id]; ok {
			continue
		}
		if known, ok := findUser(users, id); ok && known.Disabled {
			continue
		}
		if _, ok := findUser(users, id); !ok {
			byID[id] = User{ID: id}
		}
	}
	out := make([]User, 0, len(byID))
	for _, u := range byID {
		out = append(out, u)
	}
	return out
}

func findUser(users []User, id string) (User, bool) {
	for _, u := range users {
		if u.ID == id {
			return u, true
		}
	}
	return User{}, false
}
