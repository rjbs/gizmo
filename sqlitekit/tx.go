package sqlitekit

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/rjbs/gizmo/jmap"
	"github.com/rjbs/gizmo/std"
)

// Tx is a transaction on one account's database.  It is a *[sql.Tx], for the
// application's own reading and writing, with the kit's operations as
// methods, so that they need not be handed the transaction each time.  It is
// also a [std.Tx], so that the standard methods can run in it.
//
// The kit's operations are also methods of [Kit] that take a [Queryer], for
// code that has a plain *sql.Tx, or none.  It makes no difference which are
// used: a Tx learns what changed from the database, not from having been
// told.
type Tx struct {
	*sql.Tx

	kit      *Kit
	ctx      context.Context
	write    bool
	done     bool
	onCommit func(states map[string]string)

	// before is the state of every type when the transaction began, for
	// telling at commit which of them it changed.
	before map[string]string
	depth  int
}

// Begin begins a transaction that may write.  If onCommit is not nil, it is
// called once the transaction has committed, with the new state string of
// each type whose state it changed: that is the moment to publish them.
func (k *Kit) Begin(ctx context.Context, db *sql.DB, onCommit func(states map[string]string)) (*Tx, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	t := &Tx{Tx: tx, kit: k, ctx: ctx, write: true, onCommit: onCommit}
	if onCommit != nil {
		if t.before, err = k.States(ctx, tx); err != nil {
			tx.Rollback()
			return nil, err
		}
	}
	return t, nil
}

// Read begins a transaction that only reads.  Everything it reads is of one
// moment: what other transactions commit while it lasts, it does not see.
func (k *Kit) Read(ctx context.Context, db *sql.DB) (*Tx, error) {
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	return &Tx{Tx: tx, kit: k, ctx: ctx}, nil
}

// State returns the current state string of a type.
func (k *Kit) State(ctx context.Context, q Queryer, typ string) (string, error) {
	if _, err := k.typ(typ); err != nil {
		return "", err
	}
	e, err := epoch(ctx, q)
	if err != nil {
		return "", err
	}
	var modseq int64
	err = q.QueryRowContext(ctx, `SELECT highest_modseq FROM gizmo_state WHERE type = ?`, typ).Scan(&modseq)
	if err != nil {
		return "", fmt.Errorf("sqlitekit: state of %s: %w", typ, err)
	}
	return stateString(e, modseq), nil
}

// Savepoint runs fn so that whatever it wrote is undone if it returns an
// error, and returns fn's error.  Savepoints may be nested.
func (t *Tx) Savepoint(ctx context.Context, fn func() error) error {
	t.depth++
	name := fmt.Sprintf("gizmo_%d", t.depth)
	defer func() { t.depth-- }()

	if _, err := t.ExecContext(ctx, "SAVEPOINT "+name); err != nil {
		return err
	}
	err := fn()
	if err != nil {
		if _, undo := t.ExecContext(ctx, "ROLLBACK TO "+name); undo != nil {
			return undo
		}
	}
	if _, release := t.ExecContext(ctx, "RELEASE "+name); release != nil && err == nil {
		return release
	}
	return err
}

// Commit commits the transaction, and then tells whoever asked of the
// states it changed.
func (t *Tx) Commit() error {
	if !t.write {
		return errors.New("sqlitekit: a transaction that only reads can't be committed")
	}
	changed := map[string]string{}
	if t.onCommit != nil {
		after, err := t.kit.States(t.ctx, t.Tx)
		if err != nil {
			return err
		}
		for typ, state := range after {
			if t.before[typ] != state {
				changed[typ] = state
			}
		}
	}
	if err := t.Tx.Commit(); err != nil {
		return err
	}
	t.done = true
	if len(changed) > 0 {
		t.onCommit(changed)
	}
	return nil
}

// Rollback abandons the transaction.  After Commit it does nothing, so it
// can be deferred.
func (t *Tx) Rollback() error {
	if t.done {
		return nil
	}
	t.done = true
	if err := t.Tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
		return err
	}
	return nil
}

// NextModseq gives a type its next modseq, which is its new state, and
// returns it.  An application calls this itself only when it writes the
// modseq columns with its own SQL; Create, Touch and Destroy call it for
// the ordinary cases.
func (k *Kit) NextModseq(ctx context.Context, q Queryer, typ string) (int64, error) {
	if _, err := k.typ(typ); err != nil {
		return 0, err
	}
	var modseq int64
	err := q.QueryRowContext(ctx,
		`UPDATE gizmo_state SET highest_modseq = highest_modseq + 1 WHERE type = ? RETURNING highest_modseq`, typ,
	).Scan(&modseq)
	if err != nil {
		return 0, fmt.Errorf("sqlitekit: next modseq of %s: %w", typ, err)
	}
	return modseq, nil
}

