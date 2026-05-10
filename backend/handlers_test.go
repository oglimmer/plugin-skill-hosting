package main

import (
	"errors"
	"testing"
)

func TestIsUniqueViolation(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{nil, false},
		{errors.New("pq: duplicate key value violates unique constraint \"users_email_key\""), true},
		{errors.New("ERROR: unique constraint violated"), true},
		{errors.New("connection refused"), false},
	}
	for _, c := range cases {
		got := isUniqueViolation(c.err)
		if got != c.want {
			t.Errorf("isUniqueViolation(%v) = %v, want %v", c.err, got, c.want)
		}
	}
}
