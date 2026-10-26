package files

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"
)

const (
	MaxFileSize      int64 = 50 * 1024 * 1024
	DefaultPageLimit       = 50
	MaxPageLimit           = 100
	DefaultMIME            = "application/octet-stream"
)

type Reference struct {
	UUID     string `json:"uuid"`
	Revision int64  `json:"revision"`
}
type File struct {
	UUID         string     `json:"uuid"`
	Revision     int64      `json:"revision"`
	Status       string     `json:"status"`
	Directory    string     `json:"directory"`
	Name         string     `json:"name"`
	Path         string     `json:"path"`
	MIME         string     `json:"mime"`
	Size         int64      `json:"size"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
	Source       *Reference `json:"source,omitempty"`
	SupersededBy *Reference `json:"superseded_by,omitempty"`
	ObjectKey    string     `json:"-"`
}

type Error struct {
	Status  int        `json:"-"`
	Code    string     `json:"error"`
	Source  *Reference `json:"source,omitempty"`
	Current *Reference `json:"current,omitempty"`
}

func (e *Error) Error() string              { return e.Code }
func Reject(status int, code string) *Error { return &Error{Status: status, Code: code} }
func Ref(f File) *Reference                 { return &Reference{UUID: f.UUID, Revision: f.Revision} }

var namePattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)
var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func ValidUUID(s string) bool { return uuidPattern.MatchString(s) }
func ValidName(s string) bool { return s != "." && s != ".." && namePattern.MatchString(s) }
func Path(directory, name string) string {
	if directory == "/" {
		return "/" + name
	}
	return directory + "/" + name
}
func NewUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("secure random unavailable")
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	s := hex.EncodeToString(b[:])
	return s[:8] + "-" + s[8:12] + "-" + s[12:16] + "-" + s[16:20] + "-" + s[20:]
}
func ObjectKey(uuid string, revision int64) string {
	return fmt.Sprintf("files/%s/%s/%d/%s", uuid[:2], uuid, revision, NewUUID())
}

// Objects deliberately has no overwrite operation; all callers generate a new key per attempt.
type Objects interface {
	Put(context.Context, string, io.ReadSeeker, int64, string) error
	Copy(context.Context, string, string) error
	Get(context.Context, string) (io.ReadCloser, error)
	Delete(context.Context, string) error
}

func EscapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}
