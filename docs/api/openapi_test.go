package api_test

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestCEOpenAPIYAMLParses(t *testing.T) {
	path := filepath.Join("openapi.ce.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", path, err)
	}

	var doc map[string]any
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatalf("yaml.Unmarshal(%q): %v", path, err)
	}

	if got := doc["openapi"]; got == nil || got == "" {
		t.Fatalf("%q missing openapi version", path)
	}
	info, ok := doc["info"].(map[string]any)
	if !ok || info["title"] == nil {
		t.Fatalf("%q missing info.title", path)
	}
	paths, ok := doc["paths"].(map[string]any)
	if !ok || len(paths) == 0 {
		t.Fatalf("%q missing paths", path)
	}
}

func TestCEOpenAPICompatibility(t *testing.T) {
	baseline := loadOpenAPI(t, "openapi.ce.baseline.yaml")
	current := loadOpenAPI(t, "openapi.ce.yaml")

	if problems := findBreakingChanges(baseline, current); len(problems) > 0 {
		t.Fatalf("OpenAPI compatibility check failed:\n- %s", strings.Join(problems, "\n- "))
	}
}

func loadOpenAPI(t *testing.T, name string) map[string]any {
	t.Helper()

	path := filepath.Join(name)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", path, err)
	}

	var doc map[string]any
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatalf("yaml.Unmarshal(%q): %v", path, err)
	}
	return doc
}

func findBreakingChanges(baseline, current map[string]any) []string {
	var problems []string

	basePaths := asMap(baseline["paths"])
	currentPaths := asMap(current["paths"])
	for path, basePathSpec := range basePaths {
		currentPathSpec, ok := currentPaths[path]
		if !ok {
			problems = append(problems, fmt.Sprintf("deleted path %s", path))
			continue
		}

		baseOps := operations(basePathSpec)
		currentOps := operations(currentPathSpec)
		for method, baseOperation := range baseOps {
			currentOperation, ok := currentOps[method]
			if !ok {
				problems = append(problems, fmt.Sprintf("deleted operation %s %s", strings.ToUpper(method), path))
				continue
			}

			baseSecurity := effectiveSecurity(baseline, baseOperation)
			currentSecurity := effectiveSecurity(current, currentOperation)
			if !reflect.DeepEqual(baseSecurity, currentSecurity) {
				problems = append(problems, fmt.Sprintf("changed auth requirement for %s %s: baseline %v, current %v", strings.ToUpper(method), path, baseSecurity, currentSecurity))
			}

			for status, baseSchema := range responseSchemas(baseline, baseOperation) {
				currentSchema, ok := responseSchemas(current, currentOperation)[status]
				if !ok {
					continue
				}
				location := fmt.Sprintf("%s %s response %s", strings.ToUpper(method), path, status)
				problems = append(problems, compareResponseFields(location, baseline, current, baseSchema, currentSchema)...)
				problems = append(problems, compareEnums(location, baseline, current, baseSchema, currentSchema)...)
			}
		}
	}

	for name, baseSchema := range schemas(baseline) {
		currentSchema, ok := schemas(current)[name]
		if !ok {
			continue
		}
		problems = append(problems, compareEnums("#/components/schemas/"+name, baseline, current, baseSchema, currentSchema)...)
	}

	sort.Strings(problems)
	return problems
}

func operations(pathSpec any) map[string]any {
	out := make(map[string]any)
	for method, operation := range asMap(pathSpec) {
		switch strings.ToLower(method) {
		case "get", "put", "post", "delete", "options", "head", "patch", "trace":
			out[strings.ToLower(method)] = operation
		}
	}
	return out
}

func effectiveSecurity(doc map[string]any, operation any) []string {
	operationMap := asMap(operation)
	if security, ok := operationMap["security"]; ok {
		return normalizeSecurity(security)
	}
	return normalizeSecurity(doc["security"])
}

func normalizeSecurity(security any) []string {
	requirements := asSlice(security)
	if len(requirements) == 0 {
		return nil
	}

	out := make([]string, 0, len(requirements))
	for _, requirement := range requirements {
		requirementMap := asMap(requirement)
		if len(requirementMap) == 0 {
			out = append(out, "anonymous")
			continue
		}

		schemes := make([]string, 0, len(requirementMap))
		for scheme, scopes := range requirementMap {
			scopeNames := make([]string, 0)
			for _, scope := range asSlice(scopes) {
				scopeNames = append(scopeNames, fmt.Sprint(scope))
			}
			sort.Strings(scopeNames)
			if len(scopeNames) > 0 {
				schemes = append(schemes, scheme+":"+strings.Join(scopeNames, ","))
			} else {
				schemes = append(schemes, scheme)
			}
		}
		sort.Strings(schemes)
		out = append(out, strings.Join(schemes, "+"))
	}
	sort.Strings(out)
	return out
}

func responseSchemas(doc map[string]any, operation any) map[string]any {
	out := make(map[string]any)
	responses := asMap(asMap(operation)["responses"])
	for status, response := range responses {
		response = resolveResponse(doc, response)
		content := asMap(asMap(response)["content"])
		for _, mediaType := range sortedKeys(content) {
			schema, ok := asMap(content[mediaType])["schema"]
			if ok {
				out[status] = schema
				break
			}
		}
	}
	return out
}

func resolveResponse(doc map[string]any, response any) any {
	responseMap := asMap(response)
	ref, ok := responseMap["$ref"].(string)
	if !ok {
		return response
	}
	const prefix = "#/components/responses/"
	if !strings.HasPrefix(ref, prefix) {
		return response
	}
	if resolved, ok := asMap(asMap(doc["components"])["responses"])[strings.TrimPrefix(ref, prefix)]; ok {
		return resolved
	}
	return response
}

