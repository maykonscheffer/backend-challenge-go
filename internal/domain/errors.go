package domain

import "errors"

// RuleError is a stable business rejection, classifiable with errors.Is/As.
type RuleError string

func (e RuleError) Error() string { return string(e) }

func FailureCode(err error) string {
	if err == nil {
		return ""
	}
	var rule RuleError
	if errors.As(err, &rule) {
		return string(rule)
	}
	return "INVALID_INPUT"
}

func ruleError(code string) error {
	if code == "" {
		return nil
	}
	return RuleError(code)
}

func (w *Wallet) Credit(m Money) error {
	if !m.valid() || m.minor < 0 {
		return ErrInvalid
	}
	if m.currency != w.balance.currency {
		return RuleError("CURRENCY_MISMATCH")
	}
	return ruleError(w.apply(m.minor))
}

func (w *Wallet) Debit(m Money) error {
	if !m.valid() || m.minor < 0 {
		return ErrInvalid
	}
	if m.currency != w.balance.currency {
		return RuleError("CURRENCY_MISMATCH")
	}
	return ruleError(w.apply(-m.minor))
}

func Decide(o Operation, m Money, player, currency string, ref *Reference) (int64, error) {
	validated, err := o.Validate()
	if err != nil {
		return 0, err
	}
	if !m.valid() || validated != m {
		return 0, ErrInvalid
	}
	delta, code := decide(o, m, player, currency, ref)
	return delta, ruleError(code)
}
