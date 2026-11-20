package api

import "testing"

func TestLooksLikeEmail(t *testing.T) {
	valid := []string{"someone@gmail.com", "first.last+tag@example.co.uk", "a_b-c@d.e"}
	for _, text := range valid {
		if !looksLikeEmail(text) {
			t.Errorf("looksLikeEmail(%q) = false, 期望 true", text)
		}
	}

	// The page publishes opaque blobs that contain an @ as part of their own
	// serialisation; none of them may be mistaken for the signed-in address.
	invalid := []string{
		"",
		"plainaddress",
		"@no-local-part.com",
		"no-domain@",
		"two@@example.com",
		"no-dot-in-domain@example",
		`%.@.[null,null,null,[3,1,[1794911143,712500000]],null,22]]`,
		"someone@example.com extra",
		"https://someone@example.com/",
	}
	for _, text := range invalid {
		if looksLikeEmail(text) {
			t.Errorf("looksLikeEmail(%q) = true, 期望 false", text)
		}
	}
}
