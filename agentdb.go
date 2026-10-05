package main

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

const currentDBVersion = 1

type agentProvider struct {
	ID        int64
	Name      string
	BaseURL   string
	APIKeyEnv string
	Model     string
}

type agentRecord struct {
	ID            int64
	Name          string
	Prompt        string
	AutoReply     int
	ProviderID    int64
	RetentionDays int
	Enabled       bool
}

type agentChatAssignment struct {
	BackendType string
	AccountID   string
	ChatID      string
}

type agentLoggedMessage struct {
	MessageID    string
	Sender       string
	SenderID     string
	Text         string
	Timestamp    time.Time
	Outgoing     bool
	IsAgentReply bool
}

type agentDB struct {
	db *sql.DB
}

func openAgentDB(dir string) (*agentDB, error) {
	path := filepath.Join(dir, "wiregram.db")
	db, err := sql.Open("sqlite", "file:"+path+"?_foreign_keys=on&_journal_mode=wal")
	if err != nil {
		return nil, fmt.Errorf("open agent database: %w", err)
	}
	d := &agentDB{db: db}
	if err := d.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate agent database: %w", err)
	}
	return d, nil
}

func (d *agentDB) close() error {
	return d.db.Close()
}

func (d *agentDB) migrate() error {
	_, err := d.db.Exec(`CREATE TABLE IF NOT EXISTS migrations (version INTEGER NOT NULL)`)
	if err != nil {
		return fmt.Errorf("create migrations table: %w", err)
	}

	var version int
	row := d.db.QueryRow(`SELECT version FROM migrations LIMIT 1`)
	if err := row.Scan(&version); err != nil {
		if err == sql.ErrNoRows {
			version = 0
		} else {
			return fmt.Errorf("read migration version: %w", err)
		}
	}

	if version < 1 {
		if _, err := d.db.Exec(schemaV1); err != nil {
			return fmt.Errorf("apply schema v1: %w", err)
		}
	}

	if version == 0 {
		_, err = d.db.Exec(`INSERT INTO migrations (version) VALUES (?)`, currentDBVersion)
	} else if version < currentDBVersion {
		_, err = d.db.Exec(`UPDATE migrations SET version = ?`, currentDBVersion)
	}
	return err
}

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

func (d *agentDB) listProviders() ([]agentProvider, error) {
	rows, err := d.db.Query(`SELECT id, name, base_url, api_key_env, model FROM providers ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list providers: %w", err)
	}
	defer rows.Close()
	var out []agentProvider
	for rows.Next() {
		var p agentProvider
		if err := rows.Scan(&p.ID, &p.Name, &p.BaseURL, &p.APIKeyEnv, &p.Model); err != nil {
			return nil, fmt.Errorf("scan provider: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (d *agentDB) createProvider(p agentProvider) (int64, error) {
	res, err := d.db.Exec(
		`INSERT INTO providers (name, base_url, api_key_env, model) VALUES (?, ?, ?, ?)`,
		p.Name, p.BaseURL, p.APIKeyEnv, p.Model,
	)
	if err != nil {
		return 0, fmt.Errorf("create provider: %w", err)
	}
	return res.LastInsertId()
}

func (d *agentDB) updateProvider(p agentProvider) error {
	_, err := d.db.Exec(
		`UPDATE providers SET name = ?, base_url = ?, api_key_env = ?, model = ? WHERE id = ?`,
		p.Name, p.BaseURL, p.APIKeyEnv, p.Model, p.ID,
	)
	if err != nil {
		return fmt.Errorf("update provider: %w", err)
	}
	return nil
}

func (d *agentDB) deleteProvider(id int64) error {
	_, err := d.db.Exec(`DELETE FROM providers WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete provider: %w", err)
	}
	return nil
}

func (d *agentDB) getProvider(id int64) (agentProvider, error) {
	var p agentProvider
	err := d.db.QueryRow(
		`SELECT id, name, base_url, api_key_env, model FROM providers WHERE id = ?`, id,
	).Scan(&p.ID, &p.Name, &p.BaseURL, &p.APIKeyEnv, &p.Model)
	if err != nil {
		return p, fmt.Errorf("get provider: %w", err)
	}
	return p, nil
}

// Agent CRUD.

