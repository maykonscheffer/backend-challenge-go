package domain

import (
	"encoding/json"
	"fmt"
	"math"
)

func (m Money) valid() bool               { return m.currency == "BRL" || m.currency == "USD" || m.currency == "EUR" }
func Zero(currency string) (Money, error) { return ParseMoney(MoneyJSON{"0.00", currency}) }

// FromMinor restores exact signed internal money without decimal roundtripping.
func FromMinor(minor int64, currency string) (Money, error) {
	m := Money{minor: minor, currency: currency}
	if !m.valid() {
		return Money{}, ErrInvalid
	}
	return m, nil
}
func (m Money) compatible(other Money) error {
	if !m.valid() || !other.valid() || m.currency != other.currency {
		return fmt.Errorf("%w: incompatible or uninitialized money", ErrInvalid)
	}
	return nil
}
func (m Money) Add(other Money) (Money, error) {
	if err := m.compatible(other); err != nil {
		return Money{}, err
	}
	if (other.minor > 0 && m.minor > math.MaxInt64-other.minor) || (other.minor < 0 && m.minor < math.MinInt64-other.minor) {
		return Money{}, fmt.Errorf("%w: money overflow", ErrInvalid)
	}
	return Money{m.minor + other.minor, m.currency}, nil
}
func (m Money) Subtract(other Money) (Money, error) {
	if err := m.compatible(other); err != nil {
		return Money{}, err
	}
	if (other.minor > 0 && m.minor < math.MinInt64+other.minor) || (other.minor < 0 && m.minor > math.MaxInt64+other.minor) {
		return Money{}, fmt.Errorf("%w: money overflow", ErrInvalid)
	}
	return Money{m.minor - other.minor, m.currency}, nil
}
func (m Money) Negate() (Money, error) {
	if !m.valid() || m.minor == math.MinInt64 {
		return Money{}, fmt.Errorf("%w: invalid money or negation overflow", ErrInvalid)
	}
	return Money{-m.minor, m.currency}, nil
}
func (m Money) Compare(other Money) (int, error) {
	if err := m.compatible(other); err != nil {
		return 0, err
	}
	if m.minor < other.minor {
		return -1, nil
	}
	if m.minor > other.minor {
		return 1, nil
	}
	return 0, nil
}
func (m Money) MarshalJSON() ([]byte, error) {
	if !m.valid() {
		return nil, ErrInvalid
	}
	dto, err := m.JSON()
	if err != nil {
		return nil, err
	}
	return json.Marshal(dto)
}
