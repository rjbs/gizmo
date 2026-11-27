// Package sqlitekit keeps JMAP state in an SQLite database: a state for each
// type, a modseq on every object, and tombstones for what has been
// destroyed.  It is how a server gets /changes, state strings and push for
// little more than the cost of following a convention.
//
// The convention is this.  One database is one account.  Each type has a
// table of its own, of the application's design, with at least these columns:
//
//	id             TEXT PRIMARY KEY
//	created_modseq INTEGER NOT NULL
//	modseq         INTEGER NOT NULL
//
// The table holds only live objects.  When an object is made, changed or
// destroyed, the application says so through a [Tx], which gives the object
// the next modseq of its type and, for a destroy, deletes its row and
// leaves a tombstone.  The kit has three tables of its own, each named
// gizmo_ and something.
//
// The kit never holds the database.  The application opens it, begins
// every transaction, and may write to its own tables however it likes,
// from a JMAP request or from anything else, so long as it tells the Tx
// which objects it changed.
package sqlitekit

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base32"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// Type describes one JMAP type to the kit.
type Type struct {
	// Name is the type's JMAP name, like "Email".
	Name string

	// Table is the table its objects live in.
	Table string

	// NewID makes an id for a new object of the type.  If nil, DefaultID
	// is used.  The kit sees to it that the id is not already in use.
	NewID func() (string, error)
}

// Kit is the convention applied to a set of types.  One Kit serves any
// number of databases that hold those types.
type Kit struct {
	// Now tells the time, which tombstones are stamped with.  It is
	// time.Now unless a test says otherwise.
	Now func() time.Time

	types map[string]Type
	names []string
}

// New returns a [Kit] for the given types.
func New(types ...Type) *Kit {
	k := &Kit{Now: time.Now, types: map[string]Type{}}
	for _, t := range types {
		if _, dup := k.types[t.Name]; dup {
			panic("sqlitekit: type " + t.Name + " given twice")
		}
		k.types[t.Name] = t
		k.names = append(k.names, t.Name)
	}
	return k
}

func (k *Kit) typ(name string) (Type, error) {
	t, ok := k.types[name]
	if !ok {
		return Type{}, fmt.Errorf("sqlitekit: no such type %q", name)
	}
	return t, nil
}

// Queryer is what a *[sql.DB], a *[sql.Tx] and a *[sql.Conn] have in common.
type Queryer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Open opens the database at path with the settings that suit several
// writers, in this process and in others: write-ahead logging, foreign keys
// enforced, a wait of five seconds for a lock, and write transactions that
// take their lock when they begin, so that none can fail for want of it
// part way through.  Every process that writes to the database should open
// it this way.
func Open(path string) (*sql.DB, error) {
	return sql.Open("sqlite", "file:"+path+
		"?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_txlock=immediate")
}

// OpenReadOnly opens the database at path for reading only.
func OpenReadOnly(path string) (*sql.DB, error) {
	return sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&mode=ro")
}

const schema = `
CREATE TABLE IF NOT EXISTS gizmo_meta (
	name  TEXT PRIMARY KEY,
	value TEXT NOT NULL
) WITHOUT ROWID;

CREATE TABLE IF NOT EXISTS gizmo_state (
	type           TEXT PRIMARY KEY,
	lowest_modseq  INTEGER NOT NULL DEFAULT 0,
	highest_modseq INTEGER NOT NULL DEFAULT 0
) WITHOUT ROWID;

CREATE TABLE IF NOT EXISTS gizmo_tombstone (
	type           TEXT NOT NULL,
	id             TEXT NOT NULL,
	created_modseq INTEGER NOT NULL,
	modseq         INTEGER NOT NULL,
	destroyed_at   INTEGER NOT NULL,
	PRIMARY KEY (type, id)
) WITHOUT ROWID;

CREATE INDEX IF NOT EXISTS gizmo_tombstone_modseq ON gizmo_tombstone (type, modseq);
`

// Init makes the kit's own tables in a database that hasn't got them, gives
// the database an epoch if it has none, and gives each type a state if it
// has none.  It is safe to run every time the database is opened, and should
// be run after the application's own migrations, since a type's first state
// is taken from what is already in its table.
func (k *Kit) Init(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("sqlitekit: making tables: %w", err)
	}
	if _, err := epoch(ctx, tx); errors.Is(err, sql.ErrNoRows) {
		if err := setEpoch(ctx, tx); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	for _, name := range k.names {
		// A type new to a database that already has objects of it, as
		// when a server first adopts the kit, starts at the highest
		// modseq its objects have.
		_, err := tx.ExecContext(ctx, fmt.Sprintf(
			`INSERT OR IGNORE INTO gizmo_state (type, highest_modseq)
			 VALUES (?, coalesce((SELECT max(modseq) FROM %s), 0))`, k.types[name].Table), name)
		if err != nil {
			return fmt.Errorf("sqlitekit: first state of %s: %w", name, err)
		}
	}
	return tx.Commit()
}

