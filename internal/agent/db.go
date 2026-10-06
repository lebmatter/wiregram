package agent

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"

	"wiregram/internal/backend"
)

const currentDBVersion = 3

// PrimaryAccountKey names the account whose session lives in the config
// directory itself rather than under accounts/.
const PrimaryAccountKey = "primary"

type AgentProvider struct {
	ID        int64
	Name      string
	BaseURL   string
	APIKeyEnv string
	Model     string
}

// AgentRecord is a Chat Agent. It answers incoming DMs on the accounts bound
// to it; Model overrides the provider's default when set.
type AgentRecord struct {
	ID            int64
	Name          string
	Prompt        string
	Auto          bool   // send replies directly instead of suggesting them
	Media         string // MediaText, MediaTags or MediaVision
	ProviderID    int64
	Model         string
	RetentionDays int
	Enabled       bool
}

// What an agent receives of media messages.
const (
	MediaText   = "text"   // only plain text messages reach the agent
	MediaTags   = "tags"   // media arrive as their bracket tag plus caption
	MediaVision = "vision" // as tags, and incoming photos go to the model
)

func (a AgentRecord) TextOnly() bool { return a.Media == MediaText }
func (a AgentRecord) Vision() bool   { return a.Media == MediaVision }

type AgentLoggedMessage struct {
	MessageID    string
	Sender       string
	SenderID     string
	Text         string
	Timestamp    time.Time
	Outgoing     bool
	IsAgentReply bool
}

type AgentDB struct {
	db *sql.DB
}

func OpenAgentDB(dir string) (*AgentDB, error) {
	path := filepath.Join(dir, "wiregram.db")
	dsn := "file:" + path +
		"?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open agent database: %w", err)
	}
	d := &AgentDB{db: db}
	// Before v2 the primary account was keyed by the config directory's name.
	if err := d.migrate(filepath.Base(dir)); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate agent database: %w", err)
	}
	return d, nil
}

func (d *AgentDB) Close() error {
	return d.db.Close()
}