// exists reports whether a type's table has a row with the id.
func exists(ctx context.Context, q Queryer, table, id string) (bool, error) {
	var found bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM `+table+` WHERE id = ?)`, id).Scan(&found)
	return found, err
}

// NewID makes an id for a new object of the type, one that no object of the
// type has or, as far as the tombstones remember, has had.
func (k *Kit) NewID(ctx context.Context, q Queryer, typ string) (string, error) {
	ty, err := k.typ(typ)
	if err != nil {
		return "", err
	}
	generate := ty.NewID
	if generate == nil {
		generate = DefaultID
	}
	for range 20 {
		id, err := generate()
		if err != nil {
			return "", err
		}
		if !jmap.ValidID(id) {
			return "", fmt.Errorf("sqlitekit: %q is not a valid id for a %s", id, typ)
		}
		taken, err := exists(ctx, q, ty.Table, id)
		if err != nil {
			return "", err
		}
		if !taken {
			err = q.QueryRowContext(ctx,
				`SELECT EXISTS (SELECT 1 FROM gizmo_tombstone WHERE type = ? AND id = ?)`, typ, id).Scan(&taken)
			if err != nil {
				return "", err
			}
		}
		if !taken {
			return id, nil
		}
	}
	return "", fmt.Errorf("sqlitekit: could not make an id for a %s that isn't taken", typ)
}

// Create prepares for a new object of the type with the given id, and
// returns the modseq to write as both its created_modseq and its modseq.
// The application then inserts the row.
//
// If the id belonged to an object that was destroyed, its tombstone is
// removed: the object is made again, and /changes will list it once, as
// created or updated according to what the client has seen.
func (k *Kit) Create(ctx context.Context, q Queryer, typ, id string) (int64, error) {
	modseq, err := k.NextModseq(ctx, q, typ)
	if err != nil {
		return 0, err
	}
	if _, err := q.ExecContext(ctx, `DELETE FROM gizmo_tombstone WHERE type = ? AND id = ?`, typ, id); err != nil {
		return 0, err
	}
	return modseq, nil
}

// Touch records that the object with the given id has changed, by giving it
// the type's next modseq.  It reports whether there is such an object.
func (k *Kit) Touch(ctx context.Context, q Queryer, typ, id string) (bool, error) {
	ty, err := k.typ(typ)
	if err != nil {
		return false, err
	}
	found, err := exists(ctx, q, ty.Table, id)
	if err != nil || !found {
		return false, err
	}
	modseq, err := k.NextModseq(ctx, q, typ)
	if err != nil {
		return false, err
	}
	_, err = q.ExecContext(ctx, `UPDATE `+ty.Table+` SET modseq = ? WHERE id = ?`, modseq, id)
	return true, err
}

// Destroy deletes the object with the given id from its table, and leaves a
// tombstone for /changes to find.  It reports whether there was such an
// object.  Rows of other tables that depend on the object's are the
// application's to deal with, or the database's, by foreign keys.
func (k *Kit) Destroy(ctx context.Context, q Queryer, typ, id string) (bool, error) {
	ty, err := k.typ(typ)
	if err != nil {
		return false, err
	}
	var created int64
	err = q.QueryRowContext(ctx, `SELECT created_modseq FROM `+ty.Table+` WHERE id = ?`, id).Scan(&created)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	modseq, err := k.NextModseq(ctx, q, typ)
	if err != nil {
		return false, err
	}
	if _, err := q.ExecContext(ctx, `DELETE FROM `+ty.Table+` WHERE id = ?`, id); err != nil {
		return false, err
	}
	_, err = q.ExecContext(ctx,
		`INSERT INTO gizmo_tombstone (type, id, created_modseq, modseq, destroyed_at) VALUES (?, ?, ?, ?, ?)`,
		typ, id, created, modseq, k.Now().Unix())
	return true, err
}

// Purge forgets the tombstones of a type that were left before the given
// time, and returns how many.  The type's state does not change.  A client
// whose state is older than the newest tombstone forgotten can no longer be
// told what has changed, and will be told to fetch everything again.
func (k *Kit) Purge(ctx context.Context, q Queryer, typ string, before time.Time) (int, error) {
	if _, err := k.typ(typ); err != nil {
		return 0, err
	}
	var newest sql.NullInt64
	err := q.QueryRowContext(ctx,
		`SELECT max(modseq) FROM gizmo_tombstone WHERE type = ? AND destroyed_at < ?`, typ, before.Unix()).Scan(&newest)
	if err != nil || !newest.Valid {
		return 0, err
	}
	res, err := q.ExecContext(ctx,
		`DELETE FROM gizmo_tombstone WHERE type = ? AND destroyed_at < ?`, typ, before.Unix())
	if err != nil {
		return 0, err
	}
	_, err = q.ExecContext(ctx,
		`UPDATE gizmo_state SET lowest_modseq = max(lowest_modseq, ?) WHERE type = ?`, newest.Int64, typ)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}

// Changes finds what has changed in a type since a state, for [std.Changes].
// It returns at most max ids, or any number if max is zero.
//
// A state from another epoch, one older than the tombstones go back, and
// one this database has not yet reached all get cannotCalculateChanges.
// The last is what a client sees when the database has been restored from
// a backup, and the answer to all three is to fetch everything again.
//
// When max cuts the changes short, the state returned is the modseq of the
// last change given, for the client to ask again from.  Asked from there,
// an object made before that modseq and changed after it is called
// updated, though it was in no earlier answer: its change moved it past
// the cut.  This is known and left alone; Cyrus and Ix do the same, and a
// client has to take an updated id it doesn't hold for a creation.  See
// DESIGN.md.
func (k *Kit) Changes(ctx context.Context, q Queryer, typ, since string, max int) (*std.Changed, error) {
	ty, err := k.typ(typ)
	if err != nil {
		return nil, err
	}
	e, err := epoch(ctx, q)
	if err != nil {
		return nil, err
	}
	from, ok := parseState(e, since)
	if !ok {
		return nil, jmap.Errorf(jmap.ErrCannotCalculateChanges, "state %q is not from this database", since)
	}
	var lowest, highest int64
	err = q.QueryRowContext(ctx,
		`SELECT lowest_modseq, highest_modseq FROM gizmo_state WHERE type = ?`, typ).Scan(&lowest, &highest)
	if err != nil {
		return nil, err
	}
	switch {
	case from > highest:
		return nil, jmap.Errorf(jmap.ErrCannotCalculateChanges, "state %q is ahead of this database", since)
	case from < lowest:
		return nil, jmap.Errorf(jmap.ErrCannotCalculateChanges, "state %q is too old", since)
	}

	limit := -1
	if max > 0 {
		limit = max + 1
	}
	rows, err := q.QueryContext(ctx, `
		SELECT id, created_modseq, modseq, 0 FROM `+ty.Table+` WHERE modseq > ?
		UNION ALL
		SELECT id, created_modseq, modseq, 1 FROM gizmo_tombstone WHERE type = ? AND modseq > ?
		ORDER BY 3
		LIMIT ?`, from, typ, from, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	ch := &std.Changed{}
	last := highest
	n := 0
	for rows.Next() {
		var id string
		var created, modseq int64
		var gone bool
		if err := rows.Scan(&id, &created, &modseq, &gone); err != nil {
			return nil, err
		}
		if max > 0 && n == max {
			ch.HasMore = true
			break
		}
		n++
		last = modseq
		switch {
		case gone && created > from:
			// Made and destroyed since the client last looked: it never
			// saw the object, so there is nothing to tell it.
		case gone:
			ch.Destroyed = append(ch.Destroyed, id)
		case created > from:
			ch.Created = append(ch.Created, id)
		default:
			ch.Updated = append(ch.Updated, id)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if !ch.HasMore {
		last = highest
	}
	ch.NewState = stateString(e, last)
	return ch, nil
}

// The kit's operations, on this transaction.  Each is the Kit method of the
// same name.

func (t *Tx) State(ctx context.Context, typ string) (string, error) {
	return t.kit.State(ctx, t.Tx, typ)
}

func (t *Tx) NextModseq(ctx context.Context, typ string) (int64, error) {
	return t.kit.NextModseq(ctx, t.Tx, typ)
}

func (t *Tx) NewID(ctx context.Context, typ string) (string, error) {
	return t.kit.NewID(ctx, t.Tx, typ)
}

func (t *Tx) Create(ctx context.Context, typ, id string) (int64, error) {
	return t.kit.Create(ctx, t.Tx, typ, id)
}

func (t *Tx) Touch(ctx context.Context, typ, id string) (bool, error) {
	return t.kit.Touch(ctx, t.Tx, typ, id)
}

func (t *Tx) Destroy(ctx context.Context, typ, id string) (bool, error) {
	return t.kit.Destroy(ctx, t.Tx, typ, id)
}

func (t *Tx) Purge(ctx context.Context, typ string, before time.Time) (int, error) {
	return t.kit.Purge(ctx, t.Tx, typ, before)
}

func (t *Tx) Changes(ctx context.Context, typ, since string, max int) (*std.Changed, error) {
	return t.kit.Changes(ctx, t.Tx, typ, since, max)
}
