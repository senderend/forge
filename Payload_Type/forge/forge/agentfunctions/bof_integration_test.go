package agentfunctions

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	agentstructs "github.com/MythicMeta/MythicContainer/agent_structs"
)

func TestBofExecutionHandoff(t *testing.T) {
	var args agentstructs.PTTaskMessageArgsData
	mapping := agentDefinition{
		Agent: "example-agent", BofCommand: "coff_execute",
		BofFileParameterName: "bof_file", BofArgumentArrayParameterName: "bof_args", BofEntryPointParameterName: "bof_entrypoint",
	}
	typed := [][]interface{}{{"i", 0}, {"s", -1}, {"z", ""}, {"Z", "wide"}, {"b", "AAE="}}
	if err := addBofExecutionArguments(&args, mapping, "fixture-file-id", "custom_entry", typed); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]interface{}{"bof_file": "fixture-file-id", "bof_args": typed, "bof_entrypoint": "custom_entry"} {
		got, err := args.GetArg(name)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("%s = %#v, %v; want %#v", name, got, err, want)
		}
	}
	encoded, err := args.GetFinalArgs()
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]interface{}
	if err := json.Unmarshal([]byte(encoded), &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded) != 3 || decoded["bof_file"] != "fixture-file-id" || decoded["bof_entrypoint"] != "custom_entry" {
		t.Fatalf("unexpected final task JSON: %s", encoded)
	}
	if decoded["bof_args"].([]interface{})[0].([]interface{})[1] != float64(0) {
		t.Fatalf("zero lost in final task JSON: %s", encoded)
	}
}

func TestBofCLIAndExactNumericParsing(t *testing.T) {
	definition := bofCommandDefinition{CommandName: "fixture", Arguments: []bofCommandDefinitionArguments{{
		Name: "item count", Type: "int", Optional: true, Default: 7,
	}}}
	command, err := buildBofCommand(collectionSourceCommandData{}, collectionSource{}, definition)
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range []string{`{"item-count":null}`, `{"item-count":0,"item count":1}`, `{"item count":1.00000000000000000001}`, `{} {}`, `null`} {
		args, _ := agentstructs.GenerateArgsData(command.CommandParameters, agentstructs.PTTaskMessageAllData{})
		if err := command.TaskFunctionParseArgString(&args, input); err == nil {
			t.Errorf("accepted invalid input %s", input)
		}
	}
	args, _ := agentstructs.GenerateArgsData(command.CommandParameters, agentstructs.PTTaskMessageAllData{})
	if err := command.TaskFunctionParseArgString(&args, `{"item-count":0}`); err != nil {
		t.Fatal(err)
	}
	if value, _ := args.GetArg("item count"); value != 0 || !args.IsArgUserSupplied("item count") {
		t.Fatalf("alias lost explicit zero: %v", value)
	}
}

func TestBofRegistrationFailureIsAtomic(t *testing.T) {
	t.Chdir(t.TempDir())
	folder := filepath.Join(PayloadTypeName, "collections", "fixture", "package")
	if err := os.MkdirAll(folder, 0700); err != nil {
		t.Fatal(err)
	}
	manifest := `{"commands":[{"command_name":"fixture-good","arguments":[]},{"command_name":"fixture-bad","arguments":[{"name":"broken","type":"int","choices":{"bad":false}}]}]}`
	if err := os.WriteFile(filepath.Join(folder, "extension.json"), []byte(manifest), 0600); err != nil {
		t.Fatal(err)
	}
	before := len(agentstructs.AllPayloadData.Get(PayloadTypeName).GetCommands())
	err := createBofCommand(collectionSourceCommandData{CommandName: "package"}, collectionSource{Name: "fixture", CommandsFilename: "commands.json"}, true)
	if err == nil || !strings.Contains(err.Error(), "fixture-bad") || !strings.Contains(err.Error(), "broken") {
		t.Fatalf("missing contextual failure: %v", err)
	}
	if after := len(agentstructs.AllPayloadData.Get(PayloadTypeName).GetCommands()); after != before {
		t.Fatalf("partial registration: commands before %d, after %d", before, after)
	}
	if _, err := os.Stat("commands.json"); !os.IsNotExist(err) {
		t.Fatalf("invalid package persisted command registry: %v", err)
	}
}

