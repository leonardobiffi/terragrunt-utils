# Terragrunt Utils

Parse terragrunt file

## Example

```go
content, err := ioutil.ReadFile("terragrunt.hcl")
if err != nil {
	return
}

terragruntConfig, err := terragrunt.ParseConfig(content)
if err != nil {
	return
}
```
