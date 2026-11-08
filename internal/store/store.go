package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

type Status string

const (
	StatusNew         Status = "new"
	StatusFilteredOut Status = "filtered_out"
	StatusToClassify  Status = "to_classify"

	// Legacy / downstream statuses
	StatusRaw         Status = "raw"
	StatusPrefiltered Status = "prefiltered"
	StatusClassified  Status = "classified"
	StatusScored      Status = "scored"
	StatusNotified    Status = "notified"
	StatusDrafted     Status = "drafted"
	StatusIgnored     Status = "ignored"
)

type Post struct {
	ID          int64     `json:"id"`
	Source      string    `json:"source"`
	ExternalID  string    `json:"external_id"`
	URL         string    `json:"url"`
	Title       string    `json:"title"`
	Author      string    `json:"author"`
	Body        string    `json:"body"`
	PublishedAt time.Time `json:"published_at"`
	CreatedAt   time.Time `json:"created_at"`
	Status      Status    `json:"status"`

	// Classification & Scoring
	IsHiring     bool    `json:"is_hiring"`
	Category     string  `json:"category"`     // e.g. "web_development", "design", "other"
	Confidence   float64 `json:"confidence"`   // 0.0 to 1.0
	Score        int     `json:"score"`        // e.g. 0 to 100
	ScoreReason  string  `json:"score_reason"`

	// Notification & Draft
	NotifiedAt   *time.Time `json:"notified_at,omitempty"`
	DraftReply   string     `json:"draft_reply,omitempty"`
	DraftedAt    *time.Time `json:"drafted_at,omitempty"`
}

type Store struct {
	db *sql.DB
}

func Open(path string) (*Store, error) {
	// WAL mode and busy timeout for concurrent safety
	dsn := fmt.Sprintf("%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite db: %w", err)
	}

	s := &Store{db: db}
	if err := s.migrate(context.Background()); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return s, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) migrate(ctx context.Context) error {
	schema := `
	CREATE TABLE IF NOT EXISTS posts (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		source TEXT NOT NULL,
		external_id TEXT NOT NULL,
		url TEXT NOT NULL,
		title TEXT NOT NULL,
		author TEXT NOT NULL,
		body TEXT NOT NULL,
		published_at TIMESTAMP NOT NULL,
		created_at TIMESTAMP NOT NULL,
		status TEXT NOT NULL,
		is_hiring BOOLEAN NOT NULL DEFAULT 0,
		category TEXT NOT NULL DEFAULT '',
		confidence REAL NOT NULL DEFAULT 0.0,
		score INTEGER NOT NULL DEFAULT 0,
		score_reason TEXT NOT NULL DEFAULT '',
		notified_at TIMESTAMP,
		draft_reply TEXT NOT NULL DEFAULT '',
		drafted_at TIMESTAMP,
		UNIQUE(source, external_id)
	);

	CREATE INDEX IF NOT EXISTS idx_posts_status ON posts(status);
	CREATE INDEX IF NOT EXISTS idx_posts_published ON posts(published_at);

	CREATE TABLE IF NOT EXISTS sources (
		name TEXT PRIMARY KEY,
		last_polled_at TIMESTAMP NOT NULL
	);

	CREATE TABLE IF NOT EXISTS classifications (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		post_id INTEGER NOT NULL UNIQUE,
		is_request BOOLEAN NOT NULL,
		project_type TEXT NOT NULL,
		budget_amount REAL,
		budget_currency TEXT,
		urgency TEXT NOT NULL,
		remote_ok BOOLEAN,
		location_note TEXT,
		tech TEXT NOT NULL,
		scam_score INTEGER NOT NULL,
		confidence INTEGER NOT NULL,
		reason TEXT NOT NULL,
		created_at TIMESTAMP NOT NULL,
		FOREIGN KEY(post_id) REFERENCES posts(id) ON DELETE CASCADE
	);

	CREATE TABLE IF NOT EXISTS outreach (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		post_id INTEGER NOT NULL UNIQUE,
		channel TEXT NOT NULL,
		draft_reply TEXT NOT NULL,
		status TEXT NOT NULL DEFAULT 'drafted',
		follow_up_due TIMESTAMP,
		created_at TIMESTAMP NOT NULL,
		updated_at TIMESTAMP NOT NULL,
		FOREIGN KEY(post_id) REFERENCES posts(id) ON DELETE CASCADE
	);

	CREATE TABLE IF NOT EXISTS portfolio (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		title TEXT NOT NULL,
		url TEXT NOT NULL,
		description TEXT NOT NULL,
		tags TEXT NOT NULL
	);

	CREATE TABLE IF NOT EXISTS profile_settings (
		id INTEGER PRIMARY KEY CHECK (id = 1),
		services TEXT NOT NULL,
		rate_floor REAL NOT NULL,
		location TEXT NOT NULL,
		tone TEXT NOT NULL
	);
	`
	_, err := s.db.ExecContext(ctx, schema)
	return err
}

