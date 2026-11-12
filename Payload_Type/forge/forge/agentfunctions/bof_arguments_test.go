package agentfunctions

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	agentstructs "github.com/MythicMeta/MythicContainer/agent_structs"
)

func decodeArgumentFixture(t *testing.T, arguments string) bofCommandDefinition {
	t.Helper()
	var definition bofCommandDefinition
	if err := json.Unmarshal([]byte(`{"command_name":"adapter-test","entrypoint":"go","arguments":`+arguments+`}`), &definition); err != nil {
		t.Fatalf("decode manifest fixture: %v", err)
	}
	return definition
}

func buildArgumentFixture(t *testing.T, definition bofCommandDefinition) agentstructs.Command {
	t.Helper()
	command, err := buildBofCommand(collectionSourceCommandData{CommandName: "adapter-test"}, collectionSource{Name: "test"}, definition)
	if err != nil {
		t.Fatalf("build command: %v", err)
	}
	return command
}

func parseArgumentFixture(command agentstructs.Command, input string, dictionary bool) (agentstructs.PTTaskMessageArgsData, error) {
	task := agentstructs.PTTaskMessageAllData{}
	task.Task.Params = input
	args, err := agentstructs.GenerateArgsData(command.CommandParameters, task)
	if err != nil {
		return args, err
	}
	if dictionary {
		var values map[string]interface{}
		if err = json.Unmarshal([]byte(input), &values); err == nil {
			err = command.TaskFunctionParseArgDictionary(&args, values)
		}
	} else {
		err = command.TaskFunctionParseArgString(&args, input)
	}
	if err == nil {
		_, err = args.VerifyRequiredArgsHaveValues()
	}
	return args, err
}

func serializeArgumentFixture(t *testing.T, definition bofCommandDefinition, command agentstructs.Command, input string, dictionary bool) ([][]interface{}, string, error) {
	t.Helper()
	compiled, err := compileBofArguments(definition)
	if err != nil {
		return nil, "", err
	}
	args, err := parseArgumentFixture(command, input, dictionary)
	if err != nil {
		return nil, "", err
	}
	return buildBofTypedArguments(compiled, &args, func(id string) ([]byte, error) {
		return nil, fmt.Errorf("unexpected file lookup %q", id)
	})
}

func requireArgumentJSON(t *testing.T, actual interface{}, expected string) {
	t.Helper()
	data, err := json.Marshal(actual)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	if string(data) != expected {
		t.Fatalf("expected %s, got %s", expected, data)
	}
}

func TestBofEnabledBooleanRegistrationAndWireValues(t *testing.T) {
	definition := decodeArgumentFixture(t, `[{"name":"Enabled","type":"int","optional":true,"input_optional":true,"choices":{"true":1,"false":0},"default":false}]`)
	command := buildArgumentFixture(t, definition)
	parameter := command.CommandParameters[0]
	if parameter.ParameterType != agentstructs.COMMAND_PARAMETER_TYPE_BOOLEAN || parameter.DefaultValue != false {
		t.Fatalf("Boolean registration expected; got type %q, default %#v", parameter.ParameterType, parameter.DefaultValue)
	}
	if parameter.ParameterGroupInformation[0].ParameterIsRequired {
		t.Fatal("input_optional Enabled must not be required")
	}
	for _, dictionary := range []bool{false, true} {
		for _, tc := range []struct {
			name, input, expected string
		}{
			{"omitted", `{}`, `[["i",0]]`},
			{"false", `{"Enabled":false}`, `[["i",0]]`},
			{"true", `{"Enabled":true}`, `[["i",1]]`},
		} {
			t.Run(fmt.Sprintf("%s/dictionary=%t", tc.name, dictionary), func(t *testing.T) {
				typed, _, err := serializeArgumentFixture(t, definition, command, tc.input, dictionary)
				if err != nil {
					t.Fatal(err)
				}
				requireArgumentJSON(t, typed, tc.expected)
			})
		}
	}
}

