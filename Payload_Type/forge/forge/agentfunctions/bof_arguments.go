package agentfunctions

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"strconv"
	"strings"

	agentstructs "github.com/MythicMeta/MythicContainer/agent_structs"
)

// bofArgumentChoices preserves JSON object order: an omitted choice may use the
// first declared label. A Go map would make that default nondeterministic.
type bofArgumentChoices struct {
	labels []string
	values []interface{}
	mapped bool
}

func (c *bofArgumentChoices) UnmarshalJSON(data []byte) error {
	*c = bofArgumentChoices{}
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return nil
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	switch token {
	case json.Delim('['):
		for decoder.More() {
			var value interface{}
			if err := decoder.Decode(&value); err != nil {
				return err
			}
			label, ok := value.(string)
			if !ok {
				return fmt.Errorf("choices must contain strings, got %T", value)
			}
			c.labels = append(c.labels, label)
		}
	case json.Delim('{'):
		c.mapped = true
		for decoder.More() {
			key, err := decoder.Token()
			if err != nil {
				return err
			}
			var value interface{}
			if err := decoder.Decode(&value); err != nil {
				return err
			}
			c.labels = append(c.labels, key.(string))
			c.values = append(c.values, value)
		}
	default:
		return fmt.Errorf("choices must be an array of strings or an object")
	}
	_, err = decoder.Token()
	return err
}

func (c bofArgumentChoices) MarshalJSON() ([]byte, error) {
	if !c.mapped {
		return json.Marshal(c.labels)
	}
	var out bytes.Buffer
	out.WriteByte('{')
	for i, label := range c.labels {
		if i > 0 {
			out.WriteByte(',')
		}
		key, _ := json.Marshal(label)
		value, err := json.Marshal(c.values[i])
		if err != nil {
			return nil, err
		}
		out.Write(key)
		out.WriteByte(':')
		out.Write(value)
	}
	out.WriteByte('}')
	return out.Bytes(), nil
}

// Decode numeric defaults without losing precision before range validation.
func (a *bofCommandDefinitionArguments) UnmarshalJSON(data []byte) error {
	type argument bofCommandDefinitionArguments
	var decoded argument
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&decoded); err != nil {
		var identity struct {
			Name string `json:"name"`
		}
		_ = json.Unmarshal(data, &identity)
		return fmt.Errorf("argument %q: %w", identity.Name, err)
	}
	*a = bofCommandDefinitionArguments(decoded)
	return nil
}

// bofArgument is an immutable compiled adapter. UI metadata and wire values are
// separate, so task preparation never rewrites the manifest or another task.
type bofArgument struct {
	parameter    agentstructs.CommandParameter
	wireType     string
	context      string
	mappedValues map[string]interface{}
	boolean      bool
	required     bool
}

func bofWireType(kind string) (string, error) {
	switch kind {
	case "int", "integer", "i":
		return "i", nil
	case "short", "s":
		return "s", nil
	case "string", "z":
		return "z", nil
	case "wstring", "Z":
		return "Z", nil
	case "file", "b":
		return "b", nil
	default:
		return "", fmt.Errorf("unsupported BOF argument type %q", kind)
	}
}