func epoch(ctx context.Context, q Queryer) (string, error) {
	var e string
	err := q.QueryRowContext(ctx, `SELECT value FROM gizmo_meta WHERE name = 'epoch'`).Scan(&e)
	return e, err
}

func setEpoch(ctx context.Context, q Queryer) error {
	e, err := randomToken(8)
	if err != nil {
		return err
	}
	_, err = q.ExecContext(ctx,
		`INSERT INTO gizmo_meta (name, value) VALUES ('epoch', ?)
		 ON CONFLICT (name) DO UPDATE SET value = excluded.value`, e)
	return err
}

// NewEpoch gives the database a new epoch, so that every state string a
// client holds is taken for a stranger's and the client fetches everything
// again.  Whatever restores a database from a backup should do this: the
// restored database has gone back in time, and once its modseqs passed the
// ones clients hold, those clients would be given wrong changes and no
// error.
func NewEpoch(ctx context.Context, q Queryer) error {
	return setEpoch(ctx, q)
}

// SetEpoch gives the database a particular epoch.  It is for a server that
// had state strings of this form before it had the kit, and wants the ones
// its clients hold to stay good.
func SetEpoch(ctx context.Context, q Queryer, epoch string) error {
	if epoch == "" || strings.Contains(epoch, "-") {
		return fmt.Errorf("sqlitekit: %q can't be an epoch", epoch)
	}
	_, err := q.ExecContext(ctx,
		`INSERT INTO gizmo_meta (name, value) VALUES ('epoch', ?)
		 ON CONFLICT (name) DO UPDATE SET value = excluded.value`, epoch)
	return err
}

// stateString makes a state string of an epoch and a modseq.
func stateString(epoch string, modseq int64) string {
	return epoch + "-" + strconv.FormatInt(modseq, 10)
}

// parseState returns the modseq in a state string, provided the string
// carries the given epoch.
func parseState(epoch, state string) (int64, bool) {
	e, n, ok := strings.Cut(state, "-")
	if !ok || e != epoch {
		return 0, false
	}
	modseq, err := strconv.ParseInt(n, 10, 64)
	return modseq, err == nil && modseq >= 0
}

// States returns the state string of every type, as q sees them.  It is
// what the event source wants of an account.
func (k *Kit) States(ctx context.Context, q Queryer) (map[string]string, error) {
	e, err := epoch(ctx, q)
	if err != nil {
		return nil, err
	}
	rows, err := q.QueryContext(ctx, `SELECT type, highest_modseq FROM gizmo_state`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var name string
		var modseq int64
		if err := rows.Scan(&name, &modseq); err != nil {
			return nil, err
		}
		if _, ours := k.types[name]; ours {
			out[name] = stateString(e, modseq)
		}
	}
	return out, rows.Err()
}

// Migrate brings a database's own schema, which is the application's, up to
// date.  Each migration is a batch of SQL, applied in its own transaction;
// which have been applied is kept in the database's user_version.  A
// database newer than the migrations given is refused, since it was made by
// a newer build than this one.
func Migrate(ctx context.Context, db *sql.DB, migrations []string) error {
	var version int
	if err := db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		return fmt.Errorf("sqlitekit: reading schema version: %w", err)
	}
	if version > len(migrations) {
		return fmt.Errorf("sqlitekit: schema version %d is newer than this build knows (%d)", version, len(migrations))
	}
	for i := version; i < len(migrations); i++ {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, migrations[i]); err != nil {
			tx.Rollback()
			return fmt.Errorf("sqlitekit: applying schema migration %d: %w", i+1, err)
		}
		if _, err := tx.ExecContext(ctx, fmt.Sprintf(`PRAGMA user_version = %d`, i+1)); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

var idEncoding = base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding)

func randomToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return idEncoding.EncodeToString(b), nil
}

// DefaultID makes an id of sixteen characters, lowercase letters and the
// digits 2 to 7, of which the first is always a letter, as RFC 8620 §1.2
// advises.  It holds a little under eighty bits of randomness.
func DefaultID() (string, error) {
	id, err := randomToken(10)
	if err != nil {
		return "", err
	}
	if c := id[0]; c >= '2' && c <= '7' {
		// The digits 2 to 7 become the letters a to f.
		id = string('a'+(c-'2')) + id[1:]
	}
	return id, nil
}