func TestBofMappedChoicesPreserveLabelsOrderAndDefaults(t *testing.T) {
	definition := decodeArgumentFixture(t, `[{"name":"Mode","type":"int","optional":true,"input_optional":true,"choices":{"First":1,"Second":2,"All":3},"default":"First"}]`)
	command := buildArgumentFixture(t, definition)
	parameter := command.CommandParameters[0]
	if parameter.ParameterType != agentstructs.COMMAND_PARAMETER_TYPE_CHOOSE_ONE {
		t.Fatalf("expected ChooseOne, got %q", parameter.ParameterType)
	}
	if !reflect.DeepEqual(parameter.Choices, []string{"First", "Second", "All"}) || parameter.DefaultValue != "First" {
		t.Fatalf("expected ordered labels and First default, got choices %v, default %#v", parameter.Choices, parameter.DefaultValue)
	}
	for _, tc := range []struct{ input, expected string }{
		{`{}`, `[["i",1]]`},
		{`{"Mode":"first"}`, `[["i",1]]`},
		{`{"Mode":"sEcOnD"}`, `[["i",2]]`},
		{`{"Mode":"All"}`, `[["i",3]]`},
	} {
		for _, dictionary := range []bool{false, true} {
			typed, _, err := serializeArgumentFixture(t, definition, command, tc.input, dictionary)
			if err != nil {
				t.Fatalf("%s: %v", tc.input, err)
			}
			requireArgumentJSON(t, typed, tc.expected)
		}
	}
}

func TestBofExplicitFalseOverridesTrueDefault(t *testing.T) {
	definition := decodeArgumentFixture(t, `[{"name":"Enabled","type":"i","optional":true,"choices":{"true":1,"false":0},"default":true}]`)
	command := buildArgumentFixture(t, definition)
	for _, tc := range []struct{ input, expected string }{
		{`{}`, `[["i",1]]`},
		{`{"Enabled":false}`, `[["i",0]]`},
	} {
		typed, _, err := serializeArgumentFixture(t, definition, command, tc.input, true)
		if err != nil {
			t.Fatal(err)
		}
		requireArgumentJSON(t, typed, tc.expected)
	}
}

func TestBofOnlyTrueFalsePairRegistersAsBoolean(t *testing.T) {
	for _, choices := range []string{`{"yes":1,"no":0}`, `{"true":1,"false":0,"auto":2}`, `{"false":0}`} {
		definition := decodeArgumentFixture(t, `[{"name":"Value","type":"i","choices":`+choices+`}]`)
		command := buildArgumentFixture(t, definition)
		if got := command.CommandParameters[0].ParameterType; got != agentstructs.COMMAND_PARAMETER_TYPE_CHOOSE_ONE {
			t.Fatalf("expected ChooseOne for %s, got %s", choices, got)
		}
	}
}

func TestBofImplicitChoiceDefaults(t *testing.T) {
	for _, tc := range []struct {
		name, argument, wire string
		defaultValue         interface{}
	}{
		{"boolean-false", `{"name":"value","type":"i","optional":true,"choices":{"true":9,"false":3}}`, `[["i",3]]`, false},
		{"source-first", `{"name":"value","type":"s","optional":true,"choices":{"Zulu":2,"Alpha":1}}`, `[["s",2]]`, "Zulu"},
		{"string-mapping", `{"name":"value","type":"Z","optional":true,"choices":{"Zulu":"z","Alpha":"a"}}`, `[["Z","z"]]`, "Zulu"},
		{"canonical-label-default", `{"name":"value","type":"s","optional":true,"choices":{"Zulu":2,"Alpha":1},"default":"alpha"}`, `[["s",1]]`, "Alpha"},
		{"boolean-label-default", `{"name":"value","type":"i","optional":true,"choices":{"true":1,"false":0},"default":"TRUE"}`, `[["i",1]]`, true},
		{"numeric-label", `{"name":"value","type":"i","optional":true,"choices":{"1":7,"2":9},"default":"1"}`, `[["i",7]]`, "1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			definition := decodeArgumentFixture(t, "["+tc.argument+"]")
			command := buildArgumentFixture(t, definition)
			if !reflect.DeepEqual(command.CommandParameters[0].DefaultValue, tc.defaultValue) {
				t.Fatalf("expected default %#v, got %#v", tc.defaultValue, command.CommandParameters[0].DefaultValue)
			}
			typed, _, err := serializeArgumentFixture(t, definition, command, `{}`, true)
			if err != nil {
				t.Fatal(err)
			}
			requireArgumentJSON(t, typed, tc.wire)
		})
	}
}

