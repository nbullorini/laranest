package apps

import "testing"

func TestSlugify(t *testing.T) {
	cases := map[string]string{
		"My_Laravel App": "my-laravel-app",
		"--api--":        "api",
		"Ñandú":          "and",
		"":               "app",
	}
	for in, want := range cases {
		if got := Slugify(in); got != want {
			t.Errorf("Slugify(%q) = %q, want %q", in, got, want)
		}
		if !validSlug.MatchString(Slugify(in)) {
			t.Errorf("Slugify(%q) produced an invalid slug", in)
		}
	}
}