func bofWireValue(kind string, value interface{}) (interface{}, error) {
	if kind != "i" && kind != "s" {
		if text, ok := value.(string); ok {
			return text, nil
		}
		return nil, fmt.Errorf("expected string, got %T", value)
	}
	bits := 32
	if kind == "s" {
		bits = 16
	}
	var integer int64
	switch number := value.(type) {
	case json.Number:
		// Keep JSON's exact decimal value until integrality/range is checked;
		// float64 rounding could turn a tiny fractional part into an integer.
		// Bound work on malformed extreme exponents before constructing a
		// rational. Ordinary signed 16/32-bit values need only a few digits.
		text := string(number)
		if len(text) > 128 {
			return nil, fmt.Errorf("integer literal is too long")
		}
		if e := strings.IndexAny(text, "eE"); e >= 0 {
			exponent, err := strconv.Atoi(text[e+1:])
			if err != nil || exponent < -1024 || exponent > 1024 {
				return nil, fmt.Errorf("integer exponent is out of range")
			}
		}
		exact, ok := new(big.Rat).SetString(text)
		if !ok || !exact.IsInt() || !exact.Num().IsInt64() {
			return nil, fmt.Errorf("invalid integer %q", number)
		}
		integer = exact.Num().Int64()
	case string:
		parsed, err := strconv.ParseInt(number, 10, bits)
		if err != nil {
			return nil, fmt.Errorf("invalid signed %d-bit integer %q", bits, number)
		}
		integer = parsed
	case float64:
		minimum, maximum := float64(math.MinInt32), float64(math.MaxInt32)
		if bits == 16 {
			minimum, maximum = math.MinInt16, math.MaxInt16
		}
		if math.IsNaN(number) || math.IsInf(number, 0) || math.Trunc(number) != number || number < minimum || number > maximum {
			return nil, fmt.Errorf("expected signed %d-bit integer, got %v", bits, number)
		}
		integer = int64(number)
	case int:
		integer = int64(number)
	case int32:
		integer = int64(number)
	case int16:
		integer = int64(number)
	case int64:
		integer = number
	default:
		return nil, fmt.Errorf("expected signed %d-bit integer, got %T", bits, value)
	}
	minimum, maximum := int64(math.MinInt32), int64(math.MaxInt32)
	if bits == 16 {
		minimum, maximum = math.MinInt16, math.MaxInt16
	}
	if integer < minimum || integer > maximum {
		return nil, fmt.Errorf("integer %d outside signed %d-bit range", integer, bits)
	}
	return int(integer), nil
}

func compileBofArguments(definition bofCommandDefinition) ([]bofArgument, error) {
	result := make([]bofArgument, 0, len(definition.Arguments))
	names := make(map[string]bool)
	for i, original := range definition.Arguments {
		context := fmt.Sprintf("command %q argument %q", definition.CommandName, original.Name)
		cliName := strings.ReplaceAll(original.Name, " ", "-")
		if original.Name == "" || names[original.Name] || names[cliName] {
			return nil, fmt.Errorf("%s: empty or duplicate argument name", context)
		}
		names[original.Name] = true
		names[cliName] = true
		arg, err := compileBofArgument(original, i)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", context, err)
		}
		arg.context = context
		result = append(result, arg)
	}
	return result, nil
}

