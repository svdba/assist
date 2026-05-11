// internal/bot/auth.go

package bot

// auth middleware (STUB)
func (b *Bot) authorizeUser(id int64) bool {

	// List of authorized user IDs (get these from update.Message.From.ID)
	var authorizedUsers = map[int64]bool{
		828391336:  true,
		5582073687: true,
	}

	return authorizedUsers[id]
}
