package schedule

import (
	"bytes"
	"encoding/xml"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func testJob() Job {
	return Job{
		Binary: "/opt/homebrew/bin/love",
		Times:  []TimeOfDay{{Hour: 7, Minute: 30}},
		Env: map[string]string{
			"SMTP_USER":          "me@example.com",
			"SMTP_PASSWORD_FILE": "/Users/me/.ewh/smtp-password",
			"PATH":               "/usr/bin:/bin",
		},
		LogDir: "/Users/me/.ewh/logs",
	}
}

// The property list is parsed back rather than string-matched, so the test
// proves it is a well-formed plist with the right structure and not merely that
// the writer emitted the bytes we expected it to.
func TestPlistHasTheStructureLaunchdNeeds(t *testing.T) {
	raw, err := testJob().Plist()
	if err != nil {
		t.Fatal(err)
	}
	got := parsePlist(t, raw)

	if got["Label"] != Label {
		t.Errorf("Label = %v, want %v", got["Label"], Label)
	}

	args, ok := got["ProgramArguments"].([]any)
	if !ok || len(args) != 2 {
		t.Fatalf("ProgramArguments = %#v", got["ProgramArguments"])
	}
	if args[0] != "/opt/homebrew/bin/love" || args[1] != "--daily" {
		t.Errorf("ProgramArguments = %v", args)
	}

	intervals, ok := got["StartCalendarInterval"].([]any)
	if !ok || len(intervals) != 1 {
		t.Fatalf("StartCalendarInterval = %#v, want an array of one", got["StartCalendarInterval"])
	}
	first, ok := intervals[0].(map[string]any)
	if !ok {
		t.Fatalf("the first interval is %#v", intervals[0])
	}
	if first["Hour"] != 7 || first["Minute"] != 30 {
		t.Errorf("the first interval = %v, want 7:30", first)
	}

	env, ok := got["EnvironmentVariables"].(map[string]any)
	if !ok {
		t.Fatalf("EnvironmentVariables = %#v", got["EnvironmentVariables"])
	}
	if env["SMTP_USER"] != "me@example.com" {
		t.Errorf("SMTP_USER = %v", env["SMTP_USER"])
	}

	if got["RunAtLoad"] != false {
		t.Error("RunAtLoad must be false, or installing sends an email immediately")
	}
	if got["StandardErrorPath"] != filepath.Join("/Users/me/.ewh/logs", "daily.err") {
		t.Errorf("StandardErrorPath = %v", got["StandardErrorPath"])
	}
}

// The executable path is recorded verbatim, so a relative one would produce a
// job that never runs and says nothing about why.
func TestPlistRejectsInputsThatWouldFailSilently(t *testing.T) {
	cases := map[string]func(*Job){
		"no binary":       func(j *Job) { j.Binary = "" },
		"relative binary": func(j *Job) { j.Binary = "love" },
		"no times":        func(j *Job) { j.Times = nil },
		"hour too large":  func(j *Job) { j.Times = []TimeOfDay{{Hour: 24}} },
		"hour negative":   func(j *Job) { j.Times = []TimeOfDay{{Hour: -1}} },
		"minute too big":  func(j *Job) { j.Times = []TimeOfDay{{Minute: 60}} },
		"minute negative": func(j *Job) { j.Times = []TimeOfDay{{Minute: -1}} },
	}
	for name, mutate := range cases {
		job := testJob()
		mutate(&job)
		if _, err := job.Plist(); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestPlistEscapesValues(t *testing.T) {
	job := testJob()
	job.Env["NOTE"] = `a & b < c > d "quoted"`

	raw, err := job.Plist()
	if err != nil {
		t.Fatal(err)
	}
	got := parsePlist(t, raw)

	if got["EnvironmentVariables"].(map[string]any)["NOTE"] != `a & b < c > d "quoted"` {
		t.Errorf("the value did not survive encoding: %v",
			got["EnvironmentVariables"].(map[string]any)["NOTE"])
	}
	if bytes.Contains(raw, []byte("< c >")) {
		t.Error("the raw output contains unescaped angle brackets")
	}
}

// A stable file is a diffable file, and it means reinstalling an unchanged
// configuration produces no spurious churn.
func TestPlistIsStableForTheSameInput(t *testing.T) {
	first, err := testJob().Plist()
	if err != nil {
		t.Fatal(err)
	}
	second, err := testJob().Plist()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Error("the same job produced two different property lists")
	}
}

func TestPlistOmitsAnEmptyEnvironment(t *testing.T) {
	job := testJob()
	job.Env = nil

	raw, err := job.Plist()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "EnvironmentVariables") {
		t.Error("an empty environment should not be emitted at all")
	}
}

func TestPathIsUnderLaunchAgents(t *testing.T) {
	path, err := Path()
	if err != nil {
		t.Skipf("no home directory: %v", err)
	}
	if filepath.Base(path) != Label+".plist" {
		t.Errorf("path = %q", path)
	}
	if !strings.Contains(path, filepath.Join("Library", "LaunchAgents")) {
		t.Errorf("path = %q, want it under Library/LaunchAgents", path)
	}
}

// ---------------------------------------------------------------------------
// A deliberately small property list reader. The writer emits only the handful
// of constructs launchd needs, so the reader only has to understand those.
// ---------------------------------------------------------------------------

func parsePlist(t *testing.T, data []byte) map[string]any {
	t.Helper()
	dec := xml.NewDecoder(bytes.NewReader(data))

	for {
		tok, err := dec.Token()
		if err != nil {
			t.Fatalf("no plist element: %v", err)
		}
		start, ok := tok.(xml.StartElement)
		if !ok || start.Name.Local != "plist" {
			continue
		}
		break
	}
	for {
		tok, err := dec.Token()
		if err != nil {
			t.Fatalf("no root dict: %v", err)
		}
		if start, ok := tok.(xml.StartElement); ok {
			if start.Name.Local != "dict" {
				t.Fatalf("root element is %s, want dict", start.Name.Local)
			}
			return parseDict(t, dec)
		}
	}
}

func parseDict(t *testing.T, dec *xml.Decoder) map[string]any {
	t.Helper()
	out := map[string]any{}

	for {
		tok, err := dec.Token()
		if err != nil {
			t.Fatalf("unterminated dict: %v", err)
		}
		switch element := tok.(type) {
		case xml.EndElement:
			if element.Name.Local == "dict" {
				return out
			}
		case xml.StartElement:
			if element.Name.Local != "key" {
				t.Fatalf("expected key, found %s", element.Name.Local)
			}
			name := readText(t, dec)

			// The value follows the key.
			var valueToken xml.Token
			for {
				valueToken, err = dec.Token()
				if err != nil {
					t.Fatalf("key %q has no value: %v", name, err)
				}
				if _, isText := valueToken.(xml.CharData); isText {
					continue
				}
				break
			}
			start, ok := valueToken.(xml.StartElement)
			if !ok {
				t.Fatalf("key %q is not followed by an element", name)
			}
			out[name] = parseValue(t, dec, start)
		}
	}
}

func parseValue(t *testing.T, dec *xml.Decoder, start xml.StartElement) any {
	t.Helper()
	switch start.Name.Local {
	case "string":
		return readText(t, dec)
	case "integer":
		text := readText(t, dec)
		n, err := strconv.Atoi(text)
		if err != nil {
			t.Fatalf("bad integer %q", text)
		}
		return n
	case "true":
		readText(t, dec)
		return true
	case "false":
		readText(t, dec)
		return false
	case "array":
		var items []any
		for {
			tok, err := dec.Token()
			if err != nil {
				t.Fatalf("unterminated array: %v", err)
			}
			switch element := tok.(type) {
			case xml.EndElement:
				if element.Name.Local == "array" {
					return items
				}
			case xml.StartElement:
				items = append(items, parseValue(t, dec, element))
			}
		}
	case "dict":
		return parseDict(t, dec)
	}
	t.Fatalf("unsupported element %s", start.Name.Local)
	return nil
}

func readText(t *testing.T, dec *xml.Decoder) string {
	t.Helper()
	var b strings.Builder
	for {
		tok, err := dec.Token()
		if err != nil {
			t.Fatalf("unterminated element: %v", err)
		}
		switch element := tok.(type) {
		case xml.CharData:
			b.Write(element)
		case xml.EndElement:
			return b.String()
		}
	}
}

// Three sends a day are one job with three intervals, not three agents: they
// would otherwise all write the same log and would have to be removed
// separately.
func TestPlistCarriesEveryRunTime(t *testing.T) {
	job := testJob()
	job.Times = []TimeOfDay{{Hour: 7, Minute: 30}, {Hour: 12, Minute: 30}, {Hour: 20, Minute: 30}}

	raw, err := job.Plist()
	if err != nil {
		t.Fatal(err)
	}
	got := parsePlist(t, raw)

	intervals, ok := got["StartCalendarInterval"].([]any)
	if !ok {
		t.Fatalf("StartCalendarInterval = %#v, want an array", got["StartCalendarInterval"])
	}
	if len(intervals) != 3 {
		t.Fatalf("got %d intervals, want 3", len(intervals))
	}

	want := []TimeOfDay{{7, 30}, {12, 30}, {20, 30}}
	for i, entry := range intervals {
		interval, ok := entry.(map[string]any)
		if !ok {
			t.Fatalf("interval %d is %#v", i, entry)
		}
		if interval["Hour"] != want[i].Hour || interval["Minute"] != want[i].Minute {
			t.Errorf("interval %d = %v, want %s", i, interval, want[i])
		}
	}
}

// A single time still renders as an array, so there is one shape rather than
// two code paths that could disagree.
func TestPlistAlwaysUsesAnArray(t *testing.T) {
	raw, err := testJob().Plist()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "<key>StartCalendarInterval</key>\n\t<array>") {
		t.Error("a single time should still be an array of one")
	}
}

func TestTimeOfDayString(t *testing.T) {
	for _, tc := range []struct {
		time TimeOfDay
		want string
	}{
		{TimeOfDay{7, 30}, "07:30"},
		{TimeOfDay{0, 0}, "00:00"},
		{TimeOfDay{20, 5}, "20:05"},
	} {
		if got := tc.time.String(); got != tc.want {
			t.Errorf("%+v.String() = %q, want %q", tc.time, got, tc.want)
		}
	}
}