func compareResponseFields(location string, baselineDoc, currentDoc map[string]any, baseSchema, currentSchema any) []string {
	var problems []string
	walkResponseFields(location, baselineDoc, currentDoc, baseSchema, currentSchema, map[string]bool{}, &problems)
	return problems
}

func walkResponseFields(location string, baselineDoc, currentDoc map[string]any, baseSchema, currentSchema any, seen map[string]bool, problems *[]string) {
	baseResolved := resolveSchema(baselineDoc, baseSchema)
	currentResolved := resolveSchema(currentDoc, currentSchema)

	baseRef := refName(baseSchema)
	currentRef := refName(currentSchema)
	seenKey := baseRef + "->" + currentRef + "@" + location
	if baseRef != "" && currentRef != "" {
		if seen[seenKey] {
			return
		}
		seen[seenKey] = true
	}

	baseProperties := asMap(baseResolved["properties"])
	currentProperties := asMap(currentResolved["properties"])
	for property, basePropertySchema := range baseProperties {
		currentPropertySchema, ok := currentProperties[property]
		if !ok {
			*problems = append(*problems, fmt.Sprintf("deleted response field %s.%s", location, property))
			continue
		}
		walkResponseFields(location+"."+property, baselineDoc, currentDoc, basePropertySchema, currentPropertySchema, seen, problems)
	}

	baseItems, hasBaseItems := baseResolved["items"]
	currentItems, hasCurrentItems := currentResolved["items"]
	if hasBaseItems && hasCurrentItems {
		walkResponseFields(location+"[]", baselineDoc, currentDoc, baseItems, currentItems, seen, problems)
	}

	for _, composition := range []string{"allOf", "anyOf", "oneOf"} {
		baseVariants := asSlice(baseResolved[composition])
		currentVariants := asSlice(currentResolved[composition])
		for i, baseVariant := range baseVariants {
			if i >= len(currentVariants) {
				*problems = append(*problems, fmt.Sprintf("deleted response schema variant %s.%s[%d]", location, composition, i))
				continue
			}
			walkResponseFields(fmt.Sprintf("%s.%s[%d]", location, composition, i), baselineDoc, currentDoc, baseVariant, currentVariants[i], seen, problems)
		}
	}
}

func compareEnums(location string, baselineDoc, currentDoc map[string]any, baseSchema, currentSchema any) []string {
	var problems []string
	walkEnums(location, baselineDoc, currentDoc, baseSchema, currentSchema, map[string]bool{}, &problems)
	return problems
}

func walkEnums(location string, baselineDoc, currentDoc map[string]any, baseSchema, currentSchema any, seen map[string]bool, problems *[]string) {
	baseResolved := resolveSchema(baselineDoc, baseSchema)
	currentResolved := resolveSchema(currentDoc, currentSchema)

	baseRef := refName(baseSchema)
	currentRef := refName(currentSchema)
	seenKey := baseRef + "->" + currentRef + "@" + location
	if baseRef != "" && currentRef != "" {
		if seen[seenKey] {
			return
		}
		seen[seenKey] = true
	}

	baseEnum := stringSet(baseResolved["enum"])
	if len(baseEnum) > 0 {
		currentEnum := stringSet(currentResolved["enum"])
		for value := range baseEnum {
			if !currentEnum[value] {
				*problems = append(*problems, fmt.Sprintf("narrowed enum %s: missing %q", location, value))
			}
		}
	}

	for property, basePropertySchema := range asMap(baseResolved["properties"]) {
		currentPropertySchema, ok := asMap(currentResolved["properties"])[property]
		if ok {
			walkEnums(location+"."+property, baselineDoc, currentDoc, basePropertySchema, currentPropertySchema, seen, problems)
		}
	}

	baseItems, hasBaseItems := baseResolved["items"]
	currentItems, hasCurrentItems := currentResolved["items"]
	if hasBaseItems && hasCurrentItems {
		walkEnums(location+"[]", baselineDoc, currentDoc, baseItems, currentItems, seen, problems)
	}

	for _, composition := range []string{"allOf", "anyOf", "oneOf"} {
		baseVariants := asSlice(baseResolved[composition])
		currentVariants := asSlice(currentResolved[composition])
		for i, baseVariant := range baseVariants {
			if i < len(currentVariants) {
				walkEnums(fmt.Sprintf("%s.%s[%d]", location, composition, i), baselineDoc, currentDoc, baseVariant, currentVariants[i], seen, problems)
			}
		}
	}
}

func resolveSchema(doc map[string]any, schema any) map[string]any {
	schemaMap := asMap(schema)
	ref, ok := schemaMap["$ref"].(string)
	if !ok {
		return schemaMap
	}
	const prefix = "#/components/schemas/"
	if !strings.HasPrefix(ref, prefix) {
		return schemaMap
	}
	if resolved, ok := schemas(doc)[strings.TrimPrefix(ref, prefix)]; ok {
		return asMap(resolved)
	}
	return schemaMap
}

func refName(schema any) string {
	ref, _ := asMap(schema)["$ref"].(string)
	return ref
}

func schemas(doc map[string]any) map[string]any {
	return asMap(asMap(doc["components"])["schemas"])
}

func sortedKeys(values map[string]any) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func stringSet(values any) map[string]bool {
	out := make(map[string]bool)
	for _, value := range asSlice(values) {
		out[fmt.Sprint(value)] = true
	}
	return out
}

func asMap(value any) map[string]any {
	values, ok := value.(map[string]any)
	if ok {
		return values
	}
	return map[string]any{}
}

func asSlice(value any) []any {
	values, ok := value.([]any)
	if ok {
		return values
	}
	return nil
}
