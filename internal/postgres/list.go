package postgres

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"time"

	"github.com/eugene-ruby/filehola/internal/files"
)

type ListQuery struct {
	Directory, Prefix, Cursor string
	Search                    bool
	Limit                     int
}
type Page[T any] struct {
	Items      []T     `json:"items"`
	HasMore    bool    `json:"has_more"`
	NextCursor *string `json:"next_cursor"`
}
type Directory struct {
	Name string `json:"name"`
	Path string `json:"path"`
}
type cursor struct {
	Version int       `json:"v"`
	Scope   string    `json:"s"`
	Time    time.Time `json:"t,omitempty"`
	UUID    string    `json:"u,omitempty"`
	Name    string    `json:"n,omitempty"`
}

func encodeCursor(c cursor) *string {
	data, _ := json.Marshal(c)
	s := base64.RawURLEncoding.EncodeToString(data)
	return &s
}
func decodeCursor(raw, scope string) (cursor, error) {
	var c cursor
	if raw == "" {
		return c, nil
	}
	if len(raw) > 4096 {
		return c, files.Reject(422, "invalid_cursor")
	}
	data, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil || json.Unmarshal(data, &c) != nil || c.Version != 1 || c.Scope != scope {
		return c, files.Reject(422, "invalid_cursor")
	}
	return c, nil
}
func pageLimit(n int) int {
	if n <= 0 {
		return files.DefaultPageLimit
	}
	if n > files.MaxPageLimit {
		return files.MaxPageLimit
	}
	return n
}

func (r *Repository) List(ctx context.Context, q ListQuery) (Page[files.File], error) {
	page := Page[files.File]{Items: []files.File{}}
	scope := "files:directory:" + q.Directory
	filter := "directory=$1"
	value := q.Directory
	if q.Search {
		scope = "files:prefix:" + q.Prefix
		filter = `name LIKE $1 ESCAPE '\'`
		value = files.EscapeLike(q.Prefix) + "%"
	}
	c, err := decodeCursor(q.Cursor, scope)
	if err != nil {
		return page, err
	}
	if q.Cursor != "" && (!files.ValidUUID(c.UUID) || c.Time.IsZero()) {
		return page, files.Reject(422, "invalid_cursor")
	}
	q.Limit = pageLimit(q.Limit)
	rows, err := r.DB.QueryContext(ctx, `SELECT `+columns+` FROM files WHERE status='published' AND `+filter+` AND ($2 OR (created_at,uuid)<($3::timestamptz,$4::uuid)) ORDER BY created_at DESC,uuid DESC LIMIT $5`, value, q.Cursor == "", c.Time, validCursorUUID(c.UUID), q.Limit+1)
	if err != nil {
		return page, err
	}
	defer rows.Close()
	for rows.Next() {
		f, err := scan(rows)
		if err != nil {
			return page, err
		}
		page.Items = append(page.Items, f)
	}
	if err := rows.Err(); err != nil {
		return page, err
	}
	if len(page.Items) > q.Limit {
		page.HasMore = true
		page.Items = page.Items[:q.Limit]
		last := page.Items[len(page.Items)-1]
		page.NextCursor = encodeCursor(cursor{Version: 1, Scope: scope, Time: last.CreatedAt, UUID: last.UUID})
	}
	return page, nil
}
func validCursorUUID(uuid string) string {
	if uuid == "" {
		return "00000000-0000-0000-0000-000000000000"
	}
	return uuid
}

func (r *Repository) Directories(ctx context.Context, q ListQuery) (Page[Directory], error) {
	page := Page[Directory]{Items: []Directory{}}
	scope := "directories:" + q.Directory
	c, err := decodeCursor(q.Cursor, scope)
	if err != nil {
		return page, err
	}
	if q.Cursor != "" && !files.ValidName(c.Name) {
		return page, files.Reject(422, "invalid_cursor")
	}
	prefix := strings.TrimSuffix(q.Directory, "/") + "/"
	q.Limit = pageLimit(q.Limit)
	rows, err := r.DB.QueryContext(ctx, `SELECT child FROM (SELECT DISTINCT split_part(substring(directory FROM $1::integer), '/', 1) COLLATE "C" AS child FROM files WHERE status='published' AND directory LIKE $2 ESCAPE '\') children WHERE child<>'' AND child>$3 COLLATE "C" ORDER BY child COLLATE "C" LIMIT $4`, len(prefix)+1, files.EscapeLike(prefix)+"%", c.Name, q.Limit+1)
	if err != nil {
		return page, err
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return page, err
		}
		page.Items = append(page.Items, Directory{Name: name, Path: files.Path(q.Directory, name)})
	}
	if err := rows.Err(); err != nil {
		return page, err
	}
	if len(page.Items) > q.Limit {
		page.HasMore = true
		page.Items = page.Items[:q.Limit]
		last := page.Items[len(page.Items)-1]
		page.NextCursor = encodeCursor(cursor{Version: 1, Scope: scope, Name: last.Name})
	}
	return page, nil
}