func TestBofNumericStringDefaultAndExplicitZeroValues(t *testing.T) {
	definition := decodeArgumentFixture(t, `[{"name":"Depth","type":"integer","optional":true,"default":"-1"},{"name":"Text","type":"wstring","optional":true,"default":"nonempty"}]`)
	command := buildArgumentFixture(t, definition)
	if command.CommandParameters[0].ParameterType != agentstructs.COMMAND_PARAMETER_TYPE_NUMBER {
		t.Fatalf("expected Number, got %s", command.CommandParameters[0].ParameterType)
	}
	requireArgumentJSON(t, command.CommandParameters[0].DefaultValue, `-1`)
	for _, tc := range []struct{ input, expected string }{
		{`{}`, `[["i",-1],["Z","nonempty"]]`},
		{`{"Depth":0,"Text":""}`, `[["i",0],["Z",""]]`},
		{`{"Depth":-2147483648,"Text":"ok"}`, `[["i",-2147483648],["Z","ok"]]`},
		{`{"Depth":2147483647,"Text":"ok"}`, `[["i",2147483647],["Z","ok"]]`},
	} {
		typed, _, err := serializeArgumentFixture(t, definition, command, tc.input, false)
		if err != nil {
			t.Fatalf("%s: %v", tc.input, err)
		}
		requireArgumentJSON(t, typed, tc.expected)
	}
}

func TestBofInputOptionalOverridesWireOptionalAndKeepsSlots(t *testing.T) {
	definition := decodeArgumentFixture(t, `[{"name":"First","type":"int","optional":false,"input_optional":true},{"name":"Second","type":"z","optional":true,"input_optional":true},{"name":"Third","type":"short","optional":true,"input_optional":false}]`)
	command := buildArgumentFixture(t, definition)
	for i, wantRequired := range []bool{false, false, true} {
		if got := command.CommandParameters[i].ParameterGroupInformation[0].ParameterIsRequired; got != wantRequired {
			t.Fatalf("argument %d required=%t, expected %t", i, got, wantRequired)
		}
	}
	typed, _, err := serializeArgumentFixture(t, definition, command, `{"Third":7}`, true)
	if err != nil {
		t.Fatal(err)
	}
	requireArgumentJSON(t, typed, `[["i",0],["z",""],["s",7]]`)
	if _, _, err := serializeArgumentFixture(t, definition, command, `{}`, true); err == nil {
		t.Fatal("omitted required Third argument was accepted")
	}
}

func TestBofOptionalFileKeepsSlotWithoutFetching(t *testing.T) {
	definition := decodeArgumentFixture(t, `[{"name":"Blob","type":"b","optional":true},{"name":"Short","type":"s"}]`)
	command := buildArgumentFixture(t, definition)
	for _, tc := range []struct{ input, expected string }{
		{`{"Short":-32768}`, `[["b",""],["s",-32768]]`},
		{`{"Blob":"","Short":32767}`, `[["b",""],["s",32767]]`},
	} {
		typed, _, err := serializeArgumentFixture(t, definition, command, tc.input, true)
		if err != nil {
			t.Fatalf("optional file must not require a content lookup: %v", err)
		}
		requireArgumentJSON(t, typed, tc.expected)
	}
}