// GetSourceLastPolledAt returns the timestamp when a source was last successfully polled.
func (s *Store) GetSourceLastPolledAt(ctx context.Context, name string) (time.Time, error) {
	var t time.Time
	err := s.db.QueryRowContext(ctx, "SELECT last_polled_at FROM sources WHERE name = ?", name).Scan(&t)
	if err == sql.ErrNoRows {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, err
	}
	return t.UTC(), nil
}

// SetSourceLastPolledAt sets the timestamp cursor for a source.
func (s *Store) SetSourceLastPolledAt(ctx context.Context, name string, t time.Time) error {
	query := `
	INSERT INTO sources (name, last_polled_at)
	VALUES (?, ?)
	ON CONFLICT(name) DO UPDATE SET last_polled_at = excluded.last_polled_at;
	`
	_, err := s.db.ExecContext(ctx, query, name, t.UTC())
	return err
}

// InsertPostIfNew inserts a post with status "new" only if (source, external_id) does not exist.
// Returns true if inserted, false if already existing.
func (s *Store) InsertPostIfNew(ctx context.Context, p *Post) (bool, error) {
	p.Status = StatusNew
	return s.UpsertRaw(ctx, p)
}

func (s *Store) UpsertRaw(ctx context.Context, p *Post) (bool, error) {
	query := `
	INSERT INTO posts (
		source, external_id, url, title, author, body, published_at, created_at, status
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(source, external_id) DO NOTHING;
	`
	now := time.Now().UTC()
	if p.CreatedAt.IsZero() {
		p.CreatedAt = now
	}
	if p.Status == "" {
		p.Status = StatusRaw
	}

	res, err := s.db.ExecContext(ctx, query,
		p.Source, p.ExternalID, p.URL, p.Title, p.Author, p.Body, p.PublishedAt.UTC(), p.CreatedAt, p.Status,
	)
	if err != nil {
		return false, err
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return rows > 0, nil
}

func (s *Store) GetPostsByStatus(ctx context.Context, status Status, limit int) ([]*Post, error) {
	query := `
	SELECT id, source, external_id, url, title, author, body, published_at, created_at, status,
	       is_hiring, category, confidence, score, score_reason, notified_at, draft_reply, drafted_at
	FROM posts
	WHERE status = ?
	ORDER BY published_at DESC
	LIMIT ?;
	`
	rows, err := s.db.QueryContext(ctx, query, status, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var posts []*Post
	for rows.Next() {
		var p Post
		var notifiedAt, draftedAt sql.NullTime
		if err := rows.Scan(
			&p.ID, &p.Source, &p.ExternalID, &p.URL, &p.Title, &p.Author, &p.Body,
			&p.PublishedAt, &p.CreatedAt, &p.Status,
			&p.IsHiring, &p.Category, &p.Confidence, &p.Score, &p.ScoreReason,
			&notifiedAt, &p.DraftReply, &draftedAt,
		); err != nil {
			return nil, err
		}
		if notifiedAt.Valid {
			p.NotifiedAt = &notifiedAt.Time
		}
		if draftedAt.Valid {
			p.DraftedAt = &draftedAt.Time
		}
		posts = append(posts, &p)
	}
	return posts, rows.Err()
}

func (s *Store) UpdateStatus(ctx context.Context, id int64, status Status) error {
	_, err := s.db.ExecContext(ctx, "UPDATE posts SET status = ? WHERE id = ?", status, id)
	return err
}

// ClassificationRecord stores detailed Anthropic Messages API classification output.
type ClassificationRecord struct {
	ID             int64
	PostID         int64
	IsRequest      bool
	ProjectType    string
	BudgetAmount   *float64
	BudgetCurrency *string
	Urgency        string
	RemoteOK       *bool
	LocationNote   *string
	Tech           string
	ScamScore      int
	Confidence     int
	Reason         string
	CreatedAt      time.Time
}

// SaveDetailedClassification stores the classification details into the classifications table and updates post status.
func (s *Store) SaveDetailedClassification(ctx context.Context, rec ClassificationRecord, postStatus Status) error {
	now := time.Now().UTC()
	if rec.CreatedAt.IsZero() {
		rec.CreatedAt = now
	}

	query := `
	INSERT INTO classifications (
		post_id, is_request, project_type, budget_amount, budget_currency,
		urgency, remote_ok, location_note, tech, scam_score, confidence, reason, created_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(post_id) DO UPDATE SET
		is_request = excluded.is_request,
		project_type = excluded.project_type,
		budget_amount = excluded.budget_amount,
		budget_currency = excluded.budget_currency,
		urgency = excluded.urgency,
		remote_ok = excluded.remote_ok,
		location_note = excluded.location_note,
		tech = excluded.tech,
		scam_score = excluded.scam_score,
		confidence = excluded.confidence,
		reason = excluded.reason,
		created_at = excluded.created_at;
	`
	_, err := s.db.ExecContext(ctx, query,
		rec.PostID, rec.IsRequest, rec.ProjectType, rec.BudgetAmount, rec.BudgetCurrency,
		rec.Urgency, rec.RemoteOK, rec.LocationNote, rec.Tech, rec.ScamScore, rec.Confidence, rec.Reason, rec.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("insert classification: %w", err)
	}

	// Also sync summary fields on the posts record and set status
	updateQuery := `
	UPDATE posts
	SET is_hiring = ?, category = ?, confidence = ?, status = ?
	WHERE id = ?;
	`
	confFloat := float64(rec.Confidence) / 100.0
	_, err = s.db.ExecContext(ctx, updateQuery, rec.IsRequest, rec.ProjectType, confFloat, postStatus, rec.PostID)
	return err
}

// PortfolioItem represents an authentic completed project used in proposals.
type PortfolioItem struct {
	ID          int64  `json:"id"`
	Title       string `json:"title"`
	URL         string `json:"url"`
	Description string `json:"description"`
	Tags        string `json:"tags"` // comma-separated tags
}

// ProfileConfig represents developer profile settings.
type ProfileConfig struct {
	Services  string  `json:"services"`
	RateFloor float64 `json:"rate_floor"`
	Location  string  `json:"location"`
	Tone      string  `json:"tone"`
}

// OutreachRecord tracks communication state with leads.
type OutreachRecord struct {
	ID          int64      `json:"id"`
	PostID      int64      `json:"post_id"`
	Channel     string     `json:"channel"`
	DraftReply  string     `json:"draft_reply"`
	Status      string     `json:"status"` // drafted, contacted, replied, won, lost, ignored
	FollowUpDue *time.Time `json:"follow_up_due,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

func (s *Store) SaveOutreachDraft(ctx context.Context, postID int64, channel, draft string) error {
	now := time.Now().UTC()
	query := `
	INSERT INTO outreach (post_id, channel, draft_reply, status, created_at, updated_at)
	VALUES (?, ?, ?, 'drafted', ?, ?)
	ON CONFLICT(post_id) DO UPDATE SET
		channel = excluded.channel,
		draft_reply = excluded.draft_reply,
		updated_at = excluded.updated_at;
	`
	_, err := s.db.ExecContext(ctx, query, postID, channel, draft, now, now)
	return err
}

func (s *Store) GetOutreach(ctx context.Context, postID int64) (*OutreachRecord, error) {
	query := `
	SELECT id, post_id, channel, draft_reply, status, follow_up_due, created_at, updated_at
	FROM outreach
	WHERE post_id = ?;
	`
	var o OutreachRecord
	var followUp sql.NullTime
	err := s.db.QueryRowContext(ctx, query, postID).Scan(
		&o.ID, &o.PostID, &o.Channel, &o.DraftReply, &o.Status, &followUp, &o.CreatedAt, &o.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if followUp.Valid {
		o.FollowUpDue = &followUp.Time
	}
	return &o, nil
}

func (s *Store) MarkOutreachStatus(ctx context.Context, postID int64, status string) error {
	now := time.Now().UTC()
	var followUp *time.Time
	if status == "contacted" {
		t := now.Add(3 * 24 * time.Hour)
		followUp = &t
	}

	query := `
	UPDATE outreach
	SET status = ?, follow_up_due = ?, updated_at = ?
	WHERE post_id = ?;
	`
	res, err := s.db.ExecContext(ctx, query, status, followUp, now, postID)
	if err != nil {
		return err
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		// Insert outreach if didn't exist
		insQuery := `
		INSERT INTO outreach (post_id, channel, draft_reply, status, follow_up_due, created_at, updated_at)
		VALUES (?, 'direct', '', ?, ?, ?, ?);
		`
		_, err = s.db.ExecContext(ctx, insQuery, postID, status, followUp, now, now)
	}
	return err
}

func (s *Store) AddPortfolioItem(ctx context.Context, item PortfolioItem) error {
	query := `
	INSERT INTO portfolio (title, url, description, tags)
	VALUES (?, ?, ?, ?);
	`
	_, err := s.db.ExecContext(ctx, query, item.Title, item.URL, item.Description, item.Tags)
	return err
}

func (s *Store) ListPortfolioItems(ctx context.Context) ([]PortfolioItem, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT id, title, url, description, tags FROM portfolio")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []PortfolioItem
	for rows.Next() {
		var it PortfolioItem
		if err := rows.Scan(&it.ID, &it.Title, &it.URL, &it.Description, &it.Tags); err != nil {
			return nil, err
		}
		items = append(items, it)
	}
	return items, rows.Err()
}

func (s *Store) SetProfileSettings(ctx context.Context, p ProfileConfig) error {
	query := `
	INSERT INTO profile_settings (id, services, rate_floor, location, tone)
	VALUES (1, ?, ?, ?, ?)
	ON CONFLICT(id) DO UPDATE SET
		services = excluded.services,
		rate_floor = excluded.rate_floor,
		location = excluded.location,
		tone = excluded.tone;
	`
	_, err := s.db.ExecContext(ctx, query, p.Services, p.RateFloor, p.Location, p.Tone)
	return err
}

func (s *Store) GetProfileSettings(ctx context.Context) (*ProfileConfig, error) {
	query := "SELECT services, rate_floor, location, tone FROM profile_settings WHERE id = 1"
	var p ProfileConfig
	err := s.db.QueryRowContext(ctx, query).Scan(&p.Services, &p.RateFloor, &p.Location, &p.Tone)
	if err == sql.ErrNoRows {
		return &ProfileConfig{
			Services:  "web_app, ecommerce, landing_site, maintenance",
			RateFloor: 500.0,
			Location:  "remote, worldwide",
			Tone:      "professional and concise",
		}, nil
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

func (s *Store) GetPostByID(ctx context.Context, id int64) (*Post, error) {
	query := `
	SELECT id, source, external_id, url, title, author, body, published_at, created_at, status,
	       is_hiring, category, confidence, score, score_reason, notified_at, draft_reply, drafted_at
	FROM posts
	WHERE id = ?;
	`
	var p Post
	var notifiedAt, draftedAt sql.NullTime
	err := s.db.QueryRowContext(ctx, query, id).Scan(
		&p.ID, &p.Source, &p.ExternalID, &p.URL, &p.Title, &p.Author, &p.Body,
		&p.PublishedAt, &p.CreatedAt, &p.Status,
		&p.IsHiring, &p.Category, &p.Confidence, &p.Score, &p.ScoreReason,
		&notifiedAt, &p.DraftReply, &draftedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if notifiedAt.Valid {
		p.NotifiedAt = &notifiedAt.Time
	}
	if draftedAt.Valid {
		p.DraftedAt = &draftedAt.Time
	}
	return &p, nil
}

func (s *Store) ListLeadsFiltered(ctx context.Context, status string, minScore int, limit int) ([]*Post, error) {
	baseQuery := `
	SELECT id, source, external_id, url, title, author, body, published_at, created_at, status,
	       is_hiring, category, confidence, score, score_reason, notified_at, draft_reply, drafted_at
	FROM posts
	WHERE is_hiring = 1 AND score >= ?
	`
	args := []interface{}{minScore}
	if status != "" && status != "all" {
		baseQuery += " AND status = ?"
		args = append(args, status)
	}
	baseQuery += " ORDER BY published_at DESC LIMIT ?"
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, baseQuery, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var posts []*Post
	for rows.Next() {
		var p Post
		var notifiedAt, draftedAt sql.NullTime
		if err := rows.Scan(
			&p.ID, &p.Source, &p.ExternalID, &p.URL, &p.Title, &p.Author, &p.Body,
			&p.PublishedAt, &p.CreatedAt, &p.Status,
			&p.IsHiring, &p.Category, &p.Confidence, &p.Score, &p.ScoreReason,
			&notifiedAt, &p.DraftReply, &draftedAt,
		); err != nil {
			return nil, err
		}
		if notifiedAt.Valid {
			p.NotifiedAt = &notifiedAt.Time
		}
		if draftedAt.Valid {
			p.DraftedAt = &draftedAt.Time
		}
		posts = append(posts, &p)
	}
	return posts, rows.Err()
}
func (s *Store) SaveClassification(ctx context.Context, id int64, isHiring bool, category string, confidence float64, newStatus Status) error {
	query := `
	UPDATE posts
	SET is_hiring = ?, category = ?, confidence = ?, status = ?
	WHERE id = ?;
	`
	_, err := s.db.ExecContext(ctx, query, isHiring, category, confidence, newStatus, id)
	return err
}

func (s *Store) GetDetailedClassification(ctx context.Context, postID int64) (*ClassificationRecord, error) {
	query := `
	SELECT id, post_id, is_request, project_type, budget_amount, budget_currency,
	       urgency, remote_ok, location_note, tech, scam_score, confidence, reason, created_at
	FROM classifications
	WHERE post_id = ?;
	`
	var rec ClassificationRecord
	var budgetAmount sql.NullFloat64
	var budgetCurrency, locationNote sql.NullString
	var remoteOK sql.NullBool

	err := s.db.QueryRowContext(ctx, query, postID).Scan(
		&rec.ID, &rec.PostID, &rec.IsRequest, &rec.ProjectType, &budgetAmount, &budgetCurrency,
		&rec.Urgency, &remoteOK, &locationNote, &rec.Tech, &rec.ScamScore, &rec.Confidence, &rec.Reason, &rec.CreatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	if budgetAmount.Valid {
		rec.BudgetAmount = &budgetAmount.Float64
	}
	if budgetCurrency.Valid {
		rec.BudgetCurrency = &budgetCurrency.String
	}
	if locationNote.Valid {
		rec.LocationNote = &locationNote.String
	}
	if remoteOK.Valid {
		rec.RemoteOK = &remoteOK.Bool
	}

	return &rec, nil
}

func (s *Store) SaveScore(ctx context.Context, id int64, score int, reason string, newStatus Status) error {
	query := `
	UPDATE posts
	SET score = ?, score_reason = ?, status = ?
	WHERE id = ?;
	`
	_, err := s.db.ExecContext(ctx, query, score, reason, newStatus, id)
	return err
}

func (s *Store) MarkNotified(ctx context.Context, id int64, notifiedAt time.Time, newStatus Status) error {
	query := `
	UPDATE posts
	SET notified_at = ?, status = ?
	WHERE id = ?;
	`
	_, err := s.db.ExecContext(ctx, query, notifiedAt.UTC(), newStatus, id)
	return err
}

func (s *Store) SaveDraft(ctx context.Context, id int64, draft string, draftedAt time.Time, newStatus Status) error {
	query := `
	UPDATE posts
	SET draft_reply = ?, drafted_at = ?, status = ?
	WHERE id = ?;
	`
	_, err := s.db.ExecContext(ctx, query, draft, draftedAt.UTC(), newStatus, id)
	return err
}

func (s *Store) ListLatestLeads(ctx context.Context, minScore int, limit int) ([]*Post, error) {
	query := `
	SELECT id, source, external_id, url, title, author, body, published_at, created_at, status,
	       is_hiring, category, confidence, score, score_reason, notified_at, draft_reply, drafted_at
	FROM posts
	WHERE is_hiring = 1 AND score >= ?
	ORDER BY published_at DESC
	LIMIT ?;
	`
	rows, err := s.db.QueryContext(ctx, query, minScore, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var posts []*Post
	for rows.Next() {
		var p Post
		var notifiedAt, draftedAt sql.NullTime
		if err := rows.Scan(
			&p.ID, &p.Source, &p.ExternalID, &p.URL, &p.Title, &p.Author, &p.Body,
			&p.PublishedAt, &p.CreatedAt, &p.Status,
			&p.IsHiring, &p.Category, &p.Confidence, &p.Score, &p.ScoreReason,
			&notifiedAt, &p.DraftReply, &draftedAt,
		); err != nil {
			return nil, err
		}
		if notifiedAt.Valid {
			p.NotifiedAt = &notifiedAt.Time
		}
		if draftedAt.Valid {
			p.DraftedAt = &draftedAt.Time
		}
		posts = append(posts, &p)
	}
	return posts, rows.Err()
}

func (s *Store) TotalCount(ctx context.Context) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM posts").Scan(&count)
	return count, err
}

// CleanText removes common noise or excessive whitespace
func CleanText(txt string) string {
	return strings.TrimSpace(txt)
}
