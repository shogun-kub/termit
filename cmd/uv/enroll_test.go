package main

import "testing"

// The prefix changed what "complete this relative path" means: /sub lives at the server root, so the
// join must keep the origin and drop the prefix. Appending produced a URL no client could fetch, and it
// was printed to the operator as if it worked.
func TestAbsoluteSubscriptionURLKeepsTheOrigin(t *testing.T) {
	t.Parallel()
	cases := []struct{ base, path, want string }{
		{"https://mn.example.com", "/sub/tok", "https://mn.example.com/sub/tok"},
		{"https://mn.example.com/6rfjdqfn5nno", "/sub/tok", "https://mn.example.com/sub/tok"},
		{"https://mn.example.com/6rfjdqfn5nno/", "/sub/tok", "https://mn.example.com/sub/tok"},
		{"https://mn.example.com:8443/abc", "/sub/tok", "https://mn.example.com:8443/sub/tok"},
		{"https://mn.example.com/abc", "https://other.example/sub/tok", "https://other.example/sub/tok"},
	}
	for _, tc := range cases {
		if got := absoluteSubscriptionURL(tc.base, tc.path); got != tc.want {
			t.Errorf("absoluteSubscriptionURL(%q, %q) = %q, want %q", tc.base, tc.path, got, tc.want)
		}
	}
}
