# Assist - Telegram Secretary Bot

Telegram bot for time tracking and Jira integration.

## Quick Start

1. Create bot via @BotFather
2. Copy `.env.example` to `.env` and fill in
3. `make build && bin/assist.sh`

## Commands

| Command | Description |
|---------|-------------|
| `/help` | Show help |
| `/task` | Create manual task |
| `/link` | Link Jira ID to task |
| `/report` | Daily report grouped by Jira |
| `/status` | Current session info |
| `/stop` | Stop current session |
| `/extend` | Extend session timeout |
| `/tasks` | List active tasks |
| `/purge` | Delete data (with confirmation) |

## License

MIT © 2026 Sergei Beliaev sergei.v.beliaev@gmail.com