func (d *agentDB) listAgents() ([]agentRecord, error) {
	rows, err := d.db.Query(
		`SELECT id, name, prompt, autoreply, provider_id, retention_days, enabled FROM agents ORDER BY name`,
	)
	if err != nil {
		return nil, fmt.Errorf("list agents: %w", err)
	}
	defer rows.Close()
	var out []agentRecord
	for rows.Next() {
		var a agentRecord
		if err := rows.Scan(&a.ID, &a.Name, &a.Prompt, &a.AutoReply, &a.ProviderID, &a.RetentionDays, &a.Enabled); err != nil {
			return nil, fmt.Errorf("scan agent: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (d *agentDB) createAgent(a agentRecord) (int64, error) {
	res, err := d.db.Exec(
		`INSERT INTO agents (name, prompt, autoreply, provider_id, retention_days, enabled) VALUES (?, ?, ?, ?, ?, ?)`,
		a.Name, a.Prompt, a.AutoReply, a.ProviderID, a.RetentionDays, a.Enabled,
	)
	if err != nil {
		return 0, fmt.Errorf("create agent: %w", err)
	}
	return res.LastInsertId()
}

func (d *agentDB) updateAgent(a agentRecord) error {
	_, err := d.db.Exec(
		`UPDATE agents SET name = ?, prompt = ?, autoreply = ?, provider_id = ?, retention_days = ?, enabled = ? WHERE id = ?`,
		a.Name, a.Prompt, a.AutoReply, a.ProviderID, a.RetentionDays, a.Enabled, a.ID,
	)
	if err != nil {
		return fmt.Errorf("update agent: %w", err)
	}
	return nil
}

func (d *agentDB) deleteAgent(id int64) error {
	_, err := d.db.Exec(`DELETE FROM agents WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete agent: %w", err)
	}
	return nil
}

// Agent knowledge.

func (d *agentDB) agentKnowledge(agentID int64) ([]string, error) {
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

func (d *agentDB) setAgentKnowledge(agentID int64, paths []string) error {
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

// Agent chat assignments.

func (d *agentDB) agentChats(agentID int64) ([]agentChatAssignment, error) {
	rows, err := d.db.Query(
		`SELECT backend_type, account_id, chat_id FROM agent_chats WHERE agent_id = ?`, agentID,
	)
	if err != nil {
		return nil, fmt.Errorf("list agent chats: %w", err)
	}
	defer rows.Close()
	var out []agentChatAssignment
	for rows.Next() {
		var c agentChatAssignment
		if err := rows.Scan(&c.BackendType, &c.AccountID, &c.ChatID); err != nil {
			return nil, fmt.Errorf("scan agent chat: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (d *agentDB) setAgentChats(agentID int64, chats []agentChatAssignment) error {
	tx, err := d.db.Begin()
	if err != nil {
		return fmt.Errorf("begin chats tx: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`DELETE FROM agent_chats WHERE agent_id = ?`, agentID); err != nil {
		return fmt.Errorf("clear agent chats: %w", err)
	}
	for _, c := range chats {
		if _, err := tx.Exec(
			`INSERT INTO agent_chats (agent_id, backend_type, account_id, chat_id) VALUES (?, ?, ?, ?)`,
			agentID, c.BackendType, c.AccountID, c.ChatID,
		); err != nil {
			return fmt.Errorf("insert agent chat: %w", err)
		}
	}
	return tx.Commit()
}

// Message log.

func (d *agentDB) logMessage(agentID int64, backendType, accountID, chatIDStr string, msg message, isAgentReply bool) error {
	_, err := d.db.Exec(
		`INSERT INTO agent_messages (agent_id, backend_type, account_id, chat_id, message_id, sender, sender_id, text, timestamp, outgoing, is_agent_reply)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		agentID, backendType, accountID, chatIDStr,
		msg.ID, msg.Sender, msg.SenderID, msg.Text,
		msg.Timestamp.UTC().Format(time.RFC3339),
		msg.Outgoing, isAgentReply,
	)
	if err != nil {
		return fmt.Errorf("log message: %w", err)
	}
	return nil
}

func (d *agentDB) chatHistory(agentID int64, backendType, accountID, chatIDStr string, limit int) ([]agentLoggedMessage, error) {
	rows, err := d.db.Query(
		`SELECT message_id, sender, sender_id, text, timestamp, outgoing, is_agent_reply
		 FROM agent_messages
		 WHERE agent_id = ? AND backend_type = ? AND account_id = ? AND chat_id = ?
		 ORDER BY timestamp DESC
		 LIMIT ?`,
		agentID, backendType, accountID, chatIDStr, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("chat history: %w", err)
	}
	defer rows.Close()
	var out []agentLoggedMessage
	for rows.Next() {
		var m agentLoggedMessage
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

func (d *agentDB) purgeOldMessages(retentionDays int) (int64, error) {
	cutoff := time.Now().UTC().AddDate(0, 0, -retentionDays).Format(time.RFC3339)
	res, err := d.db.Exec(`DELETE FROM agent_messages WHERE created_at < ?`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("purge old messages: %w", err)
	}
	return res.RowsAffected()
}

// Thread listing for agent UI.

type agentThread struct {
	BackendType  string
	AccountID    string
	ChatID       string
	LastAt       time.Time
	MessageCount int
}

func (d *agentDB) agentThreads(agentID int64) ([]agentThread, error) {
	rows, err := d.db.Query(
		`SELECT backend_type, account_id, chat_id, MAX(timestamp) as last_at, COUNT(*) as cnt
		 FROM agent_messages
		 WHERE agent_id = ?
		 GROUP BY backend_type, account_id, chat_id
		 ORDER BY last_at DESC`,
		agentID,
	)
	if err != nil {
		return nil, fmt.Errorf("list agent threads: %w", err)
	}
	defer rows.Close()
	var out []agentThread
	for rows.Next() {
		var t agentThread
		var ts string
		if err := rows.Scan(&t.BackendType, &t.AccountID, &t.ChatID, &ts, &t.MessageCount); err != nil {
			return nil, fmt.Errorf("scan agent thread: %w", err)
		}
		t.LastAt, _ = time.Parse(time.RFC3339, ts)
		out = append(out, t)
	}
	return out, rows.Err()
}
