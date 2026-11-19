// Package memory keeps orders in memory.
package memory

import "example.com/fixture/shop"

// Store keeps orders in a slice.
type Store struct{ orders []shop.Order }

// Save keeps o.
func (s *Store) Save(o shop.Order) error {
	s.orders = append(s.orders, o)
	return nil
}
