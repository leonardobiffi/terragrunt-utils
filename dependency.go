package terragrunt

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/gocty"
	ctyjson "github.com/zclconf/go-cty/cty/json"
)

type EvalContextExtensions struct {
	// DecodedDependencies are references of other terragrunt config. This contains the following attributes that map to
	// various fields related to that config:
	// - outputs: The map of outputs from the terraform state obtained by running `terragrunt output` on that target
	//            config.
	DecodedDependencies *cty.Value
}

// terragruntDependency is a struct that can be used to only decode the dependency blocks in the terragrunt config
type terragruntDependency struct {
	Dependencies []Dependency `hcl:"dependency,block"`
	Remain       hcl.Body     `hcl:",remain"`
}

// Decode the dependency blocks from the file, and then retrieve all the outputs from the remote state. Then encode the
// resulting map as a cty.Value object.
// TODO: In the future, consider allowing importing dependency blocks from included config
// NOTE FOR MAINTAINER: When implementing importation of other config blocks (e.g referencing inputs), carefully
//                      consider whether or not the implementation of the cyclic dependency detection still makes sense.
func decodeAndRetrieveOutputs(file *hcl.File, extensions EvalContextExtensions) (*cty.Value, error) {
	decodedDependency := terragruntDependency{}
	if err := decodeHCL(file, &decodedDependency, extensions); err != nil {

		return nil, err
	}

	return dependencyBlocksToCtyValue(decodedDependency.Dependencies)
}

// Encode the list of dependency blocks into a single cty.Value object that maps the dependency block name to the
// encoded dependency mapping. The encoded dependency mapping should have the attributes:
// - outputs: The map of outputs of the corresponding terraform module that lives at the target config of the dependency.
func dependencyBlocksToCtyValue(dependencyConfigs []Dependency) (*cty.Value, error) {
	// dependencyMap is the top level map that maps dependency block names to the encoded version, which includes
	// various attributes for accessing information about the target config (including the module outputs).
	dependencyMap := map[string]cty.Value{}

	for _, dependencyConfig := range dependencyConfigs {
		// Loose struct to hold the attributes of the dependency. This includes:
		// - outputs: The module outputs of the target config
		dependencyEncodingMap := map[string]cty.Value{}

		// Encode the outputs and nest under `outputs` attribute if we should get the outputs or the `mock_outputs`
		if err := dependencyConfig.setRenderedOutputs(); err != nil {
			return nil, err
		}

		if dependencyConfig.RenderedOutputs != nil {
			dependencyEncodingMap["outputs"] = *dependencyConfig.RenderedOutputs
		}

		// Once the dependency is encoded into a map, we need to convert to a cty.Value again so that it can be fed to
		// the higher order dependency map.
		dependencyEncodingMapEncoded, err := gocty.ToCtyValue(dependencyEncodingMap, generateTypeFromValuesMap(dependencyEncodingMap))
		if err != nil {
			return nil, err
		}

		// Finally, feed the encoded dependency into the higher order map under the block name
		dependencyMap[dependencyConfig.Name] = dependencyEncodingMapEncoded
	}

	// We need to convert the value map to a single cty.Value at the end so that it can be used in the execution context
	convertedOutput, err := gocty.ToCtyValue(dependencyMap, generateTypeFromValuesMap(dependencyMap))
	if err != nil {
		return nil, err
	}

	return &convertedOutput, nil
}

func (dependencyConfig *Dependency) setRenderedOutputs() error {
	if dependencyConfig == nil {
		return nil
	}

	outputVal, err := getTerragruntOutputIfAppliedElseConfiguredDefault(*dependencyConfig)
	if err != nil {
		return err
	}

	dependencyConfig.RenderedOutputs = outputVal
	return nil
}

// This will attempt to get the outputs from the target terragrunt config if it is applied. If it is not applied,
// the behavior is different depending on the configuration of the dependency.
func getTerragruntOutputIfAppliedElseConfiguredDefault(dependencyConfig Dependency) (*cty.Value, error) {
	outputVal, isEmpty, err := getTerragruntOutput(dependencyConfig)
	if err != nil {
		return nil, err
	}

	if !isEmpty {
		return outputVal, nil
	}

	return outputVal, err
}

// Return the output from the state of another module, managed by terragrunt. This function will parse the provided
// terragrunt config and extract the desired output from the remote state. Note that this will error if the targetted
// module hasn't been applied yet.
func getTerragruntOutput(dependencyConfig Dependency) (*cty.Value, bool, error) {
	type OutputMeta struct {
		Sensitive bool   `json:"sensitive"`
		Type      string `json:"type"`
		Value     string `json:"value"`
	}
	outputs := make(map[string]OutputMeta)

	mockOutputs, err := parseCtyValueToMap(*dependencyConfig.MockOutputs)
	if err != nil {

		return nil, false, err
	}
	for k, v := range mockOutputs {
		fmt.Println(k, v)
		outputs[k] = OutputMeta{
			Type:  reflect.TypeOf(v).String(),
			Value: fmt.Sprintf("%s", v),
		}
	}

	out, err := json.Marshal(outputs)
	if err != nil {
		return nil, false, err
	}

	jsonBytes := []byte(strings.TrimSpace(string(out)))
	isEmpty := string(jsonBytes) == "{}"

	outputMap, err := terraformOutputJsonToCtyValueMap(jsonBytes)
	if err != nil {
		return nil, isEmpty, err
	}

	// We need to convert the value map to a single cty.Value at the end for use in the terragrunt config.
	convertedOutput, err := gocty.ToCtyValue(outputMap, generateTypeFromValuesMap(outputMap))
	if err != nil {
		return nil, isEmpty, err
	}

	return &convertedOutput, isEmpty, nil
}

// terraformOutputJsonToCtyValueMap takes the terraform output json and converts to a mapping between output keys to the
// parsed cty.Value encoding of the json objects.
func terraformOutputJsonToCtyValueMap(jsonBytes []byte) (map[string]cty.Value, error) {
	// When getting all outputs, terraform returns a json with the data containing metadata about the types, so we
	// can't quite return the data directly. Instead, we will need further processing to get the output we want.
	// To do so, we first Unmarshal the json into a simple go map to a OutputMeta struct.
	type OutputMeta struct {
		Sensitive bool            `json:"sensitive"`
		Type      json.RawMessage `json:"type"`
		Value     json.RawMessage `json:"value"`
	}
	var outputs map[string]OutputMeta

	err := json.Unmarshal(jsonBytes, &outputs)
	if err != nil {
		return nil, err
	}
	flattenedOutput := map[string]cty.Value{}
	for k, v := range outputs {
		outputType, err := ctyjson.UnmarshalType(v.Type)
		if err != nil {
			return nil, err
		}
		outputVal, err := ctyjson.Unmarshal(v.Value, outputType)
		if err != nil {
			return nil, err
		}
		flattenedOutput[k] = outputVal
	}
	return flattenedOutput, nil
}
