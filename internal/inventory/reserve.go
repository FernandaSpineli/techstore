package inventory

import (
	"cmp"
	"context"
	"fmt"
	"slices"

	"github.com/FernandaSpineli/techstore/internal/platform/postgres"
)

// Line is a quantity of one variant moving through the reservation cycle.
type Line struct {
	VariantID string
	Quantity  int
}

// ShortageError reports the variants whose available stock could not cover
// a reservation.
type ShortageError struct {
	Shortages []Shortage
}

// Shortage describes one variant without enough stock.
type Shortage struct {
	VariantID string `json:"variant_id"`
	Requested int    `json:"requested"`
	Available int    `json:"available"`
}

func (e *ShortageError) Error() string {
	return fmt.Sprintf("inventory: insufficient stock for %d variant(s)", len(e.Shortages))
}

// Reserve sets aside stock for lines, all or nothing. It must run inside the
// caller's transaction; the rows stay locked until it ends.
func Reserve(ctx context.Context, tx postgres.Querier, lines []Line) error {
	lines = merge(lines)
	available, err := lock(ctx, tx, lines)
	if err != nil {
		return err
	}

	var short []Shortage
	for _, l := range lines {
		if a := available[l.VariantID]; l.Quantity > a {
			short = append(short, Shortage{VariantID: l.VariantID, Requested: l.Quantity, Available: max(a, 0)})
		}
	}
	if short != nil {
		return &ShortageError{Shortages: short}
	}
	return apply(ctx, tx, lines, `reserved = i.reserved + u.qty`)
}

// Commit turns a reservation into a sale: the units leave on_hand.
func Commit(ctx context.Context, tx postgres.Querier, lines []Line) error {
	lines = merge(lines)
	if _, err := lock(ctx, tx, lines); err != nil {
		return err
	}
	return apply(ctx, tx, lines, `on_hand = i.on_hand - u.qty, reserved = i.reserved - u.qty`)
}

// Release returns reserved units to available stock.
func Release(ctx context.Context, tx postgres.Querier, lines []Line) error {
	lines = merge(lines)
	if _, err := lock(ctx, tx, lines); err != nil {
		return err
	}
	return apply(ctx, tx, lines, `reserved = i.reserved - u.qty`)
}

// merge sums duplicate variants and sorts by variant ID. Every transaction
// locks inventory rows in this same order, so two orders sharing variants
// cannot deadlock.
func merge(lines []Line) []Line {
	sum := make(map[string]int, len(lines))
	for _, l := range lines {
		sum[l.VariantID] += l.Quantity
	}
	out := make([]Line, 0, len(sum))
	for id, q := range sum {
		out = append(out, Line{VariantID: id, Quantity: q})
	}
	slices.SortFunc(out, func(a, b Line) int { return cmp.Compare(a.VariantID, b.VariantID) })
	return out
}

// lock takes row locks on the lines' inventory rows, in variant order, and
// returns their available stock. Variants without a row have none.
func lock(ctx context.Context, tx postgres.Querier, lines []Line) (map[string]int, error) {
	rows, err := tx.Query(ctx, `
		SELECT variant_id, on_hand - reserved FROM inventory
		WHERE variant_id = ANY($1)
		ORDER BY variant_id
		FOR UPDATE`, ids(lines))
	if err != nil {
		return nil, fmt.Errorf("inventory: lock: %w", err)
	}
	defer rows.Close()

	available := make(map[string]int, len(lines))
	for rows.Next() {
		var id string
		var a int
		if err := rows.Scan(&id, &a); err != nil {
			return nil, fmt.Errorf("inventory: lock: %w", err)
		}
		available[id] = a
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("inventory: lock: %w", err)
	}
	return available, nil
}

// apply runs set over every line in one statement. The table's CHECK
// constraints reject any update that would oversell or go negative.
func apply(ctx context.Context, tx postgres.Querier, lines []Line, set string) error {
	qty := make([]int32, len(lines))
	for i, l := range lines {
		qty[i] = int32(l.Quantity) //nolint:gosec // quantities are bounded by validation (<= 99 per line)
	}
	tag, err := tx.Exec(ctx, `
		UPDATE inventory i SET `+set+`
		FROM unnest($1::uuid[], $2::int[]) AS u(variant_id, qty)
		WHERE i.variant_id = u.variant_id`, ids(lines), qty)
	if err != nil {
		return fmt.Errorf("inventory: update: %w", err)
	}
	if int(tag.RowsAffected()) != len(lines) {
		return fmt.Errorf("inventory: update: %d of %d rows found", tag.RowsAffected(), len(lines))
	}
	return nil
}

func ids(lines []Line) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = l.VariantID
	}
	return out
}
