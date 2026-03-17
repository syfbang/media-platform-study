package repository

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/media-service/media-platform/internal/telemetry"
	_ "github.com/lib/pq"
	"go.opentelemetry.io/otel/attribute"
)

type Media struct {
	ID          string
	Filename    string
	S3Key       string
	ContentType string
	Size        int64
	Status      string // uploaded, transcoding, ready, failed
	HLSKey      string
	DASHKey     string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type Repository struct {
	db *sql.DB
}

func New(dsn string) (*Repository, error) {
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	db.SetMaxOpenConns(25)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(5 * time.Minute)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		return nil, fmt.Errorf("ping db: %w", err)
	}

	r := &Repository{db: db}
	if err := r.migrate(ctx); err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return r, nil
}

func (r *Repository) migrate(ctx context.Context) error {
	_, err := r.db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS media (
			id           TEXT PRIMARY KEY,
			filename     TEXT NOT NULL,
			s3_key       TEXT NOT NULL,
			content_type TEXT NOT NULL DEFAULT '',
			size         BIGINT NOT NULL DEFAULT 0,
			status       TEXT NOT NULL DEFAULT 'uploaded',
			hls_key      TEXT NOT NULL DEFAULT '',
			dash_key     TEXT NOT NULL DEFAULT '',
			created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`)
	return err
}

func (r *Repository) Create(ctx context.Context, m *Media) error {
	ctx, span := telemetry.Tracer.Start(ctx, "db.create_media")
	defer span.End()
	span.SetAttributes(attribute.String("db.media_id", m.ID))

	_, err := r.db.ExecContext(ctx,
		`INSERT INTO media (id, filename, s3_key, content_type, size, status) VALUES ($1,$2,$3,$4,$5,$6)`,
		m.ID, m.Filename, m.S3Key, m.ContentType, m.Size, m.Status)
	return err
}

func (r *Repository) GetByID(ctx context.Context, id string) (*Media, error) {
	m := &Media{}
	err := r.db.QueryRowContext(ctx,
		`SELECT id, filename, s3_key, content_type, size, status, hls_key, dash_key, created_at, updated_at FROM media WHERE id=$1`, id).
		Scan(&m.ID, &m.Filename, &m.S3Key, &m.ContentType, &m.Size, &m.Status, &m.HLSKey, &m.DASHKey, &m.CreatedAt, &m.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return m, nil
}

func (r *Repository) List(ctx context.Context, limit, offset int) ([]*Media, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, filename, s3_key, content_type, size, status, hls_key, dash_key, created_at, updated_at FROM media ORDER BY created_at DESC LIMIT $1 OFFSET $2`,
		limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []*Media
	for rows.Next() {
		m := &Media{}
		if err := rows.Scan(&m.ID, &m.Filename, &m.S3Key, &m.ContentType, &m.Size, &m.Status, &m.HLSKey, &m.DASHKey, &m.CreatedAt, &m.UpdatedAt); err != nil {
			return nil, err
		}
		list = append(list, m)
	}
	return list, rows.Err()
}

func (r *Repository) UpdateStatus(ctx context.Context, id, status string) error {
	ctx, span := telemetry.Tracer.Start(ctx, "db.update_status")
	defer span.End()
	span.SetAttributes(attribute.String("db.media_id", id), attribute.String("db.status", status))

	_, err := r.db.ExecContext(ctx,
		`UPDATE media SET status=$1, updated_at=NOW() WHERE id=$2`, status, id)
	return err
}

func (r *Repository) UpdateTranscodeResult(ctx context.Context, id, hlsKey, dashKey string) error {
	ctx, span := telemetry.Tracer.Start(ctx, "db.update_transcode_result")
	defer span.End()
	span.SetAttributes(attribute.String("db.media_id", id))

	_, err := r.db.ExecContext(ctx,
		`UPDATE media SET hls_key=$1, dash_key=$2, status='ready', updated_at=NOW() WHERE id=$3`,
		hlsKey, dashKey, id)
	return err
}

func (r *Repository) Close() error { return r.db.Close() }
