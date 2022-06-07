package terragrunt

import (
	"errors"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/gohcl"
	"github.com/hashicorp/hcl/v2/hclparse"
	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/zclconf/go-cty/cty"
)

const filename = "tmp.hcl"

// terragruntConfigFile represents the configuration supported in a Terragrunt configuration file
// (i.e. terragrunt.hcl)
type TerragruntConfigFile struct {
	Terraform              *TerraformConfig          `hcl:"terraform,block"`
	TerraformBinary        *string                   `hcl:"terraform_binary,attr"`
	Inputs                 *cty.Value                `hcl:"inputs,attr"`
	TerragruntDependencies []Dependency              `hcl:"dependency,block"`
	Include                []terragruntIncludeIgnore `hcl:"include,block"`
}

type TerraformConfig struct {
	Source *string `hcl:"source,attr"`
}

type Dependency struct {
	Name                                string     `hcl:",label" cty:"name"`
	ConfigPath                          string     `hcl:"config_path,attr" cty:"config_path"`
	SkipOutputs                         *bool      `hcl:"skip_outputs,attr" cty:"skip"`
	MockOutputs                         *cty.Value `hcl:"mock_outputs,attr" cty:"mock_outputs"`
	MockOutputsAllowedTerraformCommands *[]string  `hcl:"mock_outputs_allowed_terraform_commands,attr" cty:"mock_outputs_allowed_terraform_commands"`
	MockOutputsMergeWithState           *bool      `hcl:"mock_outputs_merge_with_state,attr" cty:"mock_outputs_merge_with_state"`
	RenderedOutputs                     *cty.Value `cty:"outputs"`
}

type terragruntIncludeIgnore struct {
	Name   string   `hcl:"name,label"`
	Remain hcl.Body `hcl:",remain"`
}

// TerragruntConfig represents a parsed and expanded configuration
type TerragruntConfig struct {
	Terraform              *TerraformConfig
	TerraformBinary        string
	Inputs                 map[string]interface{}
	TerragruntDependencies []Dependency
}

func ParseConfig(content []byte) (*TerragruntConfig, error) {
	file, err := parseHCL(content)
	if err != nil {
		return nil, err
	}

	// Initialize evaluation context extensions from base blocks.
	contextExtensions := EvalContextExtensions{
		DecodedDependencies: nil,
	}

	retrievedOutputs, err := decodeAndRetrieveOutputs(file, contextExtensions)
	if err != nil {
		return nil, err
	}

	contextExtensions.DecodedDependencies = retrievedOutputs

	terragruntConfigFile, err := decodeAsTerragruntConfigFile(file, contextExtensions)
	if err != nil {
		return nil, err
	}

	if terragruntConfigFile == nil {
		err = errors.New("no terragrunt configuration found")
		return nil, err
	}

	config, err := convertToTerragruntConfig(terragruntConfigFile)
	if err != nil {
		return nil, err
	}

	return config, nil
}

// parseHCL parses the HCL file content and returns a simple data structure representing the file.
func parseHCL(content []byte) (file *hcl.File, err error) {
	parser := hclparse.NewParser()

	file, parseDiagnostics := parser.ParseHCL(content, filename)
	if parseDiagnostics != nil && parseDiagnostics.HasErrors() {
		return nil, parseDiagnostics
	}
	return file, nil
}

func decodeAsTerragruntConfigFile(file *hcl.File, extensions EvalContextExtensions) (*TerragruntConfigFile, error) {
	terragruntConfig := TerragruntConfigFile{}
	err := decodeHCL(file, &terragruntConfig, extensions)
	if err != nil {
		return nil, err
	}

	return &terragruntConfig, nil
}

// decodeHCL uses the HCL parser to decode the parsed HCL into the struct specified by out.
func decodeHCL(file *hcl.File, out interface{}, extensions EvalContextExtensions) (err error) {
	// Check if we need to update the file to label any bare include blocks.
	updatedBytes, isUpdated, err := updateBareIncludeBlock(file, filename)
	if err != nil {
		return err
	}
	if isUpdated {
		// Code was updated, so we need to reparse the new updated contents. This is necessarily because the blocks
		// returned by hclparse does not support editing, and so we have to go through hclwrite, which leads to a
		// different AST representation.
		file, err = parseHCL(updatedBytes)
		if err != nil {
			return err
		}
	}

	evalContext, err := CreateTerragruntEvalContext(extensions)
	if err != nil {
		return err
	}

	decodeDiagnostics := gohcl.DecodeBody(file.Body, evalContext, out)
	if decodeDiagnostics != nil && decodeDiagnostics.HasErrors() {
		return decodeDiagnostics
	}

	return
}

// convertToTerragruntConfig convert the contents of a fully resolved Terragrunt configuration to a TerragruntConfig object
func convertToTerragruntConfig(configFromFile *TerragruntConfigFile) (*TerragruntConfig, error) {
	terragruntConfig := &TerragruntConfig{}

	terragruntConfig.Terraform = configFromFile.Terraform
	terragruntConfig.TerragruntDependencies = configFromFile.TerragruntDependencies

	if configFromFile.TerraformBinary != nil {
		terragruntConfig.TerraformBinary = *configFromFile.TerraformBinary
	}

	if configFromFile.Inputs != nil {
		inputs, err := parseCtyValueToMap(*configFromFile.Inputs)
		if err != nil {
			return nil, err
		}

		terragruntConfig.Inputs = inputs
	}

	return terragruntConfig, nil
}

// updateBareIncludeBlock searches the parsed terragrunt contents for a bare include block (include without a label),
// and convert it to one with empty string as the label. This is necessary because the hcl parser is strictly enforces
// label counts when parsing out labels with a go struct.
//
// Returns the updated contents, a boolean indicated whether anything changed, and an error (if any).
func updateBareIncludeBlock(file *hcl.File, filename string) ([]byte, bool, error) {
	const bareIncludeKey = ""

	hclFile, diags := hclwrite.ParseConfig(file.Bytes, filename, hcl.InitialPos)
	if diags.HasErrors() {
		return nil, false, diags
	}

	codeWasUpdated := false
	for _, block := range hclFile.Body().Blocks() {
		if block.Type() == "include" && len(block.Labels()) == 0 {
			if codeWasUpdated {
				return nil, false, errors.New("multiple bare include blocks (include blocks without label) is not supported")
			}
			block.SetLabels([]string{bareIncludeKey})
			codeWasUpdated = true
		}
	}
	return hclFile.Bytes(), codeWasUpdated, nil
}
