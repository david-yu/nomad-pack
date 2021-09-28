package manager

import (
	"fmt"
	"path"

	"github.com/hashicorp/nom/internal/pkg/loader"
	"github.com/hashicorp/nom/internal/pkg/renderer"
	"github.com/hashicorp/nom/internal/pkg/variable"
	"github.com/hashicorp/nom/pkg/pack"
	v1 "github.com/hashicorp/nomad-openapi/v1"
)

// Config contains all the user specified parameters needed to correctly run
// the pack manager.
type Config struct {
	Path            string
	VariableFiles   []string
	VariableCLIArgs map[string]string
}

// PackManager is responsible for loading, parsing, and rendering a Pack and
// all dependencies.
type PackManager struct {
	cfg      *Config
	client   *v1.Client
	renderer *renderer.Renderer
}

func NewPackManager(cfg *Config, client *v1.Client) *PackManager {
	return &PackManager{
		cfg:    cfg,
		client: client,
	}
}

// ProcessTemplates is responsible for running all backend process for the PackManager
// returning an error along with the ProcessedPack. This contains all the
// rendered templates.
//
// TODO(jrasell) figure out whether we want an error or hcl.Diagnostics return
//   object. If we stick to an error, then we need to come up with a way of
//   nicely formatting them.
func (pm *PackManager) ProcessTemplates() (*renderer.Rendered, error) {

	loadedPack, err := pm.loadAndValidatePacks()
	if err != nil {
		return nil, err
	}

	variableParser, err := variable.NewParser(&variable.ParserConfig{
		ParentName:        path.Base(pm.cfg.Path),
		RootVariableFiles: loadedPack.RootVariableFiles(),
		FileOverrides:     pm.cfg.VariableFiles,
		CLIOverrides:      pm.cfg.VariableCLIArgs,
	})
	if err != nil {
		return nil, err
	}

	parsedVars, diags := variableParser.Parse()
	if diags != nil && diags.HasErrors() {
		return nil, diags
	}

	mapVars, diags := parsedVars.ConvertVariablesToMapInterface()
	if diags != nil && diags.HasErrors() {
		return nil, diags
	}

	r := new(renderer.Renderer)
	r.Client = pm.client
	pm.renderer = r

	return r.Render(loadedPack, mapVars)
}

// ProcessOutputTemplate performs the output template rendering.
func (pm *PackManager) ProcessOutputTemplate() (string, error) { return pm.renderer.RenderOutput() }

// loadAndValidatePacks triggers the initial parent load and then starts the
// dependent pack loader. The returned pack will therefore be fully populated.
func (pm *PackManager) loadAndValidatePacks() (*pack.Pack, error) {

	parentPack, err := loader.Load(pm.cfg.Path)
	if err != nil {
		return nil, fmt.Errorf("failed to load pack: %v", err)
	}

	if err := parentPack.Metadata.Validate(); err != nil {
		return nil, fmt.Errorf("failed to validate pack metadata: %v", err)
	}

	// Using the input path to the parent pack, define the path where
	// dependencies are stored.
	depsPath := path.Join(pm.cfg.Path, "deps")

	if err := pm.loadAndValidatePack(parentPack, depsPath); err != nil {
		return nil, fmt.Errorf("failed to load pack dependency: %v", err)
	}

	return parentPack, nil
}

// loadAndValidatePack recursively loads a pack and it's dependencies. Errors
// result in an immediate return.
func (pm *PackManager) loadAndValidatePack(cur *pack.Pack, depsPath string) error {

	for _, dependency := range cur.Metadata.Dependencies {

		// Skip any dependencies that are not enabled.
		if !*dependency.Enabled {
			continue
		}

		// Load and validate the dependent pack.
		dependentPack, err := loader.Load(path.Join(depsPath, dependency.Name))
		if err != nil {
			return fmt.Errorf("failed to load dependent pack: %v", err)
		}

		if err := dependentPack.Metadata.Validate(); err != nil {
			return fmt.Errorf("failed to validate dependent pack metadata: %v", err)
		}

		// Add the dependency to the current pack.
		cur.AddDependencies(dependentPack)

		// Recursive call.
		if err := pm.loadAndValidatePack(dependentPack, depsPath); err != nil {
			return err
		}
	}

	return nil
}