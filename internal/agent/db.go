package agent

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"time"

	"wiregram/internal/backend"
)

const currentDBVersion = 6

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

// AgentRecord is a Chat Agent, a bot in the UI. It belongs to one account and
// answers its incoming DMs, all of them or only the chats in agent_chats; no
// DM has two. Model overrides the provider's default when set.
type AgentRecord struct {
	ID            int64
	AccountKey    string
	Name          string
	Handle        string // unique per account, reserved for registering the bot
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
		func(tx *sql.Tx) error {
			if _, err := tx.Exec(schemaV5); err != nil {
				return err
			}
			if err := fillHandles(tx); err != nil {
				return err
			}
			_, err := tx.Exec(schemaV5Rules)
			return err
		},
		func(tx *sql.Tx) error {
			_, err := tx.Exec(schemaV6)
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

// schemaV6 records the agents registered as bots on their network, with the
// token each signs in with.
const schemaV6 = `
CREATE TABLE agent_registrations (
	agent_id    INTEGER PRIMARY KEY REFERENCES agents(id) ON DELETE CASCADE,
	bot_user_id TEXT NOT NULL,
	username    TEXT NOT NULL,
	token_id    TEXT NOT NULL,
	token       TEXT NOT NULL,
	created_at  TEXT NOT NULL DEFAULT (datetime('now'))
);
`

// schemaV5 lets an account have several agents, each DM answered by at most
// one of them, and gives every agent a handle.
const schemaV5 = `
DROP INDEX idx_agents_account;
ALTER TABLE agents ADD COLUMN handle TEXT NOT NULL DEFAULT '';
ALTER TABLE agent_chats ADD COLUMN account_key TEXT NOT NULL DEFAULT '';
UPDATE agent_chats SET account_key =
	(SELECT account_key FROM agents WHERE agents.id = agent_chats.agent_id);
DELETE FROM agent_chats WHERE agent_id IN (SELECT id FROM agents WHERE all_chats = 1);
`

// schemaV5Rules keeps every DM to one agent: an agent claims all of the
// account's DMs or picks some, and the two exclude each other.
const schemaV5Rules = `
CREATE UNIQUE INDEX idx_agents_handle ON agents(account_key, handle);
CREATE UNIQUE INDEX idx_agents_all ON agents(account_key) WHERE all_chats = 1;
CREATE UNIQUE INDEX idx_agent_chats_owner ON agent_chats(account_key, chat_id);

CREATE TRIGGER agent_chats_all_taken BEFORE INSERT ON agent_chats
WHEN EXISTS (SELECT 1 FROM agents WHERE account_key = NEW.account_key AND all_chats = 1
	AND id != NEW.agent_id)
BEGIN SELECT RAISE(ABORT, 'another bot answers all private chats'); END;

CREATE TRIGGER agents_all_insert BEFORE INSERT ON agents
WHEN NEW.all_chats = 1 AND EXISTS (SELECT 1 FROM agent_chats WHERE account_key = NEW.account_key)
BEGIN SELECT RAISE(ABORT, 'another bot answers some private chats'); END;

CREATE TRIGGER agents_all_update BEFORE UPDATE OF all_chats ON agents
WHEN NEW.all_chats = 1 AND EXISTS (SELECT 1 FROM agent_chats
	WHERE account_key = NEW.account_key AND agent_id != NEW.id)
BEGIN SELECT RAISE(ABORT, 'another bot answers some private chats'); END;
`

// fillHandles gives each existing agent a handle made from its name.
func fillHandles(tx *sql.Tx) error {
	rows, err := tx.Query(`SELECT id, account_key, name FROM agents ORDER BY id`)
	if err != nil {
		return err
	}
	type named struct {
		id        int64
		key, name string
	}
	var list []named
	for rows.Next() {
		var n named
		if err := rows.Scan(&n.id, &n.key, &n.name); err != nil {
			rows.Close()
			return err
		}
		list = append(list, n)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	used := make(map[string]bool)
	for _, n := range list {
		handle := SuggestHandle(n.name)
		for i := 2; used[n.key+"/"+handle]; i++ {
			handle = fmt.Sprintf("%s_%d", SuggestHandle(n.name), i)
		}
		used[n.key+"/"+handle] = true
		if _, err := tx.Exec(`UPDATE agents SET handle = ? WHERE id = ?`, handle, n.id); err != nil {
			return err
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
		`SELECT id, account_key, name, handle, prompt, autoreply, media, provider_id, model,
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
		if err := rows.Scan(&a.ID, &a.AccountKey, &a.Name, &a.Handle, &a.Prompt, &a.Auto,
			&a.Media, &a.ProviderID, &a.Model, &a.RetentionDays, &a.Enabled,
			&a.AllChats); err != nil {
			return nil, fmt.Errorf("scan agent: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// querier is a *sql.DB or a *sql.Tx.
type querier interface {
	Query(query string, args ...any) (*sql.Rows, error)
	QueryRow(query string, args ...any) *sql.Row
}

// checkHandle fails when another of the account's agents has the handle.
func checkHandle(q querier, a AgentRecord) error {
	if err := ValidHandle(a.Handle); err != nil {
		return err
	}
	var name string
	err := q.QueryRow(`SELECT name FROM agents WHERE account_key = ? AND handle = ? AND id != ?`,
		a.AccountKey, a.Handle, a.ID).Scan(&name)
	switch {
	case err == sql.ErrNoRows:
		return nil
	case err != nil:
		return fmt.Errorf("check handle: %w", err)
	}
	return fmt.Errorf("@%s is used by bot %s", a.Handle, name)
}

// CreateAgent saves a new agent. It answers every DM when the account's other
// agents answer none, else no DM until some are picked; a.AllChats is set to say which.
func (d *AgentDB) CreateAgent(a AgentRecord) (AgentRecord, error) {
	tx, err := d.db.Begin()
	if err != nil {
		return a, fmt.Errorf("begin create agent: %w", err)
	}
	defer tx.Rollback()
	if err := checkHandle(tx, a); err != nil {
		return a, err
	}
	claims, err := chatClaims(tx, a.AccountKey)
	if err != nil {
		return a, err
	}
	a.AllChats = claims.All == 0 && len(claims.Chats) == 0
	res, err := tx.Exec(
		`INSERT INTO agents (account_key, name, handle, prompt, autoreply, media, provider_id,
		 model, retention_days, enabled, all_chats)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		a.AccountKey, a.Name, a.Handle, a.Prompt, a.Auto, a.Media, a.ProviderID, a.Model,
		a.RetentionDays, a.Enabled, a.AllChats,
	)
	if err != nil {
		return a, fmt.Errorf("create agent: %w", err)
	}
	if a.ID, err = res.LastInsertId(); err != nil {
		return a, fmt.Errorf("create agent: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return a, fmt.Errorf("create agent: %w", err)
	}
	return a, nil
}

// UpdateAgent saves the agent's settings. Which DMs it answers is changed
// only by SetAgentChats.
func (d *AgentDB) UpdateAgent(a AgentRecord) error {
	if err := checkHandle(d.db, a); err != nil {
		return err
	}
	_, err := d.db.Exec(
		`UPDATE agents SET name = ?, handle = ?, prompt = ?, autoreply = ?, media = ?,
		 provider_id = ?, model = ?, retention_days = ?, enabled = ? WHERE id = ?`,
		a.Name, a.Handle, a.Prompt, a.Auto, a.Media, a.ProviderID, a.Model, a.RetentionDays,
		a.Enabled, a.ID,
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

// ChatClaims says which agent answers each of an account's DMs: one agent
// all of them, or each picked DM the agent that picked it.
type ChatClaims struct {
	All   int64            // the agent answering every DM, or 0
	Chats map[string]int64 // DM ID → the agent that picked it
}

// Owner is the agent answering the DM, or 0.
func (c ChatClaims) Owner(chatID string) int64 {
	if c.All != 0 {
		return c.All
	}
	return c.Chats[chatID]
}

func (d *AgentDB) ChatClaims(accountKey string) (ChatClaims, error) {
	return chatClaims(d.db, accountKey)
}

func chatClaims(q querier, accountKey string) (ChatClaims, error) {
	c := ChatClaims{Chats: make(map[string]int64)}
	err := q.QueryRow(`SELECT id FROM agents WHERE account_key = ? AND all_chats = 1`,
		accountKey).Scan(&c.All)
	if err != nil && err != sql.ErrNoRows {
		return c, fmt.Errorf("read chat claims: %w", err)
	}
	rows, err := q.Query(`SELECT chat_id, agent_id FROM agent_chats WHERE account_key = ?`,
		accountKey)
	if err != nil {
		return c, fmt.Errorf("read chat claims: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var agentID int64
		if err := rows.Scan(&id, &agentID); err != nil {
			return c, fmt.Errorf("scan chat claim: %w", err)
		}
		c.Chats[id] = agentID
	}
	return c, rows.Err()
}

// SetAgentChats makes the agent answer every DM of its account, or only
// chats, in one step. It fails, naming the other agent, when one already
// answers a DM it would take.
func (d *AgentDB) SetAgentChats(agentID int64, all bool, chats []AgentChat) error {
	tx, err := d.db.Begin()
	if err != nil {
		return fmt.Errorf("begin agent chats tx: %w", err)
	}
	defer tx.Rollback()

	var key string
	if err := tx.QueryRow(`SELECT account_key FROM agents WHERE id = ?`, agentID).
		Scan(&key); err != nil {
		return fmt.Errorf("find agent: %w", err)
	}
	claims, err := chatClaims(tx, key)
	if err != nil {
		return err
	}
	name := func(id int64) string {
		n := "another bot"
		if tx.QueryRow(`SELECT 'bot ' || name FROM agents WHERE id = ?`, id).Scan(&n) != nil {
			return "another bot"
		}
		return n
	}
	if claims.All != 0 && claims.All != agentID {
		return fmt.Errorf("%s answers all private chats", name(claims.All))
	}
	for _, owner := range claims.Chats {
		if all && owner != agentID {
			return fmt.Errorf("%s answers some private chats here", name(owner))
		}
	}
	for _, c := range chats {
		if owner := claims.Chats[c.ID]; !all && owner != 0 && owner != agentID {
			return fmt.Errorf("%s answers %s", name(owner), c.Title)
		}
	}

	// Clear the agent's picks before claiming all, and drop all before picking.
	if _, err := tx.Exec(`DELETE FROM agent_chats WHERE agent_id = ?`, agentID); err != nil {
		return fmt.Errorf("clear agent chats: %w", err)
	}
	if _, err := tx.Exec(`UPDATE agents SET all_chats = ? WHERE id = ?`, all, agentID); err != nil {
		return fmt.Errorf("save agent chats: %w", err)
	}
	if !all {
		for _, c := range chats {
			if _, err := tx.Exec(`INSERT INTO agent_chats (agent_id, account_key, chat_id, title)
				VALUES (?, ?, ?, ?)`, agentID, key, c.ID, c.Title); err != nil {
				return fmt.Errorf("insert agent chat: %w", err)
			}
		}
	}
	return tx.Commit()
}

// Registrations maps each agent registered as a bot to its bot account.
func (d *AgentDB) Registrations() (map[int64]backend.BotAccount, error) {
	rows, err := d.db.Query(
		`SELECT agent_id, bot_user_id, username, token_id, token FROM agent_registrations`)
	if err != nil {
		return nil, fmt.Errorf("list bot registrations: %w", err)
	}
	defer rows.Close()
	out := make(map[int64]backend.BotAccount)
	for rows.Next() {
		var id int64
		var b backend.BotAccount
		if err := rows.Scan(&id, &b.UserID, &b.Username, &b.TokenID, &b.Token); err != nil {
			return nil, fmt.Errorf("scan bot registration: %w", err)
		}
		out[id] = b
	}
	return out, rows.Err()
}

// SetRegistration records the agent's bot account, replacing any before.
func (d *AgentDB) SetRegistration(agentID int64, b backend.BotAccount) error {
	_, err := d.db.Exec(`INSERT OR REPLACE INTO agent_registrations
		(agent_id, bot_user_id, username, token_id, token) VALUES (?, ?, ?, ?, ?)`,
		agentID, b.UserID, b.Username, b.TokenID, b.Token)
	if err != nil {
		return fmt.Errorf("save bot registration: %w", err)
	}
	return nil
}

func (d *AgentDB) DeleteRegistration(agentID int64) error {
	if _, err := d.db.Exec(`DELETE FROM agent_registrations WHERE agent_id = ?`,
		agentID); err != nil {
		return fmt.Errorf("delete bot registration: %w", err)
	}
	return nil
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