func (d *AgentDB) migrate(oldPrimaryKey string) error {
	_, err := d.db.Exec(`CREATE TABLE IF NOT EXISTS migrations (version INTEGER NOT NULL)`)
	if err != nil {
		return fmt.Errorf("create migrations table: %w", err)
	}

	version := 0
	err = d.db.QueryRow(`SELECT version FROM migrations LIMIT 1`).Scan(&version)
	if err == sql.ErrNoRows {
		if _, err := d.db.Exec(`INSERT INTO migrations (version) VALUES (0)`); err != nil {
			return fmt.Errorf("init migration version: %w", err)
		}
	} else if err != nil {
		return fmt.Errorf("read migration version: %w", err)
	}

	steps := []func(*sql.Tx) error{
		func(tx *sql.Tx) error {
			_, err := tx.Exec(schemaV1)
			return err
		},
		func(tx *sql.Tx) error {
			if _, err := tx.Exec(schemaV2); err != nil {
				return err
			}
			// Chat assignments become account bindings: one agent per account.
			if _, err := tx.Exec(`INSERT OR IGNORE INTO agent_accounts (account_key, agent_id)
				SELECT CASE account_id WHEN ?1 THEN ?2 ELSE account_id END, MIN(agent_id)
				FROM agent_chats GROUP BY account_id`, oldPrimaryKey, PrimaryAccountKey); err != nil {
				return err
			}
			if _, err := tx.Exec(`UPDATE agent_messages SET account_id = ? WHERE account_id = ?`,
				PrimaryAccountKey, oldPrimaryKey); err != nil {
				return err
			}
			_, err := tx.Exec(`DROP TABLE agent_chats`)
			return err
		},
		func(tx *sql.Tx) error {
			_, err := tx.Exec(schemaV3)
			return err
		},
	}
	for v := version; v < currentDBVersion; v++ {
		tx, err := d.db.Begin()
		if err != nil {
			return fmt.Errorf("begin schema v%d: %w", v+1, err)
		}
		if err := steps[v](tx); err != nil {
			tx.Rollback()
			return fmt.Errorf("apply schema v%d: %w", v+1, err)
		}
		if _, err := tx.Exec(`UPDATE migrations SET version = ?`, v+1); err != nil {
			tx.Rollback()
			return fmt.Errorf("record schema v%d: %w", v+1, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit schema v%d: %w", v+1, err)
		}
	}
	return nil
}

// schemaV3 replaces the vision flag with a media mode; the old column stays,
// unused, because SQLite cannot drop it cheaply.
const schemaV3 = `
ALTER TABLE agents ADD COLUMN media TEXT NOT NULL DEFAULT 'tags';
UPDATE agents SET media = 'vision' WHERE vision = 1;
`

const schemaV2 = `
ALTER TABLE agents ADD COLUMN model TEXT NOT NULL DEFAULT '';
ALTER TABLE agents ADD COLUMN vision INTEGER NOT NULL DEFAULT 0;
ALTER TABLE agent_messages ADD COLUMN chat_title TEXT NOT NULL DEFAULT '';

DELETE FROM agent_messages WHERE id NOT IN (
	SELECT MIN(id) FROM agent_messages GROUP BY agent_id, account_id, chat_id, message_id
);
CREATE UNIQUE INDEX idx_agent_messages_unique
	ON agent_messages(agent_id, account_id, chat_id, message_id);

CREATE TABLE agent_accounts (
	account_key TEXT PRIMARY KEY,
	agent_id    INTEGER NOT NULL REFERENCES agents(id) ON DELETE CASCADE
);
`

const schemaV1 = `
CREATE TABLE IF NOT EXISTS providers (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	name       TEXT NOT NULL,
	base_url   TEXT NOT NULL,
	api_key_env TEXT NOT NULL DEFAULT '',
	model      TEXT NOT NULL,
	created_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS agents (
	id             INTEGER PRIMARY KEY AUTOINCREMENT,
	name           TEXT NOT NULL,
	prompt         TEXT NOT NULL DEFAULT '',
	autoreply      INTEGER NOT NULL DEFAULT 0,
	provider_id    INTEGER NOT NULL REFERENCES providers(id),
	retention_days INTEGER NOT NULL DEFAULT 30,
	enabled        INTEGER NOT NULL DEFAULT 1,
	created_at     TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS agent_knowledge (
	agent_id  INTEGER NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
	file_path TEXT NOT NULL,
	PRIMARY KEY (agent_id, file_path)
);

CREATE TABLE IF NOT EXISTS agent_chats (
	agent_id     INTEGER NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
	backend_type TEXT NOT NULL,
	account_id   TEXT NOT NULL,
	chat_id      TEXT NOT NULL,
	PRIMARY KEY (agent_id, backend_type, account_id, chat_id)
);

CREATE TABLE IF NOT EXISTS agent_messages (
	id             INTEGER PRIMARY KEY AUTOINCREMENT,
	agent_id       INTEGER NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
	backend_type   TEXT NOT NULL,
	account_id     TEXT NOT NULL,
	chat_id        TEXT NOT NULL,
	message_id     TEXT NOT NULL,
	sender         TEXT NOT NULL,
	sender_id      TEXT NOT NULL,
	text           TEXT NOT NULL,
	timestamp      TEXT NOT NULL,
	outgoing       INTEGER NOT NULL DEFAULT 0,
	is_agent_reply INTEGER NOT NULL DEFAULT 0,
	created_at     TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX IF NOT EXISTS idx_agent_messages_lookup
	ON agent_messages(agent_id, backend_type, account_id, chat_id, timestamp);

CREATE INDEX IF NOT EXISTS idx_agent_messages_retention
	ON agent_messages(created_at);
`

// Provider CRUD.

func (d *AgentDB) ListProviders() ([]AgentProvider, error) {
	rows, err := d.db.Query(`SELECT id, name, base_url, api_key_env, model FROM providers ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list providers: %w", err)
	}
	defer rows.Close()
	var out []AgentProvider
	for rows.Next() {
		var p AgentProvider
		if err := rows.Scan(&p.ID, &p.Name, &p.BaseURL, &p.APIKeyEnv, &p.Model); err != nil {
			return nil, fmt.Errorf("scan provider: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (d *AgentDB) CreateProvider(p AgentProvider) (int64, error) {
	res, err := d.db.Exec(
		`INSERT INTO providers (name, base_url, api_key_env, model) VALUES (?, ?, ?, ?)`,
		p.Name, p.BaseURL, p.APIKeyEnv, p.Model,
	)
	if err != nil {
		return 0, fmt.Errorf("create provider: %w", err)
	}
	return res.LastInsertId()
}

func (d *AgentDB) UpdateProvider(p AgentProvider) error {
	_, err := d.db.Exec(
		`UPDATE providers SET name = ?, base_url = ?, api_key_env = ?, model = ? WHERE id = ?`,
		p.Name, p.BaseURL, p.APIKeyEnv, p.Model, p.ID,
	)
	if err != nil {
		return fmt.Errorf("update provider: %w", err)
	}
	return nil
}

func (d *AgentDB) DeleteProvider(id int64) error {
	_, err := d.db.Exec(`DELETE FROM providers WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete provider: %w", err)
	}
	return nil
}

func (d *AgentDB) GetProvider(id int64) (AgentProvider, error) {
	var p AgentProvider
	err := d.db.QueryRow(
		`SELECT id, name, base_url, api_key_env, model FROM providers WHERE id = ?`, id,
	).Scan(&p.ID, &p.Name, &p.BaseURL, &p.APIKeyEnv, &p.Model)
	if err != nil {
		return p, fmt.Errorf("get provider: %w", err)
	}
	return p, nil
}

// Agent CRUD.

func (d *AgentDB) ListAgents() ([]AgentRecord, error) {
	rows, err := d.db.Query(
		`SELECT id, name, prompt, autoreply, media, provider_id, model, retention_days, enabled
		 FROM agents ORDER BY name`,
	)
	if err != nil {
		return nil, fmt.Errorf("list agents: %w", err)
	}
	defer rows.Close()
	var out []AgentRecord
	for rows.Next() {
		var a AgentRecord
		if err := rows.Scan(&a.ID, &a.Name, &a.Prompt, &a.Auto, &a.Media, &a.ProviderID,
			&a.Model, &a.RetentionDays, &a.Enabled); err != nil {
			return nil, fmt.Errorf("scan agent: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (d *AgentDB) CreateAgent(a AgentRecord) (int64, error) {
	res, err := d.db.Exec(
		`INSERT INTO agents (name, prompt, autoreply, media, provider_id, model, retention_days, enabled)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		a.Name, a.Prompt, a.Auto, a.Media, a.ProviderID, a.Model, a.RetentionDays, a.Enabled,
	)
	if err != nil {
		return 0, fmt.Errorf("create agent: %w", err)
	}
	return res.LastInsertId()
}

func (d *AgentDB) UpdateAgent(a AgentRecord) error {
	_, err := d.db.Exec(
		`UPDATE agents SET name = ?, prompt = ?, autoreply = ?, media = ?, provider_id = ?,
		 model = ?, retention_days = ?, enabled = ? WHERE id = ?`,
		a.Name, a.Prompt, a.Auto, a.Media, a.ProviderID, a.Model, a.RetentionDays, a.Enabled, a.ID,
	)
	if err != nil {
		return fmt.Errorf("update agent: %w", err)
	}
	return nil
}

func (d *AgentDB) DeleteAgent(id int64) error {
	_, err := d.db.Exec(`DELETE FROM agents WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete agent: %w", err)
	}
	return nil
}

// Agent knowledge.

func (d *AgentDB) AgentKnowledge(agentID int64) ([]string, error) {
	rows, err := d.db.Query(`SELECT file_path FROM agent_knowledge WHERE agent_id = ? ORDER BY file_path`, agentID)
	if err != nil {
		return nil, fmt.Errorf("list agent knowledge: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			return nil, fmt.Errorf("scan knowledge path: %w", err)
		}
		out = append(out, path)
	}
	return out, rows.Err()
}

func (d *AgentDB) SetAgentKnowledge(agentID int64, paths []string) error {
	tx, err := d.db.Begin()
	if err != nil {
		return fmt.Errorf("begin knowledge tx: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`DELETE FROM agent_knowledge WHERE agent_id = ?`, agentID); err != nil {
		return fmt.Errorf("clear knowledge: %w", err)
	}
	for _, path := range paths {
		if _, err := tx.Exec(`INSERT INTO agent_knowledge (agent_id, file_path) VALUES (?, ?)`, agentID, path); err != nil {
			return fmt.Errorf("insert knowledge %q: %w", path, err)
		}
	}
	return tx.Commit()
}

// Account bindings. Each account answers to at most one agent.

// AccountAgents maps every bound account key to its agent.
func (d *AgentDB) AccountAgents() (map[string]int64, error) {
	rows, err := d.db.Query(`SELECT account_key, agent_id FROM agent_accounts`)
	if err != nil {
		return nil, fmt.Errorf("list agent accounts: %w", err)
	}
	defer rows.Close()
	out := make(map[string]int64)
	for rows.Next() {
		var key string
		var id int64
		if err := rows.Scan(&key, &id); err != nil {
			return nil, fmt.Errorf("scan agent account: %w", err)
		}
		out[key] = id
	}
	return out, rows.Err()
}

// SetAgentAccounts replaces the agent's bindings. A key bound to another agent
// fails the whole change.
func (d *AgentDB) SetAgentAccounts(agentID int64, keys []string) error {
	tx, err := d.db.Begin()
	if err != nil {
		return fmt.Errorf("begin accounts tx: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`DELETE FROM agent_accounts WHERE agent_id = ?`, agentID); err != nil {
		return fmt.Errorf("clear agent accounts: %w", err)
	}
	for _, key := range keys {
		if _, err := tx.Exec(
			`INSERT INTO agent_accounts (account_key, agent_id) VALUES (?, ?)`, key, agentID,
		); err != nil {
			return fmt.Errorf("account is already connected to another agent: %w", err)
		}
	}
	return tx.Commit()
}

// Message log.

// LogMessage records a DM message once; the backend's echo of an agent reply
// only keeps the reply flag. chatTitle may be empty for outgoing messages.
func (d *AgentDB) LogMessage(agentID int64, backendType, accountID, chatIDStr, chatTitle string, msg backend.Message, text string, isAgentReply bool) error {
	_, err := d.db.Exec(
		`INSERT INTO agent_messages (agent_id, backend_type, account_id, chat_id, chat_title, message_id, sender, sender_id, text, timestamp, outgoing, is_agent_reply)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT (agent_id, account_id, chat_id, message_id) DO UPDATE SET
		 	is_agent_reply = MAX(is_agent_reply, excluded.is_agent_reply)`,
		agentID, backendType, accountID, chatIDStr, chatTitle,
		msg.ID, msg.Sender, msg.SenderID, text,
		msg.Timestamp.UTC().Format(time.RFC3339),
		msg.Outgoing, isAgentReply,
	)
	if err != nil {
		return fmt.Errorf("log message: %w", err)
	}
	return nil
}

func (d *AgentDB) ChatHistory(agentID int64, backendType, accountID, chatIDStr string, limit int) ([]AgentLoggedMessage, error) {
	rows, err := d.db.Query(
		`SELECT message_id, sender, sender_id, text, timestamp, outgoing, is_agent_reply
		 FROM agent_messages
		 WHERE agent_id = ? AND backend_type = ? AND account_id = ? AND chat_id = ?
		 ORDER BY timestamp DESC, id DESC
		 LIMIT ?`,
		agentID, backendType, accountID, chatIDStr, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("chat history: %w", err)
	}
	defer rows.Close()
	var out []AgentLoggedMessage
	for rows.Next() {
		var m AgentLoggedMessage
		var ts string
		if err := rows.Scan(&m.MessageID, &m.Sender, &m.SenderID, &m.Text, &ts, &m.Outgoing, &m.IsAgentReply); err != nil {
			return nil, fmt.Errorf("scan message: %w", err)
		}
		m.Timestamp, _ = time.Parse(time.RFC3339, ts)
		out = append(out, m)
	}
	// Reverse to chronological order.
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, rows.Err()
}

func (d *AgentDB) PurgeOldMessages(agentID int64, retentionDays int) (int64, error) {
	cutoff := time.Now().UTC().AddDate(0, 0, -retentionDays).Format(time.RFC3339)
	res, err := d.db.Exec(
		`DELETE FROM agent_messages WHERE agent_id = ? AND timestamp < ?`, agentID, cutoff,
	)
	if err != nil {
		return 0, fmt.Errorf("purge old messages: %w", err)
	}
	return res.RowsAffected()
}

// Thread listing for agent UI.

type AgentThread struct {
	BackendType  string
	AccountID    string
	ChatID       string
	ChatTitle    string
	LastAt       time.Time
	MessageCount int
}

func (d *AgentDB) AgentThreads(agentID int64) ([]AgentThread, error) {
	rows, err := d.db.Query(
		`SELECT backend_type, account_id, chat_id,
		 	COALESCE((SELECT t.chat_title FROM agent_messages t
		 		WHERE t.agent_id = m.agent_id AND t.account_id = m.account_id
		 		AND t.chat_id = m.chat_id AND t.chat_title != ''
		 		ORDER BY t.timestamp DESC LIMIT 1), ''),
		 	MAX(timestamp) as last_at, COUNT(*) as cnt
		 FROM agent_messages m
		 WHERE agent_id = ?
		 GROUP BY backend_type, account_id, chat_id
		 ORDER BY last_at DESC`,
		agentID,
	)
	if err != nil {
		return nil, fmt.Errorf("list agent threads: %w", err)
	}
	defer rows.Close()
	var out []AgentThread
	for rows.Next() {
		var t AgentThread
		var ts string
		if err := rows.Scan(&t.BackendType, &t.AccountID, &t.ChatID, &t.ChatTitle, &ts, &t.MessageCount); err != nil {
			return nil, fmt.Errorf("scan agent thread: %w", err)
		}
		t.LastAt, _ = time.Parse(time.RFC3339, ts)
		out = append(out, t)
	}
	return out, rows.Err()
}
