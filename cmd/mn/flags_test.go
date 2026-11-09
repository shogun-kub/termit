package main

import (
	"reflect"
	"testing"
)

func TestFlagsFirst(t *testing.T) {
	t.Parallel()
	boolean := map[string]bool{"--force": true, "-f": true}
	for _, tc := range []struct {
		name string
		in   []string
		want []string
	}{
		{
			name: "flags before the argument are left alone",
			in:   []string{"--config", "a.yaml", "archive.gz"},
			want: []string{"--config", "a.yaml", "archive.gz"},
		},
		{
			name: "a flag after the argument is moved in front of it",
			in:   []string{"archive.gz", "--config", "a.yaml"},
			want: []string{"--config", "a.yaml", "archive.gz"},
		},
		{
			name: "a boolean flag takes no value",
			in:   []string{"archive.gz", "--force"},
			want: []string{"--force", "archive.gz"},
		},
		{
			name: "an equals form keeps its value",
			in:   []string{"archive.gz", "--config=a.yaml"},
			want: []string{"--config=a.yaml", "archive.gz"},
		},
		{
			name: "several arguments keep their order",
			in:   []string{"one", "--force", "two", "--config", "a.yaml"},
			want: []string{"--force", "--config", "a.yaml", "one", "two"},
		},
		{
			name: "no arguments at all",
			in:   []string{"--force"},
			want: []string{"--force"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := flagsFirst(tc.in, boolean); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("flagsFirst(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