func TestBofRequiredZeroAndEmptyAreDifferentFromOmission(t *testing.T) {
	definition := decodeArgumentFixture(t, `[{"name":"Number","type":"i"},{"name":"Text","type":"z"}]`)
	command := buildArgumentFixture(t, definition)
	for _, input := range []string{`{}`, `{"Number":0}`, `{"Text":""}`} {
		if _, _, err := serializeArgumentFixture(t, definition, command, input, true); err == nil {
			t.Fatalf("omitted required argument accepted: %s", input)
		}
	}
	typed, _, err := serializeArgumentFixture(t, definition, command, `{"Number":0,"Text":""}`, true)
	if err != nil {
		t.Fatal(err)
	}
	requireArgumentJSON(t, typed, `[["i",0],["z",""]]`)
}

func TestBofRequiredArgumentsWithDefaultsStillRequireInput(t *testing.T) {
	definition := decodeArgumentFixture(t, `[{"name":"Value","type":"i","default":7}]`)
	command := buildArgumentFixture(t, definition)
	if _, _, err := serializeArgumentFixture(t, definition, command, `{}`, true); err == nil {
		t.Fatal("a default must not silently bypass a required input")
	}
}

func TestBofStandardStringArrayChoicesRemainCaseSensitive(t *testing.T) {
	definition := decodeArgumentFixture(t, `[{"name":"Mode","type":"string","choices":["Read","Write"],"default":"Read"}]`)
	command := buildArgumentFixture(t, definition)
	parameter := command.CommandParameters[0]
	if parameter.ParameterType != agentstructs.COMMAND_PARAMETER_TYPE_CHOOSE_ONE || !reflect.DeepEqual(parameter.Choices, []string{"Read", "Write"}) {
		t.Fatalf("unexpected standard choices registration: %#v", parameter)
	}
	typed, _, err := serializeArgumentFixture(t, definition, command, `{"Mode":"Write"}`, false)
	if err != nil {
		t.Fatal(err)
	}
	requireArgumentJSON(t, typed, `[["z","Write"]]`)
	for _, input := range []string{`{"Mode":"write"}`, `{"Mode":"Other"}`, `{"Mode":1}`} {
		if _, _, err := serializeArgumentFixture(t, definition, command, input, true); err == nil {
			t.Fatalf("invalid standard choice accepted: %s", input)
		}
	}
}