func compileBofArgument(original bofCommandDefinitionArguments, position int) (bofArgument, error) {
	arg := bofArgument{required: !original.Optional}
	var err error
	arg.wireType, err = bofWireType(original.Type)
	if err != nil {
		return arg, err
	}
	if original.InputOptional != nil {
		arg.required = !*original.InputOptional
	}
	parameterType := agentstructs.COMMAND_PARAMETER_TYPE_STRING
	if arg.wireType == "i" || arg.wireType == "s" {
		parameterType = agentstructs.COMMAND_PARAMETER_TYPE_NUMBER
	} else if arg.wireType == "b" {
		parameterType = agentstructs.COMMAND_PARAMETER_TYPE_FILE
	}
	arg.parameter = agentstructs.CommandParameter{
		Name: original.Name, CLIName: strings.ReplaceAll(original.Name, " ", "-"), Description: original.Description,
		ParameterType: parameterType,
		ParameterGroupInformation: []agentstructs.ParameterGroupInfo{{
			GroupName: "Default", ParameterIsRequired: arg.required, UIModalPosition: uint32(position),
		}},
	}
	choices := original.Choices
	if choices.mapped && len(choices.labels) == 0 {
		return arg, fmt.Errorf("choice mapping must not be empty")
	}
	if len(choices.labels) == 0 {
		if original.Default != nil {
			arg.parameter.DefaultValue, err = bofWireValue(arg.wireType, original.Default)
		}
		return arg, err
	}
	if arg.wireType == "b" {
		return arg, fmt.Errorf("choices are not supported for file arguments")
	}
	if choices.mapped {
		arg.mappedValues = make(map[string]interface{}, len(choices.labels))
	}
	seen := make(map[string]bool)
	for i, label := range choices.labels {
		key := label
		if choices.mapped {
			key = strings.ToLower(label)
		}
		if seen[key] {
			return arg, fmt.Errorf("duplicate or case-colliding choice label %q", label)
		}
		seen[key] = true
		value := interface{}(label)
		if choices.mapped {
			value = choices.values[i]
		}
		wireValue, err := bofWireValue(arg.wireType, value)
		if err != nil {
			return arg, fmt.Errorf("choice %q: %w", label, err)
		}
		if choices.mapped {
			arg.mappedValues[key] = wireValue
		}
	}
	arg.boolean = choices.mapped && len(choices.labels) == 2 && seen["true"] && seen["false"]
	if arg.boolean {
		arg.parameter.ParameterType = agentstructs.COMMAND_PARAMETER_TYPE_BOOLEAN
		arg.parameter.DefaultValue = false
	} else {
		arg.parameter.ParameterType = agentstructs.COMMAND_PARAMETER_TYPE_CHOOSE_ONE
		arg.parameter.Choices = append([]string(nil), choices.labels...)
		arg.parameter.DefaultValue = choices.labels[0]
	}
	if original.Default != nil {
		defaultValue := original.Default
		if choices.mapped && !arg.boolean {
			// Treat a scalar ChooseOne default as a label.
			// Resolve it only against declared labels, never as a wire value.
			switch defaultValue.(type) {
			case bool, json.Number, int, int16, int32, int64, float64:
				defaultValue = fmt.Sprint(defaultValue)
			}
		}
		if !choices.mapped && (arg.wireType == "i" || arg.wireType == "s") {
			if _, isLabel := defaultValue.(string); !isLabel {
				defaultValue, err = bofWireValue(arg.wireType, defaultValue)
				if err != nil {
					return arg, fmt.Errorf("invalid default: %w", err)
				}
				defaultValue = fmt.Sprint(defaultValue)
			}
		}
		arg.parameter.DefaultValue, err = arg.uiValue(defaultValue)
		if err != nil {
			return arg, fmt.Errorf("invalid default: %w", err)
		}
	}
	return arg, nil
}

// Dictionary labels match case-insensitively; list choices retain
// exact string matching. Numeric values cannot bypass a declared label mapping.
func (arg bofArgument) uiValue(value interface{}) (interface{}, error) {
	if arg.boolean {
		switch flag := value.(type) {
		case bool:
			return flag, nil
		case string:
			if strings.ToLower(flag) == "true" {
				return true, nil
			}
			if strings.ToLower(flag) == "false" {
				return false, nil
			}
		}
		return nil, fmt.Errorf("expected true or false, got %v", value)
	}
	if len(arg.parameter.Choices) > 0 {
		label, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf("expected a choice label, got %T", value)
		}
		for _, choice := range arg.parameter.Choices {
			if (arg.mappedValues != nil && strings.ToLower(choice) == strings.ToLower(label)) || (arg.mappedValues == nil && choice == label) {
				return choice, nil
			}
		}
		return nil, fmt.Errorf("unknown choice label %q", label)
	}
	return bofWireValue(arg.wireType, value)
}

