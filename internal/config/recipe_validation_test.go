package config

import (
	"bytes"
	"reflect"
	"testing"
)

func TestRecipeValidationMatchesLoaderWithoutMutation(t *testing.T) {
	cfg := Config{Cheap: Tier{Name: "cheap", Model: "configured", OutputLimit: 99, RecipeDigest: "old", RequiredCapabilities: []string{"chat"}}}
	for _, tc := range []struct{ document, field string }{
		{`{"schema":"hypermesh.conductor.recipe.v1","cheap":{"model":"configured","max_output_tokens":12}}`, ""},
		{`{"schema":"hypermesh.conductor.recipe.v1","cheap":{"model":"other","max_output_tokens":12}}`, "cheap-model"},
		{`{"schema":"hypermesh.conductor.recipe.v1","cheap":{"model":"configured","max_output_tokens":0}}`, "cheap-output"},
		{`{"schema":"hypermesh.conductor.recipe.v1","cheap":{"model":"configured","max_output_tokens":12,"required_capabilities":["tools"]}}`, "cheap-capabilities"},
		{`{"schema":"hypermesh.conductor.recipe.v1","cheap":{"model":"configured","max_output_tokens":12,"context_window_tokens":128}}`, "cheap-context"},
		{`{"schema":"hypermesh.conductor.recipe.v1","max_task_tokens":128,"cheap":{"model":"configured","max_output_tokens":12}}`, "task-total-limit"},
		{`{"schema":"hypermesh.conductor.recipe.v1","max_task_output_tokens":-1}`, "task-output-limit"},
		{`{"schema":"hypermesh.conductor.recipe.v1","frontier":{"model":"other","max_output_tokens":12}}`, "cheap-model"},
		{`{"schema":"wrong","private":"secret"}`, "recipe"},
	} {
		before := cfg
		result := ValidateRecipe([]byte(tc.document), cfg)
		if result.Field != tc.field || result.Valid != (tc.field == "") {
			t.Fatalf("%s: %+v", tc.document, result)
		}
		cheap, frontier := cfg.Cheap, cfg.Frontier
		if err := applyRecipe(bytes.NewBufferString(tc.document), &cheap, &frontier); (err == nil) != result.Valid {
			t.Fatal("loader disagreement")
		}
		if !reflect.DeepEqual(before, cfg) {
			t.Fatal("active configuration changed")
		}
	}
	if ValidateRecipe(bytes.Repeat([]byte(" "), 65537), cfg).Valid {
		t.Fatal("oversized draft accepted")
	}
}
