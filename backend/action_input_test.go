package backend

import (
	"reflect"
	"testing"
)

func TestActionStdin(t *testing.T) {
	cases := []struct {
		action string
		in     ActionInput
		want   string
		err    bool
	}{
		{"start", ActionInput{}, "", false},
		{"update", ActionInput{Mod: "x", Password: "ignored1"}, "", false},
		{"fastdl", ActionInput{}, "y\ny\ny\n", false},
		{"install-default-resources", ActionInput{}, "y\n", false},
		{"mods-install", ActionInput{Mod: "metamodsource2"}, "metamodsource2\ny\n", false},
		{"mods-remove", ActionInput{Mod: "oxide"}, "oxide\ny\n", false},
		{"mods-install", ActionInput{}, "", true},
		{"mods-install", ActionInput{Mod: "y"}, "y\ny\n", false},
		{"mods-install", ActionInput{Mod: "oxide\nrustedit"}, "", true},
		{"mods-install", ActionInput{Mod: "Oxide"}, "", true},
		{"mods-install", ActionInput{Mod: "-x"}, "", true},
		{"change-password", ActionInput{Password: "Secret_123"}, "y\nSecret_123\n", false},
		{"change-password", ActionInput{}, "", true},
		{"change-password", ActionInput{Password: "short"}, "", true},
		{"change-password", ActionInput{Password: `abc"defgh`}, "", true},
		{"change-password", ActionInput{Password: "abc$(id)xx"}, "", true},
		{"change-password", ActionInput{Password: "abc`id`xxx"}, "", true},
		{"change-password", ActionInput{Password: "abc defghi"}, "", true},
		{"change-password", ActionInput{Password: "abcdefgh\nx"}, "", true},
		{"change-password", ActionInput{Password: `abcdefgh\`}, "", true},
	}
	for _, c := range cases {
		got, err := ActionStdin(c.action, c.in)
		if (err != nil) != c.err || got != c.want {
			t.Errorf("ActionStdin(%q, %+v) = %q, %v; want %q, err %v", c.action, c.in, got, err, c.want, c.err)
		}
	}
}

func TestParseAvailableMods(t *testing.T) {
	out := "\x1b[1mInstalled addons/mods\x1b[0m\n=================================\n * \x1b[32mmetamodsource2\x1b[0m\n\n" +
		"Available addons/mods\n=================================\n" +
		"Metamod:Source 2.0 - Plugins Framework (Dev Build) - https://www.metamodsource.net\n * \x1b[36mmetamodsource2\x1b[0m\n" +
		"CounterStrikeSharp - C# plugin framework - https://docs.cssharp.dev\r\n * \x1b[36mcssharp\x1b[0m\r\n" +
		"Broken - entry - x\n * Not A Valid Name\n" +
		"\nEnter an addon/mod to install (or exit to abort): \n * afterprompt\n"
	got := ParseAvailableMods(out)
	want := []ModInfo{
		{Command: "metamodsource2", Description: "Metamod:Source 2.0 - Plugins Framework (Dev Build) - https://www.metamodsource.net"},
		{Command: "cssharp", Description: "CounterStrikeSharp - C# plugin framework - https://docs.cssharp.dev"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
	if mods := ParseAvailableMods("No mods are currently available for Valheim."); len(mods) != 0 {
		t.Fatalf("expected no mods, got %+v", mods)
	}
}
