package compare

import (
	"testing"

	"github.com/opolancoh/dbsync/internal/adapters"
)

func tbl(name string) TableMapping {
	target := name
	return TableMapping{Source: name, Target: &target, Status: StatusMatched}
}

func names(tables []TableMapping) []string {
	out := make([]string, len(tables))
	for i, t := range tables {
		out[i] = t.Source
	}
	return out
}

func TestOrderTablesPutsReferencedTableFirst(t *testing.T) {
	// "TransactionTags" sorts before "Transactions" alphabetically because
	// uppercase T precedes lowercase s, which is how the real migration failed.
	tables := []TableMapping{tbl("app.TransactionTags"), tbl("app.Transactions")}
	fks := []adapters.ForeignKey{{Table: "app.TransactionTags", RefTable: "app.Transactions"}}

	ordered, cycles := orderTables(tables, fks)

	if got := names(ordered); got[0] != "app.Transactions" || got[1] != "app.TransactionTags" {
		t.Errorf("expected parent first, got %v", got)
	}
	if len(cycles) != 0 {
		t.Errorf("expected no cycles, got %v", cycles)
	}
	if ordered[0].Order != 1 || ordered[1].Order != 2 {
		t.Errorf("expected Order 1,2; got %d,%d", ordered[0].Order, ordered[1].Order)
	}
}

func TestOrderTablesKeepsOriginalOrderWithoutForeignKeys(t *testing.T) {
	// Config schema order must survive where no foreign key overrides it.
	tables := []TableMapping{tbl("auth.Zebra"), tbl("app.Alpha"), tbl("app.Beta")}

	ordered, _ := orderTables(tables, nil)

	want := []string{"auth.Zebra", "app.Alpha", "app.Beta"}
	for i, n := range names(ordered) {
		if n != want[i] {
			t.Fatalf("order changed without foreign keys: got %v, want %v", names(ordered), want)
		}
	}
}

func TestOrderTablesCrossSchemaDependencyWins(t *testing.T) {
	// app listed before auth in config, but app.Orders references auth.Users.
	tables := []TableMapping{tbl("app.Orders"), tbl("auth.Users")}
	fks := []adapters.ForeignKey{{Table: "app.Orders", RefTable: "auth.Users"}}

	ordered, _ := orderTables(tables, fks)

	if got := names(ordered); got[0] != "auth.Users" {
		t.Errorf("cross-schema dependency ignored: got %v", got)
	}
}

func TestOrderTablesBreaksCycleAndReportsIt(t *testing.T) {
	tables := []TableMapping{tbl("app.A"), tbl("app.B")}
	fks := []adapters.ForeignKey{
		{Table: "app.A", RefTable: "app.B"},
		{Table: "app.B", RefTable: "app.A"},
	}

	ordered, cycles := orderTables(tables, fks)

	if len(ordered) != 2 {
		t.Fatalf("cycle dropped tables: got %d, want 2", len(ordered))
	}
	if len(cycles) == 0 {
		t.Error("expected the cycle to be reported")
	}
}

func TestOrderTablesHandlesUnmatchedTables(t *testing.T) {
	// An unmatched table has a nil Target and so no place in the FK graph.
	unmatched := TableMapping{Source: "app.Ghost", Status: StatusUnmatched}
	tables := []TableMapping{unmatched, tbl("app.TransactionTags"), tbl("app.Transactions")}
	fks := []adapters.ForeignKey{{Table: "app.TransactionTags", RefTable: "app.Transactions"}}

	ordered, _ := orderTables(tables, fks)

	if len(ordered) != 3 {
		t.Fatalf("lost a table: got %d, want 3", len(ordered))
	}
	seen := map[string]bool{}
	for i, t2 := range ordered {
		seen[t2.Source] = true
		if t2.Order != i+1 {
			t.Errorf("Order not sequential at %d: got %d", i, t2.Order)
		}
	}
	if !seen["app.Ghost"] {
		t.Error("unmatched table was dropped")
	}
}