func loadBofInput(compiled []bofArgument, args *agentstructs.PTTaskMessageArgsData, input map[string]interface{}) error {
	if input == nil {
		return fmt.Errorf("expected an argument object, got null")
	}
	normalized := make(map[string]interface{}, len(input))
	for key, value := range input {
		normalized[key] = value
	}
	for _, arg := range compiled {
		value, present := input[arg.parameter.Name]
		if arg.parameter.CLIName != arg.parameter.Name {
			if aliasValue, aliasPresent := input[arg.parameter.CLIName]; aliasPresent {
				if present {
					return fmt.Errorf("%s: supplied both name and CLI alias", arg.context)
				}
				value, present = aliasValue, true
				delete(normalized, arg.parameter.CLIName)
			}
		}
		if !present {
			continue
		}
		// The SDK otherwise treats explicit null as the parameter default,
		// losing the distinction before task construction can validate it.
		if value == nil {
			return fmt.Errorf("%s: explicit null is not a valid value; omit the argument to use its default", arg.context)
		}
		value, err := arg.uiValue(value)
		if err != nil {
			return fmt.Errorf("%s: %w", arg.context, err)
		}
		normalized[arg.parameter.Name] = value
	}
	return args.LoadArgsFromDictionary(normalized)
}

func buildBofTypedArguments(compiled []bofArgument, args *agentstructs.PTTaskMessageArgsData, readFile func(string) ([]byte, error)) ([][]interface{}, string, error) {
	typed := make([][]interface{}, 0, len(compiled))
	var display strings.Builder
	for _, arg := range compiled {
		if arg.required && !args.IsArgUserSupplied(arg.parameter.Name) {
			return nil, "", fmt.Errorf("%s: required input was not supplied", arg.context)
		}
		value, err := args.GetArg(arg.parameter.Name)
		if err != nil {
			return nil, "", fmt.Errorf("%s: %w", arg.context, err)
		}
		if value == nil {
			value = arg.parameter.DefaultValue
		}
		if value == nil {
			// BOF arguments are positional. Optional input still needs a slot;
			// never drop explicit zero/false or shift subsequent arguments.
			value = ""
			if arg.wireType == "i" || arg.wireType == "s" {
				value = 0
			}
		}
		value, err = arg.uiValue(value)
		if err != nil {
			return nil, "", fmt.Errorf("%s: %w", arg.context, err)
		}
		fmt.Fprintf(&display, "-%s %v ", arg.parameter.Name, value)
		if arg.mappedValues != nil {
			value = arg.mappedValues[strings.ToLower(fmt.Sprint(value))]
		}
		value, err = bofWireValue(arg.wireType, value)
		if err != nil {
			return nil, "", fmt.Errorf("%s: %w", arg.context, err)
		}
		if arg.wireType == "b" {
			fileID := value.(string)
			if fileID == "" {
				if arg.required {
					return nil, "", fmt.Errorf("%s: required file is empty", arg.context)
				}
				value = ""
			} else {
				content, err := readFile(fileID)
				if err != nil {
					return nil, "", fmt.Errorf("%s: read file: %w", arg.context, err)
				}
				value = base64.StdEncoding.EncodeToString(content)
			}
		}
		typed = append(typed, []interface{}{arg.wireType, value})
	}
	return typed, display.String(), nil
}

// Keep the existing typed-array packing boundary in the selected agent. Forge
// forwards ordered values and the entrypoint to the selected agent.
func addBofExecutionArguments(args *agentstructs.PTTaskMessageArgsData, agent agentDefinition, fileID, entrypoint string, typed [][]interface{}) error {
	for _, parameter := range []agentstructs.CommandParameter{
		{Name: agent.BofFileParameterName, ParameterType: agentstructs.COMMAND_PARAMETER_TYPE_FILE, DefaultValue: fileID},
		{Name: agent.BofArgumentArrayParameterName, ParameterType: agentstructs.COMMAND_PARAMETER_TYPE_TYPED_ARRAY, DefaultValue: typed},
		{Name: agent.BofEntryPointParameterName, ParameterType: agentstructs.COMMAND_PARAMETER_TYPE_STRING, DefaultValue: entrypoint},
	} {
		if err := args.AddArg(parameter); err != nil {
			return err
		}
	}
	return nil
}
