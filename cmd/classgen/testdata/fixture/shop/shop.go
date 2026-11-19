// Package shop sells things: the generator's test fixture.
package shop

// Store keeps orders.
type Store interface {
	Save(o Order) error
}

// Order is one sale.
type Order struct {
	ID    int
	Lines []Line
	note  string
}

// Line is one item of an order.
type Line struct{ Item string }

// Priced has a price.
type Priced interface {
	Price() int
}

// Price is a line's price: a value receiver, so Line itself is Priced.
func (l Line) Price() int { return len(l.Item) }

// Clock tells the time.
type Clock struct{ Now func() int }

// Service sells through a store.
type Service struct {
	store Store
	Clock
}

// NewService returns a service over store.
func NewService(store Store) *Service { return &Service{store: store} }

// Sell saves an order.
func (s *Service) Sell(o Order) error { return s.store.Save(o) }

// noted reads an order's note.
func noted(o Order) string { return o.note }
