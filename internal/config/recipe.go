package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/hyperme-sh/hypermesh-conductor/internal/routing"
)

type recipeTier struct {
	RequiredCapabilities []string        `json:"required_capabilities"`
	ContextWindow        json.RawMessage `json:"context_window_tokens"`
	Model                string          `json:"model"`
	MaxOutputTokens      int             `json:"max_output_tokens"`
}

func loadRecipe(path string, cheap, frontier *Tier) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	return applyRecipe(file, cheap, frontier)
}

func applyRecipe(reader io.Reader, cheap, frontier *Tier) error {
	var recipe struct {
		TaskTokenLimit  json.RawMessage `json:"max_task_tokens"`
		TaskOutputLimit json.RawMessage `json:"max_task_output_tokens"`
		Schema          string          `json:"schema"`
		Cheap           recipeTier      `json:"cheap"`
		Frontier        recipeTier      `json:"frontier"`
	}
	hash := sha256.New()
	decoder := json.NewDecoder(io.TeeReader(reader, hash))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&recipe); err != nil {
		return fmt.Errorf("recipe: %w", err)
	}
	if decoder.Decode(new(any)) != io.EOF || recipe.Schema != "hypermesh.conductor.recipe.v1" {
		return fmt.Errorf("invalid recipe document or schema")
	}
	var totalLimit int
	if recipe.TaskTokenLimit != nil && (json.Unmarshal(recipe.TaskTokenLimit, &totalLimit) != nil || totalLimit < 1 || totalLimit > 2147483647) {
		return recipeField("task-total-limit", "invalid max_task_tokens")
	}
	var taskLimit int
	if recipe.TaskOutputLimit != nil && (json.Unmarshal(recipe.TaskOutputLimit, &taskLimit) != nil || taskLimit < 1 || taskLimit > 2147483647) {
		return recipeField("task-output-limit", "invalid max_task_output_tokens")
	}
	for _, entry := range []struct {
		name     string
		tier     *Tier
		settings recipeTier
	}{{routing.Cheap, cheap, recipe.Cheap}, {routing.Frontier, frontier, recipe.Frontier}} {
		if !entry.tier.Configured() {
			if strings.TrimSpace(entry.settings.Model) != "" || entry.settings.MaxOutputTokens != 0 || entry.settings.ContextWindow != nil {
				return recipeField("tier-selection", "recipe %s requires that tier in the endpoints file", entry.name)
			}
			continue
		}
		if entry.settings.Model != entry.tier.Model {
			return recipeField(entry.name+"-model", "recipe %s requires configured model %q", entry.tier.Name, entry.tier.Model)
		}
		if entry.settings.MaxOutputTokens < 1 || entry.settings.MaxOutputTokens > 2147483647 {
			return recipeField(entry.name+"-output", "recipe requires a positive 32-bit output limit")
		}
		if len(entry.settings.RequiredCapabilities) > 0 {
			if entry.tier.Capabilities == nil {
				return recipeField(entry.name+"-capabilities", "recipe requires a verified capability record")
			}
			if err := entry.tier.Capabilities.Require(entry.tier.Model, entry.tier.RuntimeRevision, entry.tier.Provider, entry.settings.RequiredCapabilities, false); err != nil {
				return recipeField(entry.name+"-capabilities", "%s", err)
			}
		}
		entry.tier.RequiredCapabilities = append([]string(nil), entry.settings.RequiredCapabilities...)
		var window int
		if entry.settings.ContextWindow != nil {
			if json.Unmarshal(entry.settings.ContextWindow, &window) != nil || window <= 0 || window > 2147483647 || entry.tier.InputCounter != "vllm" {
				return recipeField(entry.name+"-context", "recipe %s requires a positive 32-bit context window and configured vllm input counter", entry.tier.Name)
			}
		}
		if totalLimit > 0 && entry.tier.InputCounter != "vllm" {
			return recipeField("task-total-limit", "max_task_tokens requires a vllm counter on %s", entry.tier.Name)
		}
		entry.tier.TaskTokenLimit = totalLimit
		entry.tier.ContextWindow = window
		entry.tier.RecipeDigest = hex.EncodeToString(hash.Sum(nil))
		entry.tier.TaskOutputLimit = taskLimit
		entry.tier.OutputLimit = entry.settings.MaxOutputTokens
	}
	return nil
}

// RecipeValidation never returns parser text, configured model names or file paths.
type RecipeValidation struct {
	Schema string `json:"schema"`
	Valid  bool   `json:"valid"`
	Field  string `json:"field"`
}
type recipeFieldError struct {
	field string
	err   error
}

func (e *recipeFieldError) Error() string { return e.err.Error() }
func recipeField(field, format string, args ...any) error {
	return &recipeFieldError{field, fmt.Errorf(format, args...)}
}

// ValidateRecipe applies the loader to value copies; active limits and records are untouched.
func ValidateRecipe(data []byte, current Config) RecipeValidation {
	_, result := PreviewRecipe(data, current)
	return result
}

// PreviewRecipe returns candidate value copies only after the runtime loader accepts them.
func PreviewRecipe(data []byte, current Config) (Config, RecipeValidation) {
	result := RecipeValidation{Schema: "hypermesh.conductor.recipe-validation.v1", Field: "recipe"}
	if len(data) > 65536 {
		return Config{}, result
	}
	cheap, frontier := current.Cheap, current.Frontier
	err := applyRecipe(bytes.NewReader(data), &cheap, &frontier)
	if err == nil {
		result.Valid = true
		result.Field = ""
		current.Cheap, current.Frontier = cheap, frontier
		return current, result
	}
	var field *recipeFieldError
	if errors.As(err, &field) {
		result.Field = field.field
	}
	return Config{}, result
}
