package estimate

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/shtmthw/ponnoKhoj/internal/domain"
)

// OutputSchema is the JSON Schema the LLM's final answer must satisfy.
// Amounts are integer poisha (1 BDT = 100 poisha): money is never a float.
const OutputSchema = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "additionalProperties": false,
  "required": ["low", "point", "high", "confidence", "evidence_ids", "explanation_bn", "explanation_en"],
  "properties": {
    "low": {"type": "integer", "minimum": 1},
    "point": {"type": "integer", "minimum": 1},
    "high": {"type": "integer", "minimum": 1},
    "confidence": {"enum": ["low", "medium", "high"]},
    "evidence_ids": {"type": "array", "minItems": 1, "maxItems": 200, "uniqueItems": true, "items": {"type": "string", "pattern": "^[LAOCH][0-9]{1,18}$"}},
    "explanation_bn": {"type": "string", "minLength": 1, "maxLength": 1000},
    "explanation_en": {"type": "string", "minLength": 1, "maxLength": 1000}
  }
}`

var outputSchema = func() *jsonschema.Schema {
	doc, err := jsonschema.UnmarshalJSON(strings.NewReader(OutputSchema))
	if err != nil {
		panic(err) // the schema is a compile-time constant; covered by tests
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource("estimate-output.json", doc); err != nil {
		panic(err)
	}
	return c.MustCompile("estimate-output.json")
}()

// LLMOutput is the model's structured final answer.
type LLMOutput struct {
	Low           int64             `json:"low"`
	Point         int64             `json:"point"`
	High          int64             `json:"high"`
	Confidence    domain.Confidence `json:"confidence"`
	EvidenceIDs   []string          `json:"evidence_ids"`
	ExplanationBn string            `json:"explanation_bn"`
	ExplanationEn string            `json:"explanation_en"`
}

// ValidationError explains why a model answer was rejected. Its message is
// fed back to the model for the single allowed retry.
type ValidationError struct {
	Reason string
}

func (e *ValidationError) Error() string { return "estimate: invalid model output: " + e.Reason }

func invalid(format string, args ...any) error {
	return &ValidationError{Reason: fmt.Sprintf(format, args...)}
}

// extractJSONObject tolerates models that wrap their JSON in prose or code
// fences by taking the outermost {...}. The content is still validated
// strictly afterwards.
func extractJSONObject(raw string) (string, bool) {
	start := strings.IndexByte(raw, '{')
	end := strings.LastIndexByte(raw, '}')
	if start < 0 || end <= start {
		return "", false
	}
	return raw[start : end+1], true
}

// ValidateOutput checks a model answer server-side:
//
//  1. it is JSON that satisfies OutputSchema;
//  2. low <= point <= high;
//  3. every cited evidence ID was actually retrieved through a tool call in
//     this run (no invented evidence);
//  4. the range lies within the envelope around ALL gathered evidence.
//
// retrieved is the set of evidence IDs returned by tool calls in this run;
// evidence is everything the gatherer found.
func ValidateOutput(raw string, retrieved map[string]bool, evidence []Evidence, p Params) (LLMOutput, error) {
	if len(raw) > 64<<10 {
		return LLMOutput{}, invalid("answer is too long")
	}
	obj, ok := extractJSONObject(raw)
	if !ok || !utf8.ValidString(obj) {
		return LLMOutput{}, invalid("answer is not a JSON object")
	}
	inst, err := jsonschema.UnmarshalJSON(strings.NewReader(obj))
	if err != nil {
		return LLMOutput{}, invalid("answer is not valid JSON")
	}
	if err := outputSchema.Validate(inst); err != nil {
		var ve *jsonschema.ValidationError
		if errors.As(err, &ve) {
			return LLMOutput{}, invalid("answer does not match the schema: %s", firstSchemaProblem(ve))
		}
		return LLMOutput{}, invalid("answer does not match the schema")
	}
	var out LLMOutput
	dec := json.NewDecoder(bytes.NewReader([]byte(obj)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&out); err != nil {
		return LLMOutput{}, invalid("answer could not be decoded")
	}
	if out.Low > out.Point || out.Point > out.High {
		return LLMOutput{}, invalid("low <= point <= high does not hold (low=%d, point=%d, high=%d)", out.Low, out.Point, out.High)
	}
	if !hasBangla(out.ExplanationBn) {
		return LLMOutput{}, invalid("explanation_bn must be written in Bangla")
	}
	if hasCJK(out.ExplanationBn) || hasCJK(out.ExplanationEn) {
		return LLMOutput{}, invalid("explanations must be in Bangla and English only")
	}
	for _, id := range out.EvidenceIDs {
		if !retrieved[id] {
			return LLMOutput{}, invalid("evidence id %q was not returned by any tool call in this run", id)
		}
	}
	lo, hi := Envelope(evidence, p)
	if out.Low < lo || out.High > hi {
		return LLMOutput{}, invalid("range [%d, %d] is outside the allowed envelope [%d, %d] around the evidence", out.Low, out.High, lo, hi)
	}
	return out, nil
}

// firstSchemaProblem renders the most specific schema violation briefly.
func firstSchemaProblem(ve *jsonschema.ValidationError) string {
	for len(ve.Causes) > 0 {
		ve = ve.Causes[0]
	}
	loc := "/" + strings.Join(ve.InstanceLocation, "/")
	msg := ve.Error()
	if len(msg) > 200 {
		msg = msg[:200]
	}
	return loc + ": " + strings.ReplaceAll(msg, "\n", " ")
}

func hasBangla(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Bengali, r) && unicode.IsLetter(r) {
			return true
		}
	}
	return false
}

// hasCJK catches models that drift into another language mid-sentence.
func hasCJK(s string) bool {
	for _, r := range s {
		if unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul) {
			return true
		}
	}
	return false
}
