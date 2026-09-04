package approve

import "testing"

func TestParseConfirmJSON(t *testing.T) {
	cases := []struct {
		name    string
		stdout  string
		want    bool
		wantErr bool
	}{
		{"bare yes", "yes", true, false},
		{"bare Y padded", "  Y\n", true, false},
		{"bare no", "no", false, false},
		{"bare n", "n", false, false},
		{"json yes code 0", `{"code":0,"text":"yes"}`, true, false},
		{"json no code 0", `{"code":0,"text":"no"}`, false, false},
		{"json cancel code -1", `{"code":-1,"text":""}`, false, false},
		{"cancel wins over yes text", `{"code":-1,"text":"yes"}`, false, false},
		{"cancel wins over Y text", `{"text":"Y","code":-1}`, false, false},
		{"yes text without code", `{"text":"yes"}`, true, false},
		{"code 0 without text denies", `{"code":0}`, false, false},
		{"empty text denies", `{"code":0,"text":""}`, false, false},
		{"empty stdout errors", "", false, true},
		{"whitespace stdout errors", "  \n", false, true},
		{"invalid json errors", "not json at all {{{", false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseConfirmJSON(tc.stdout)
			if tc.wantErr != (err != nil) {
				t.Fatalf("err=%v, wantErr=%v", err, tc.wantErr)
			}
			if got != tc.want {
				t.Fatalf("got=%v, want=%v", got, tc.want)
			}
		})
	}
}
