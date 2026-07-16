package main

import "testing"

func TestEnv(t *testing.T) {
	tests := []struct {
		name     string
		key      string
		value    string
		fallback string
		want     string
	}{
		{name: "uses fallback", key: "OMNISWITCH_TEST_EMPTY", fallback: ":8080", want: ":8080"},
		{name: "uses value", key: "OMNISWITCH_TEST_VALUE", value: ":9090", fallback: ":8080", want: ":9090"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(tt.key, tt.value)
			if got := env(tt.key, tt.fallback); got != tt.want {
				t.Fatalf("env() = %q, want %q", got, tt.want)
			}
		})
	}
}
