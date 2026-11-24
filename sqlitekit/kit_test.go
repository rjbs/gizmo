package sqlitekit

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/rjbs/gizmo/jmap"
)

var widgetMigrations = []string{
	`CREATE TABLE widget (id TEXT PRIMARY KEY, created_modseq INTEGER NOT NULL, modseq INTEGER NOT NULL, name TEXT NOT NULL)`,
	`CREATE TABLE gadget (id TEXT PRIMARY KEY, created_modseq INTEGER NOT NULL, modseq INTEGER NOT NULL)`,
}

// shop is a database of widgets and gadgets, with a clock that the test
// moves, and a record of the states each commit announced.
type shop struct {
	t         *testing.T
	kit       *Kit
	db        *sql.DB
	path      string
	now       time.Time
	announced []map[string]string
}

func newShop(t *testing.T) *shop {
	t.Helper()
	s := &shop{t: t, path: filepath.Join(t.TempDir(), "shop.sqlite"), now: time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)}
	s.kit = New(Type{Name: "Widget", Table: "widget"}, Type{Name: "Gadget", Table: "gadget"})
	s.kit.Now = func() time.Time { return s.now }
	s.open()
	return s
}

func (s *shop) open() {
	s.t.Helper()
	db, err := Open(s.path)
	if err != nil {
		s.t.Fatal(err)
	}
	s.t.Cleanup(func() { db.Close() })
	if err := Migrate(context.Background(), db, widgetMigrations); err != nil {
		s.t.Fatal(err)
	}
	if err := s.kit.Init(context.Background(), db); err != nil {
		s.t.Fatal(err)
	}
	s.db = db
}

// write runs fn in a write transaction and commits it.
func (s *shop) write(fn func(tx *Tx)) {
	s.t.Helper()
	tx, err := s.kit.Begin(context.Background(), s.db, func(states map[string]string) {
		s.announced = append(s.announced, states)
	})
	if err != nil {
		s.t.Fatal(err)
	}
	defer tx.Rollback()
	fn(tx)
	if err := tx.Commit(); err != nil {
		s.t.Fatal(err)
	}
}

// read runs fn in a read transaction.
func (s *shop) read(fn func(tx *Tx)) {
	s.t.Helper()
	tx, err := s.kit.Read(context.Background(), s.db)
	if err != nil {
		s.t.Fatal(err)
	}
	defer tx.Rollback()
	fn(tx)
}

var ctx = context.Background()