func TestBofFileContentReadIsInjectedAndBase64Encoded(t *testing.T) {
	definition := decodeArgumentFixture(t, `[{"name":"Blob","type":"file"},{"name":"Count","type":"s","optional":true,"default":-1}]`)
	command := buildArgumentFixture(t, definition)
	compiled, err := compileBofArguments(definition)
	if err != nil {
		t.Fatal(err)
	}
	args, err := parseArgumentFixture(command, `{"Blob":"file-id"}`, true)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	typed, display, err := buildBofTypedArguments(compiled, &args, func(id string) ([]byte, error) {
		calls++
		if id != "file-id" {
			t.Fatalf("unexpected file id %q", id)
		}
		return []byte{0, 255, 1}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || !strings.Contains(display, "file-id") {
		t.Fatalf("expected one read and file-id display, got calls=%d, display=%q", calls, display)
	}
	requireArgumentJSON(t, typed, `[["b","AP8B"],["s",-1]]`)
	args, err = parseArgumentFixture(command, `{"Blob":"file-id"}`, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = buildBofTypedArguments(compiled, &args, func(string) ([]byte, error) {
		return nil, errors.New("fixture file unavailable")
	}); err == nil || !strings.Contains(err.Error(), "fixture file unavailable") {
		t.Fatalf("file failure not propagated: %v", err)
	}
}

func TestBofInvalidManifestArgumentsFailRegistration(t *testing.T) {
	for _, tc := range []struct{ name, argument string }{
		{"fractional-map", `{"name":"Value","type":"i","choices":{"Good":1,"Bad":1.5}}`},
		{"overflow-map", `{"name":"Value","type":"i","choices":{"Bad":2147483648}}`},
		{"short-overflow-map", `{"name":"Value","type":"s","choices":{"Bad":32768}}`},
		{"boolean-map-value", `{"name":"Value","type":"i","choices":{"Bad":true}}`},
		{"null-map-value", `{"name":"Value","type":"i","choices":{"Bad":null}}`},
		{"object-map-value", `{"name":"Value","type":"i","choices":{"Bad":{"nested":1}}}`},
		{"invalid-numeric-string-map", `{"name":"Value","type":"i","choices":{"Bad":"wrong"}}`},
		{"case-collision", `{"name":"Value","type":"i","choices":{"Read":1,"read":2}}`},
		{"unknown-label-default", `{"name":"Value","type":"i","choices":{"Read":1},"default":"Other"}`},
		{"numeric-map-default", `{"name":"Value","type":"i","choices":{"Read":1},"default":1}`},
		{"numeric-string-map-default", `{"name":"Value","type":"i","choices":{"Read":1},"default":"1"}`},
		{"invalid-boolean-default", `{"name":"Value","type":"i","choices":{"true":1,"false":0},"default":"yes"}`},
		{"overflow-default", `{"name":"Value","type":"i","default":2147483648}`},
		{"underflow-default", `{"name":"Value","type":"i","default":-2147483649}`},
		{"fractional-default", `{"name":"Value","type":"s","default":1.5}`},
		{"short-underflow-default", `{"name":"Value","type":"s","default":-32769}`},
		{"precise-fractional-default", `{"name":"Value","type":"i","default":1.00000000000000001}`},
		{"precise-fractional-map", `{"name":"Value","type":"i","choices":{"Bad":2147483647.00000001}}`},
		{"unknown-list-default", `{"name":"Value","type":"z","choices":["Read","Write"],"default":"read"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			definition := decodeArgumentFixture(t, "["+tc.argument+"]")
			if _, err := compileBofArguments(definition); err == nil {
				t.Fatal("invalid manifest accepted by compiler")
			}
			if _, err := buildBofCommand(collectionSourceCommandData{}, collectionSource{}, definition); err == nil {
				t.Fatal("invalid manifest accepted by registration")
			}
		})
	}
}

func TestBofInvalidTaskValuesFailBeforeSerialization(t *testing.T) {
	for _, tc := range []struct{ name, argument, input string }{
		{"unknown-mapped-label", `{"name":"Value","type":"i","choices":{"Read":1}}`, `{"Value":"Other"}`},
		{"numeric-choice-bypass", `{"name":"Value","type":"i","choices":{"Read":1}}`, `{"Value":1}`},
		{"numeric-string-choice-bypass", `{"name":"Value","type":"i","choices":{"Read":1}}`, `{"Value":"1"}`},
		{"boolean-numeric-bypass", `{"name":"Value","type":"i","choices":{"true":1,"false":0}}`, `{"Value":1}`},
		{"boolean-invalid-label", `{"name":"Value","type":"i","choices":{"true":1,"false":0}}`, `{"Value":"yes"}`},
		{"int-fraction", `{"name":"Value","type":"i"}`, `{"Value":1.1}`},
		{"int-overflow", `{"name":"Value","type":"i"}`, `{"Value":2147483648}`},
		{"int-underflow", `{"name":"Value","type":"i"}`, `{"Value":-2147483649}`},
		{"short-overflow", `{"name":"Value","type":"s"}`, `{"Value":32768}`},
		{"short-underflow", `{"name":"Value","type":"s"}`, `{"Value":-32769}`},
		{"bool-as-number", `{"name":"Value","type":"i"}`, `{"Value":true}`},
		{"object-as-number", `{"name":"Value","type":"i"}`, `{"Value":{}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			definition := decodeArgumentFixture(t, "["+tc.argument+"]")
			command := buildArgumentFixture(t, definition)
			for _, dictionary := range []bool{false, true} {
				if _, _, err := serializeArgumentFixture(t, definition, command, tc.input, dictionary); err == nil {
					t.Fatalf("invalid value accepted (dictionary=%t): %s", dictionary, tc.input)
				}
			}
		})
	}
}

func TestBofTaskExplicitNullIsRejectedByBothParseHooks(t *testing.T) {
	definition := decodeArgumentFixture(t, `[{"name":"Value","type":"i","optional":true,"default":7}]`)
	command := buildArgumentFixture(t, definition)
	for _, dictionary := range []bool{false, true} {
		if _, err := parseArgumentFixture(command, `{"Value":null}`, dictionary); err == nil {
			t.Fatalf("null accepted by parse hook (dictionary=%t)", dictionary)
		}
	}
}

func TestBofStringParserRejectsFractionsThatWouldRoundToInteger(t *testing.T) {
	definition := decodeArgumentFixture(t, `[{"name":"Value","type":"i"}]`)
	command := buildArgumentFixture(t, definition)
	for _, input := range []string{`{"Value":1.00000000000000001}`, `{"Value":2147483647.00000001}`} {
		if _, _, err := serializeArgumentFixture(t, definition, command, input, false); err == nil {
			t.Fatalf("fractional JSON value rounded into an integer: %s", input)
		}
	}
}

func TestBofInheritedArgumentsRemainUnchangedAcrossCommandsAndTasks(t *testing.T) {
	const manifest = `{"name":"bundle","version":"test","entrypoint":"go","arguments":[{"name":"Enabled","type":"i","optional":true,"choices":{"true":1,"false":0},"default":false},{"name":"Mode","type":"s","optional":true,"choices":{"Zulu":2,"Alpha":1}}],"commands":[{"command_name":"first"},{"command_name":"second"}]}`
	var packageDefinition bofCommandDefinition
	if err := json.Unmarshal([]byte(manifest), &packageDefinition); err != nil {
		t.Fatal(err)
	}
	// Exercise the same disk loader used during registration on every pass.
	t.Chdir(t.TempDir())
	manifestDirectory := filepath.Join(PayloadTypeName, "collections", "test", "bundle")
	if err := os.MkdirAll(manifestDirectory, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(manifestDirectory, "extension.json"), []byte(manifest), 0600); err != nil {
		t.Fatal(err)
	}
	before, err := json.Marshal(packageDefinition)
	if err != nil {
		t.Fatal(err)
	}
	for repeat := 0; repeat < 3; repeat++ {
		definitions := expandBofCommandDefinitions(packageDefinition)
		loaded, err := loadBofCommandDefinitions(collectionSourceCommandData{CommandName: "bundle"}, collectionSource{Name: "test"})
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(loaded, definitions) {
			t.Fatal("disk load and in-memory expansion produced different definitions")
		}
		if len(definitions) != 2 {
			t.Fatalf("expected two inherited commands, got %d", len(definitions))
		}
		for _, definition := range definitions {
			if definition.Version != "test" || definition.Entrypoint != "go" {
				t.Fatalf("package metadata was not inherited: %#v", definition)
			}
			command := buildArgumentFixture(t, definition)
			for _, tc := range []struct{ input, expected string }{
				{`{"Enabled":true,"Mode":"Alpha"}`, `[["i",1],["s",1]]`},
				{`{}`, `[["i",0],["s",2]]`},
				{`{"Enabled":false,"Mode":"Zulu"}`, `[["i",0],["s",2]]`},
			} {
				typed, _, err := serializeArgumentFixture(t, definition, command, tc.input, true)
				if err != nil {
					t.Fatal(err)
				}
				requireArgumentJSON(t, typed, tc.expected)
			}
		}
	}
	after, err := json.Marshal(packageDefinition)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatalf("manifest mutated across builds/tasks:\nbefore %s\nafter  %s", before, after)
	}
}