// Optional offline corpus check: reads manifests only, never BOF binaries or
// services. Keep private collections outside the repository and pass their
// directory explicitly when validating a local installation.
func TestBofExternalManifestCorpus(t *testing.T) {
	directory := os.Getenv("FORGE_MANIFEST_DIR")
	if directory == "" {
		t.Skip("set FORGE_MANIFEST_DIR to check an external manifest corpus")
	}
	files, err := filepath.Glob(filepath.Join(directory, "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	installed, err := filepath.Glob(filepath.Join(directory, "*", "extension.json"))
	files = append(files, installed...)
	if err != nil || len(files) == 0 {
		t.Fatalf("no manifest corpus: %v", err)
	}
	commands, arguments := 0, 0
	for _, path := range files {
		t.Run(filepath.Base(path), func(t *testing.T) {
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var manifest bofCommandDefinition
			if err := json.Unmarshal(data, &manifest); err != nil {
				t.Fatal(err)
			}
			definitions := expandBofCommandDefinitions(manifest)
			if len(definitions) == 0 {
				t.Fatal("no command definitions")
			}
			for _, definition := range definitions {
				command, err := buildBofCommand(collectionSourceCommandData{}, collectionSource{}, definition)
				if err != nil {
					t.Fatal(err)
				}
				// Exercise the exact metadata JSON sent by the SDK at sync.
				if _, err := json.Marshal(command); err != nil {
					t.Fatal(err)
				}
				commands++
				arguments += len(definition.Arguments)
			}
		})
	}
	t.Logf("validated %d manifests, %d commands, %d parameters", len(files), commands, arguments)
}

func TestBofSDKDictionaryNumericPrecision(t *testing.T) {
	definition := decodeArgumentFixture(t, `[{"name":"Value","type":"int","optional":true}]`)
	command := buildArgumentFixture(t, definition)
	for _, input := range []string{`{"Value":2147483647.0000001}`, `{"Value":1.000000000000000000001}`, `null`, `{"Value":1e-100000000}`} {
		for _, dictionary := range []bool{false, true} {
			if _, err := parseArgumentFixture(command, input, dictionary); err == nil {
				t.Errorf("accepted %s, dictionary=%v", input, dictionary)
			}
		}
	}
	args, _ := agentstructs.GenerateArgsData(command.CommandParameters, agentstructs.PTTaskMessageAllData{})
	if err := command.TaskFunctionParseArgDictionary(&args, nil); err == nil {
		t.Error("direct nil dictionary accepted")
	}
	var malformed bofCommandDefinition
	if err := json.Unmarshal([]byte(`{"command_name":"bad","arguments":[{"name":"Value","type":"z","choices":[null]}]}`), &malformed); err == nil {
		t.Error("null list choice accepted")
	}
}

func TestBofNumericLabelDefault(t *testing.T) {
	definition := decodeArgumentFixture(t, `[{"name":"Value","type":"i","optional":true,"choices":{"1":10,"2":20},"default":1}]`)
	command := buildArgumentFixture(t, definition)
	if command.CommandParameters[0].DefaultValue != "1" {
		t.Fatal("numeric label default was not canonicalized")
	}
	actual, _, err := serializeArgumentFixture(t, definition, command, `{}`, false)
	if err != nil {
		t.Fatal(err)
	}
	requireArgumentJSON(t, actual, `[["i",10]]`)
	if _, err := parseArgumentFixture(command, `{"Value":1}`, false); err == nil {
		t.Fatal("numeric task value bypassed choice-label validation")
	}
}
