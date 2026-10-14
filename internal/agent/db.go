package agent

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"time"

	"wiregram/internal/backend"
)

const currentDBVersion = 4

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

// AgentRecord is a Chat Agent. It belongs to one account and answers its
// incoming DMs, all of them or only the chats in agent_chats; Model overrides
// the provider's default when set.
type AgentRecord struct {
	ID            int64
	AccountKey    string
	Name          string
	Prompt        string
	Auto          bool   // send replies directly instead of suggesting them
	Media         string // MediaText, MediaTags or MediaVision
	ProviderID    int64
	Model         string
	RetentionDays int
	Enabled       bool
	AllChats      bool // answer every DM rather than only the chosen chats
}

// AgentChat is a DM an agent with AllChats off answers.
type AgentChat struct {
	ID    string
	Title string
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

// OpenAgentDB adds the agent tables to the shared wiregram.db handle, which
// its owner closes.
func OpenAgentDB(db *sql.DB, dir string) (*AgentDB, error) {
	d := &AgentDB{db: db}
	// Before v2 the primary account was keyed by the config directory's name.
	if err := d.migrate(filepath.Base(dir)); err != nil {
		return nil, fmt.Errorf("migrate agent database: %w", err)
	}
	return d, nil
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
		func(tx *sql.Tx) error {
			_, err := tx.Exec(schemaV4)
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

// schemaV4 makes each agent part of one account. Agents could serve several
// accounts before, so they start afresh; their history goes with them.
const schemaV4 = `
DELETE FROM agents;
DROP TABLE agent_accounts;
ALTER TABLE agents ADD COLUMN account_key TEXT NOT NULL DEFAULT '';
ALTER TABLE agents ADD COLUMN all_chats INTEGER NOT NULL DEFAULT 1;
CREATE UNIQUE INDEX idx_agents_account ON agents(account_key);

CREATE TABLE agent_chats (
	agent_id INTEGER NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
	chat_id  TEXT NOT NULL,
	title    TEXT NOT NULL DEFAULT '',
	PRIMARY KEY (agent_id, chat_id)
);

CREATE TABLE agent_sent (
	account_key TEXT NOT NULL,
	chat_id     TEXT NOT NULL,
	message_id  TEXT NOT NULL,
	agent_name  TEXT NOT NULL,
	sent_at     TEXT NOT NULL DEFAULT (datetime('now')),
	PRIMARY KEY (account_key, chat_id, message_id)
);
`

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
		`SELECT id, account_key, name, prompt, autoreply, media, provider_id, model,
		 retention_days, enabled, all_chats
		 FROM agents ORDER BY name`,
	)
	if err != nil {
		return nil, fmt.Errorf("list agents: %w", err)
	}
	defer rows.Close()
	var out []AgentRecord
	for rows.Next() {
		var a AgentRecord
		if err := rows.Scan(&a.ID, &a.AccountKey, &a.Name, &a.Prompt, &a.Auto, &a.Media,
			&a.ProviderID, &a.Model, &a.RetentionDays, &a.Enabled, &a.AllChats); err != nil {
			return nil, fmt.Errorf("scan agent: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (d *AgentDB) CreateAgent(a AgentRecord) (int64, error) {
	res, err := d.db.Exec(
		`INSERT INTO agents (account_key, name, prompt, autoreply, media, provider_id, model,
		 retention_days, enabled, all_chats)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		a.AccountKey, a.Name, a.Prompt, a.Auto, a.Media, a.ProviderID, a.Model,
		a.RetentionDays, a.Enabled, a.AllChats,
	)
	if err != nil {
		return 0, fmt.Errorf("create agent: %w", err)
	}
	return res.LastInsertId()
}

func (d *AgentDB) UpdateAgent(a AgentRecord) error {
	_, err := d.db.Exec(
		`UPDATE agents SET name = ?, prompt = ?, autoreply = ?, media = ?, provider_id = ?,
		 model = ?, retention_days = ?, enabled = ?, all_chats = ? WHERE id = ?`,
		a.Name, a.Prompt, a.Auto, a.Media, a.ProviderID, a.Model, a.RetentionDays, a.Enabled,
		a.AllChats, a.ID,
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

// AgentChats lists the DMs the agent answers when AllChats is off.
func (d *AgentDB) AgentChats(agentID int64) ([]AgentChat, error) {
	rows, err := d.db.Query(
		`SELECT chat_id, title FROM agent_chats WHERE agent_id = ? ORDER BY title`, agentID)
	if err != nil {
		return nil, fmt.Errorf("list agent chats: %w", err)
	}
	defer rows.Close()
	var out []AgentChat
	for rows.Next() {
		var c AgentChat
		if err := rows.Scan(&c.ID, &c.Title); err != nil {
			return nil, fmt.Errorf("scan agent chat: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (d *AgentDB) SetAgentChats(agentID int64, chats []AgentChat) error {
	tx, err := d.db.Begin()
	if err != nil {
		return fmt.Errorf("begin agent chats tx: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`DELETE FROM agent_chats WHERE agent_id = ?`, agentID); err != nil {
		return fmt.Errorf("clear agent chats: %w", err)
	}
	for _, c := range chats {
		if _, err := tx.Exec(`INSERT INTO agent_chats (agent_id, chat_id, title) VALUES (?, ?, ?)`,
			agentID, c.ID, c.Title); err != nil {
			return fmt.Errorf("insert agent chat: %w", err)
		}
	}
	return tx.Commit()
}

// DeleteAccount drops the account's agent and the record of what it sent.
func (d *AgentDB) DeleteAccount(key string) error {
	if _, err := d.db.Exec(`DELETE FROM agents WHERE account_key = ?`, key); err != nil {
		return fmt.Errorf("delete account agent: %w", err)
	}
	if _, err := d.db.Exec(`DELETE FROM agent_sent WHERE account_key = ?`, key); err != nil {
		return fmt.Errorf("delete agent sent messages: %w", err)
	}
	return nil
}

// Sent messages. Kept beyond retention so a chat always shows who wrote them.

func (d *AgentDB) RecordSent(accountKey, chatID, messageID, agentName string) error {
	_, err := d.db.Exec(
		`INSERT OR REPLACE INTO agent_sent (account_key, chat_id, message_id, agent_name)
		 VALUES (?, ?, ?, ?)`, accountKey, chatID, messageID, agentName)
	if err != nil {
		return fmt.Errorf("record agent message: %w", err)
	}
	return nil
}

// SentBy maps the IDs of the chat's agent-sent messages to the agent's name.
func (d *AgentDB) SentBy(accountKey, chatID string) (map[string]string, error) {
	rows, err := d.db.Query(
		`SELECT message_id, agent_name FROM agent_sent WHERE account_key = ? AND chat_id = ?`,
		accountKey, chatID)
	if err != nil {
		return nil, fmt.Errorf("list agent messages: %w", err)
	}
	defer rows.Close()
	out := make(map[string]string)
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, fmt.Errorf("scan agent message: %w", err)
		}
		out[id] = name
	}
	return out, rows.Err()
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
