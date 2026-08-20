package main

import "testing"

func TestValidateServeListen(t *testing.T) {
	valid := []string{"127.0.0.1:19777", "[::1]:19777"}
	for _, addr := range valid {
		if err := validateServeListen(addr); err != nil {
			t.Errorf("%s should be valid: %v", addr, err)
		}
	}

	invalid := []string{"0.0.0.0:19777", "localhost:19777", "127.0.0.1", "bad"}
	for _, addr := range invalid {
		if err := validateServeListen(addr); err == nil {
			t.Errorf("%s should be invalid", addr)
		}
	}
}