// make makes a widget with the given id, in tx.
func (s *shop) make(tx *Tx, id string) {
	s.t.Helper()
	modseq, err := tx.Create(ctx, "Widget", id)
	if err != nil {
		s.t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO widget (id, created_modseq, modseq, name) VALUES (?, ?, ?, ?)`, id, modseq, modseq, id); err != nil {
		s.t.Fatal(err)
	}
}

func (s *shop) touch(tx *Tx, id string) {
	s.t.Helper()
	if found, err := tx.Touch(ctx, "Widget", id); err != nil || !found {
		s.t.Fatalf("touching %s: found %v, error %v", id, found, err)
	}
}

func (s *shop) destroy(tx *Tx, id string) {
	s.t.Helper()
	if found, err := tx.Destroy(ctx, "Widget", id); err != nil || !found {
		s.t.Fatalf("destroying %s: found %v, error %v", id, found, err)
	}
}

// state returns the modseq part of Widget's state, and checks its epoch.
func (s *shop) state() string {
	s.t.Helper()
	var state string
	s.read(func(tx *Tx) {
		var err error
		if state, err = tx.State(ctx, "Widget"); err != nil {
			s.t.Fatal(err)
		}
	})
	return state
}

// at makes a Widget state string for a modseq, with the database's epoch.
func (s *shop) at(modseq string) string {
	s.t.Helper()
	e, err := epoch(ctx, s.db)
	if err != nil {
		s.t.Fatal(err)
	}
	return e + "-" + modseq
}

// checkChanges checks the changes to Widget since a modseq: the ids
// created, updated and destroyed, each joined by spaces, the modseq of the
// new state, and whether there are more.
func checkChanges(t *testing.T, s *shop, name, since string, max int, wantCreated, wantUpdated, wantDestroyed, wantState string, wantMore bool) {
	t.Helper()
	s.read(func(tx *Tx) {
		ch, err := tx.Changes(ctx, "Widget", s.at(since), max)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			return
		}
		got := []any{strings.Join(ch.Created, " "), strings.Join(ch.Updated, " "), strings.Join(ch.Destroyed, " "), ch.NewState, ch.HasMore}
		want := []any{wantCreated, wantUpdated, wantDestroyed, s.at(wantState), wantMore}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s:\n got %#v\nwant %#v", name, got, want)
		}
	})
}

// checkCannotCalculate checks that the changes to Widget since a state, given
// whole, can't be had.
func checkCannotCalculate(t *testing.T, s *shop, name, since string) {
	t.Helper()
	s.read(func(tx *Tx) {
		_, err := tx.Changes(ctx, "Widget", since, 0)
		var me *jmap.MethodError
		if !errors.As(err, &me) || me.Type != jmap.ErrCannotCalculateChanges {
			t.Errorf("%s: got %v, want cannotCalculateChanges", name, err)
		}
	})
}

func TestStatesAndChanges(t *testing.T) {
	s := newShop(t)
	if got := s.state(); got != s.at("0") {
		t.Errorf("the state of a type with nothing in it is %q, want %q", got, s.at("0"))
	}
	checkChanges(t, s, "nothing has happened", "0", 0, "", "", "", "0", false)

	s.write(func(tx *Tx) { s.make(tx, "a"); s.make(tx, "b"); s.make(tx, "c") }) // 1, 2, 3
	s.write(func(tx *Tx) { s.touch(tx, "a") })                                  // 4
	s.write(func(tx *Tx) { s.destroy(tx, "b") })                                // 5
	s.write(func(tx *Tx) { s.make(tx, "d") })                                   // 6
	s.write(func(tx *Tx) { s.destroy(tx, "d") })                                // 7

	if got := s.state(); got != s.at("7") {
		t.Errorf("each object changed takes a modseq: the state is %q, want %q", got, s.at("7"))
	}
	checkChanges(t, s, "from the start: what was made and unmade is not mentioned", "0", 0, "c a", "", "", "7", false)
	checkChanges(t, s, "an update, a destroy, and one that came and went", "3", 0, "", "a", "b", "7", false)
	checkChanges(t, s, "what the client saw made is destroyed", "6", 0, "", "", "d", "7", false)
	checkChanges(t, s, "up to date", "7", 0, "", "", "", "7", false)
	checkChanges(t, s, "the first page", "3", 1, "", "a", "", "4", true)
	checkChanges(t, s, "the next page", "4", 1, "", "", "b", "5", true)
	checkChanges(t, s, "a page with room to spare", "4", 5, "", "", "b", "7", false)

	checkCannotCalculate(t, s, "a state from another database", "elsewhere-3")
	checkCannotCalculate(t, s, "a state that isn't one", "3")
	checkCannotCalculate(t, s, "a state with no number", s.at("three"))
	checkCannotCalculate(t, s, "a state ahead of the database, as after a restore", s.at("8"))

	states, err := s.kit.States(ctx, s.db)
	if err != nil {
		t.Fatal(err)
	}
	if want := map[string]string{"Widget": s.at("7"), "Gadget": s.at("0")}; !reflect.DeepEqual(states, want) {
		t.Errorf("States: got %v, want %v", states, want)
	}

	s.read(func(tx *Tx) {
		if _, err := tx.State(ctx, "Gizmo"); err == nil {
			t.Error("the state of a type the kit wasn't told of should be an error")
		}
		if err := tx.Commit(); err == nil {
			t.Error("a read transaction should not commit")
		}
	})
}

// An object made before a page's cut and changed after it comes on a later
// page as updated, the client never having been told it was made.  This is
// not wanted so much as accepted; see the comment on Changes.
func TestPagedChangesCallTheUnseenUpdated(t *testing.T) {
	s := newShop(t)
	s.write(func(tx *Tx) { s.make(tx, "a") })  // 1
	s.write(func(tx *Tx) { s.make(tx, "b") })  // 2
	s.write(func(tx *Tx) { s.touch(tx, "a") }) // 3

	checkChanges(t, s, "all at once, a is created", "0", 0, "b a", "", "", "3", false)
	checkChanges(t, s, "the first page has only b", "0", 1, "b", "", "", "2", true)
	checkChanges(t, s, "the second page calls a updated", "2", 1, "", "a", "", "3", false)
}

func TestMissingObjects(t *testing.T) {
	s := newShop(t)
	s.write(func(tx *Tx) {
		if found, err := tx.Touch(ctx, "Widget", "ghost"); found || err != nil {
			t.Errorf("touching nothing: found %v, error %v", found, err)
		}
		if found, err := tx.Destroy(ctx, "Widget", "ghost"); found || err != nil {
			t.Errorf("destroying nothing: found %v, error %v", found, err)
		}
	})
	if got := s.state(); got != s.at("0") {
		t.Errorf("nothing was changed, but the state is %q", got)
	}
	if len(s.announced) != 0 {
		t.Errorf("nothing was changed, but this was announced: %v", s.announced)
	}
}

func TestAnnouncements(t *testing.T) {
	s := newShop(t)
	s.write(func(tx *Tx) {
		s.make(tx, "a")
		s.make(tx, "b")
		if _, err := tx.NextModseq(ctx, "Gadget"); err != nil {
			t.Fatal(err)
		}
	})
	s.write(func(tx *Tx) { s.touch(tx, "a") })
	want := []map[string]string{{"Widget": s.at("2"), "Gadget": s.at("1")}, {"Widget": s.at("3")}}
	if !reflect.DeepEqual(s.announced, want) {
		t.Errorf("announced %v, want %v", s.announced, want)
	}

	tx, err := s.kit.Begin(ctx, s.db, func(map[string]string) { t.Error("a transaction that was abandoned announced something") })
	if err != nil {
		t.Fatal(err)
	}
	s.make(tx, "c")
	tx.Rollback()
	if got := s.state(); got != s.at("3") {
		t.Errorf("after a rollback the state is %q, want %q", got, s.at("3"))
	}
}

func TestSavepoints(t *testing.T) {
	s := newShop(t)
	failure := errors.New("no")
	s.write(func(tx *Tx) {
		s.make(tx, "kept")
		err := tx.Savepoint(ctx, func() error {
			s.make(tx, "lost")
			if _, err := tx.NextModseq(ctx, "Gadget"); err != nil {
				t.Fatal(err)
			}
			// A savepoint inside one that fails is undone with it,
			// though it succeeded.
			if err := tx.Savepoint(ctx, func() error { s.make(tx, "lost too"); return nil }); err != nil {
				t.Fatal(err)
			}
			return failure
		})
		if err != failure {
			t.Fatalf("the savepoint returned %v", err)
		}
		if err := tx.Savepoint(ctx, func() error { s.make(tx, "also kept"); return nil }); err != nil {
			t.Fatal(err)
		}
	})
	checkChanges(t, s, "what the failed savepoint wrote is gone, modseqs and all", "0", 0, "kept also kept", "", "", "2", false)
	if want := []map[string]string{{"Widget": s.at("2")}}; !reflect.DeepEqual(s.announced, want) {
		t.Errorf("announced %v, want %v", s.announced, want)
	}
}

func TestPurge(t *testing.T) {
	s := newShop(t)
	s.write(func(tx *Tx) { s.make(tx, "a"); s.make(tx, "b"); s.make(tx, "c") }) // 1, 2, 3
	s.write(func(tx *Tx) { s.destroy(tx, "a") })                                // 4, on the 1st
	s.now = s.now.AddDate(0, 0, 10)
	s.write(func(tx *Tx) { s.destroy(tx, "b") }) // 5, on the 11th
	s.write(func(tx *Tx) { s.touch(tx, "c") })   // 6

	purge := func(before time.Time, want int) {
		t.Helper()
		s.write(func(tx *Tx) {
			if n, err := tx.Purge(ctx, "Widget", before); err != nil || n != want {
				t.Errorf("purging before %s: purged %d, error %v; want %d", before.Format(time.DateOnly), n, err, want)
			}
		})
	}
	before := s.state()
	purge(s.now.AddDate(0, 0, -20), 0)
	checkChanges(t, s, "nothing was old enough to purge", "0", 0, "c", "", "", "6", false)

	purge(s.now.AddDate(0, 0, -5), 1)
	if got := s.state(); got != before {
		t.Errorf("purging changed the state from %q to %q", before, got)
	}
	if len(s.announced) != 4 {
		t.Errorf("purging announced something: %v", s.announced)
	}
	checkCannotCalculate(t, s, "a state from before the purged tombstone", s.at("3"))
	checkCannotCalculate(t, s, "a state from the very start", s.at("0"))
	checkChanges(t, s, "a state from when the purged object was already gone", "4", 0, "", "c", "b", "6", false)
	checkChanges(t, s, "a later state", "5", 0, "", "c", "", "6", false)

	purge(s.now.AddDate(0, 0, 1), 1)
	checkCannotCalculate(t, s, "a state from before the second purged tombstone", s.at("4"))
	checkChanges(t, s, "a state from after everything purged", "5", 0, "", "c", "", "6", false)
}

func TestRecreating(t *testing.T) {
	s := newShop(t)
	s.write(func(tx *Tx) { s.make(tx, "a"); s.make(tx, "b") }) // 1, 2
	s.write(func(tx *Tx) { s.destroy(tx, "a") })               // 3
	s.write(func(tx *Tx) { s.make(tx, "a") })                  // 4

	checkChanges(t, s, "a client that never saw it is told it was made", "0", 0, "b a", "", "", "4", false)
	checkChanges(t, s, "a client that saw it destroyed is told it was made", "3", 0, "a", "", "", "4", false)
	// A client that has the old object is told of the new one as a
	// creation, and never that the old one was destroyed.  Either way it
	// fetches the object, which is what it needs to do.
	checkChanges(t, s, "a client that has the old one is told once, and not that it was destroyed", "2", 0, "a", "", "", "4", false)

	s.write(func(tx *Tx) { s.destroy(tx, "a") }) // 5
	checkChanges(t, s, "and it can be destroyed again", "4", 0, "", "", "a", "5", false)
}

func TestEpochs(t *testing.T) {
	s := newShop(t)
	s.write(func(tx *Tx) { s.make(tx, "a") })
	old := s.state()

	s.db.Close()
	s.open()
	if got := s.state(); got != old {
		t.Errorf("opening the database again changed the state from %q to %q", old, got)
	}

	if err := NewEpoch(ctx, s.db); err != nil {
		t.Fatal(err)
	}
	if got := s.state(); got == old || !strings.HasSuffix(got, "-1") {
		t.Errorf("after a new epoch the state is %q; it was %q", got, old)
	}
	checkCannotCalculate(t, s, "a state from the old epoch", old)

	if err := SetEpoch(ctx, s.db, "legacy"); err != nil {
		t.Fatal(err)
	}
	if got := s.state(); got != "legacy-1" {
		t.Errorf("after setting the epoch the state is %q", got)
	}
	if err := SetEpoch(ctx, s.db, "two-parts"); err == nil {
		t.Error("an epoch with a hyphen in it should be refused")
	}
}

// A database that had objects, with modseqs, before it had the kit.
func TestAdopting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.sqlite")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := Migrate(ctx, db, widgetMigrations); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO widget VALUES ('a', 3, 9, 'a'), ('b', 5, 5, 'b')`); err != nil {
		t.Fatal(err)
	}
	kit := New(Type{Name: "Widget", Table: "widget"}, Type{Name: "Gadget", Table: "gadget"})
	if err := kit.Init(ctx, db); err != nil {
		t.Fatal(err)
	}
	states, err := kit.States(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(states["Widget"], "-9") || !strings.HasSuffix(states["Gadget"], "-0") {
		t.Errorf("a type's first state should be the highest modseq it has: %v", states)
	}
}

func TestMigrate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "m.sqlite")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	version := func() int {
		var v int
		db.QueryRow(`PRAGMA user_version`).Scan(&v)
		return v
	}
	if err := Migrate(ctx, db, widgetMigrations[:1]); err != nil || version() != 1 {
		t.Fatalf("the first migration: error %v, version %d", err, version())
	}
	if err := Migrate(ctx, db, widgetMigrations); err != nil || version() != 2 {
		t.Fatalf("the second migration: error %v, version %d", err, version())
	}
	if err := Migrate(ctx, db, widgetMigrations); err != nil || version() != 2 {
		t.Fatalf("migrating again: error %v, version %d", err, version())
	}
	if err := Migrate(ctx, db, widgetMigrations[:1]); err == nil {
		t.Error("a database newer than the build should be refused")
	}
	bad := append(append([]string{}, widgetMigrations...), `CREATE TABLE extra (x); CREATE TABLE widget (y)`)
	if err := Migrate(ctx, db, bad); err == nil || version() != 2 {
		t.Errorf("a migration that fails: error %v, version %d", err, version())
	}
	var n int
	db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name = 'extra'`).Scan(&n)
	if n != 0 {
		t.Error("a migration that failed left part of itself behind")
	}
}

func TestIDs(t *testing.T) {
	seen := map[string]bool{}
	for range 500 {
		id, err := DefaultID()
		if err != nil {
			t.Fatal(err)
		}
		if len(id) != 16 || id[0] < 'a' || id[0] > 'z' || !jmap.ValidID(id) || seen[id] {
			t.Fatalf("a default id should be sixteen characters, the first a letter, and new: %q", id)
		}
		seen[id] = true
	}

	// A generator that runs through a few ids, of which the first two are
	// taken: one by a widget, one by a widget's tombstone.
	queue := []string{"007", "12e4", "000123456789"}
	kit := New(Type{Name: "Widget", Table: "widget", NewID: func() (string, error) {
		id := queue[0]
		queue = queue[1:]
		return id, nil
	}}, Type{Name: "Gadget", Table: "gadget"})
	s := newShop(t)
	s.write(func(tx *Tx) { s.make(tx, "007"); s.make(tx, "12e4") })
	s.write(func(tx *Tx) { s.destroy(tx, "12e4") })

	tx, err := kit.Begin(ctx, s.db, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if id, err := tx.NewID(ctx, "Widget"); err != nil || id != "000123456789" {
		t.Errorf("a type's own generator, past the ids in use: got %q, error %v", id, err)
	}
	if id, err := tx.NewID(ctx, "Gadget"); err != nil || len(id) != 16 {
		t.Errorf("a type with no generator gets the default: got %q, error %v", id, err)
	}

	// Ids that look like numbers are kept as the strings they are.
	s.make(tx, "000123456789")
	var back string
	if err := tx.QueryRowContext(ctx, `SELECT id FROM widget WHERE id = '000123456789'`).Scan(&back); err != nil || back != "000123456789" {
		t.Errorf("an id of digits came back as %q, error %v", back, err)
	}

	stuck := New(Type{Name: "Widget", Table: "widget", NewID: func() (string, error) { return "007", nil }})
	// These share the open transaction, which holds the write lock.
	stuckTx := &Tx{Tx: tx.Tx, kit: stuck, ctx: ctx, write: true}
	if _, err := stuckTx.NewID(ctx, "Widget"); err == nil {
		t.Error("a generator that only makes an id in use should give up")
	}
	bad := New(Type{Name: "Widget", Table: "widget", NewID: func() (string, error) { return "not an id!", nil }})
	badTx := &Tx{Tx: tx.Tx, kit: bad, ctx: ctx, write: true}
	if _, err := badTx.NewID(ctx, "Widget"); err == nil {
		t.Error("a generator that makes something that isn't an id should be refused")
	}
}
