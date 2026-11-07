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
	StatusRaw        Status = "raw"
	StatusPrefiltered Status = "prefiltered"
	StatusClassified Status = "classified"
	StatusScored     Status = "scored"
	StatusNotified   Status = "notified"
	StatusDrafted    Status = "drafted"
	StatusIgnored    Status = "ignored"
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
	`
	_, err := s.db.ExecContext(ctx, schema)
	return err
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

func (s *Store) SaveClassification(ctx context.Context, id int64, isHiring bool, category string, confidence float64, newStatus Status) error {
	query := `
	UPDATE posts
	SET is_hiring = ?, category = ?, confidence = ?, status = ?
	WHERE id = ?;
	`
	_, err := s.db.ExecContext(ctx, query, isHiring, category, confidence, newStatus, id)
	return err
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
