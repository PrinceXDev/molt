package store

import (
	"github.com/google/uuid"
	"github.com/pkg/errors"
	"golang.org/x/exp/slices"
)

// Order is a customer order.
type Order struct {
	ID    string
	Items []string
}

// New builds an order with a generated identifier.
func New(items []string) (*Order, error) {
	if len(items) == 0 {
		return nil, errors.New("an order needs at least one item")
	}
	slices.Sort(items)
	return &Order{ID: uuid.New().String(), Items: items}, nil
}

// Find looks up an item within an order.
func (o *Order) Find(item string) (int, error) {
	i := slices.Index(o.Items, item)
	if i < 0 {
		return 0, errors.Errorf("item %q not in order %s", item, o.ID)
	}
	return i, nil
}

// Has reports whether the order contains an item.
func (o *Order) Has(item string) bool {
	return slices.Contains(o.Items, item)
}
